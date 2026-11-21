// Package contract holds the helpers behind `bairn smoke immich`, the
// tag-time gate that verifies the Immich wire contract against a live
// server: log in, mint an ephemeral API key, upload a tiny test asset,
// assert the response, and clean up the asset and the key. It catches
// wire format issues, controller-layer enforcement, and persistence-path
// behavior that no static spec can model.
package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/safehttp"
)

// ImmichLogin authenticates with email + password and returns the
// session access token. Used by the CI smoke gate when the test
// user is provisioned via group CI variables (IMMICH_BAIRN_USER /
// IMMICH_BAIRN_PASSWORD) instead of a long-lived API key.
func ImmichLogin(ctx context.Context, client *http.Client, baseURL, email, password string) (string, error) {
	if client == nil {
		client = defaultClient()
	}
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("contract: login: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("contract: login HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("contract: parse login: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("contract: login response has no accessToken")
	}
	return out.AccessToken, nil
}

// ImmichAPIKey describes an ephemeral API key minted for the round-
// trip smoke. The Secret is what subsequent calls send as
// x-api-key; the ID is what DeleteAPIKey accepts for cleanup.
type ImmichAPIKey struct {
	ID     string
	Secret string
}

// MintImmichAPIKey creates a short-lived API key with the given
// permissions. The CI smoke uses this so the rest of the gate
// runs through the same x-api-key code path bairn uses in
// production.
func MintImmichAPIKey(ctx context.Context, client *http.Client, baseURL, token, name string, permissions []string) (*ImmichAPIKey, error) {
	if client == nil {
		client = defaultClient()
	}
	body, _ := json.Marshal(map[string]any{"name": name, "permissions": permissions})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/api-keys", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contract: mint api key: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("contract: mint api key HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out struct {
		Secret string `json:"secret"`
		APIKey struct {
			ID string `json:"id"`
		} `json:"apiKey"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("contract: parse mint api key: %w", err)
	}
	return &ImmichAPIKey{ID: out.APIKey.ID, Secret: out.Secret}, nil
}

// DeleteImmichAPIKey revokes the key minted by MintImmichAPIKey.
// Best-effort: callers should defer this and not fail the test on
// cleanup error, but should log loudly so leaked keys are noticed.
func DeleteImmichAPIKey(ctx context.Context, client *http.Client, baseURL, token, keyID string) error {
	if client == nil {
		client = defaultClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, baseURL+"/api/api-keys/"+keyID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("contract: delete api key: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("contract: delete api key HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// DeleteImmichAsset removes the asset uploaded during the round-
// trip smoke. force=true bypasses the trash and deletes
// immediately, which is what we want for a test asset that should
// leave no trace.
func DeleteImmichAsset(ctx context.Context, client *http.Client, baseURL, apiKey, assetID string) error {
	if client == nil {
		client = defaultClient()
	}
	body, _ := json.Marshal(map[string]any{
		"ids":   []string{assetID},
		"force": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, baseURL+"/api/assets", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("contract: delete asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("contract: delete asset HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// defaultClient is the fallback for calls that carry a password, session
// token or API key; it refuses cross-host and downgrade redirects.
func defaultClient() *http.Client { return safehttp.NewClient(30 * time.Second) }
