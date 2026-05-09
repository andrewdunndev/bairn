// Package contract verifies a sink's wire contract against a live
// server. Two modes:
//
//   - Round-trip (the gate): authenticates via user+password,
//     mints an ephemeral API key, uploads a tiny test asset,
//     asserts the response, deletes the asset, deletes the API
//     key. Catches wire format issues, controller-layer
//     enforcement, and persistence-path behavior. Used by the
//     CI smoke job at tag time against a quota-limited test user.
//
//   - Probe-only: sends a deliberately-incomplete request and
//     parses the validator's rejection. Non-destructive. Used by
//     `bairn smoke immich --probe-only` for diagnostics / offline
//     analysis of the required-field set.
//
// The round-trip is the load-bearing gate. The probe-only mode is
// for cases where you can't or shouldn't actually upload (public
// demo Immichs, audit modes, manifest capture).
package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sort"
	"strings"
	"time"
)

// ImmichLogin authenticates with email + password and returns the
// session access token. Used by the CI smoke gate when the test
// user is provisioned via group CI variables (IMMICH_BAIRN_USER /
// IMMICH_BAIRN_PASSWORD) instead of a long-lived API key.
func ImmichLogin(ctx context.Context, client *http.Client, baseURL, email, password string) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
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
		client = &http.Client{Timeout: 30 * time.Second}
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
		client = &http.Client{Timeout: 30 * time.Second}
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
		client = &http.Client{Timeout: 30 * time.Second}
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

// ImmichManifest is the on-disk shape the probe captures and the
// contract test reads. Pinned to a server version so a future bump
// can be reasoned about.
type ImmichManifest struct {
	ImmichVersion string   `json:"immich_version"`
	CapturedAt    string   `json:"captured_at"`
	Endpoint      string   `json:"endpoint"`
	Required      []string `json:"required"`
}

// ProbeImmichUploadRequiredFields sends a deliberately-incomplete
// multipart POST to /api/assets and parses the 400 response's
// class-validator error messages to discover which fields the live
// server enforces as required.
//
// Includes a minimal valid assetData part so the multer file-parse
// step succeeds and the request reaches the validator (where it is
// rejected). The validator runs before any persistence, so this is
// non-destructive against the operator's library.
func ProbeImmichUploadRequiredFields(ctx context.Context, httpClient *http.Client, baseURL, apiKey string) (*ImmichManifest, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	body, contentType, err := buildMinimalUploadProbe()
	if err != nil {
		return nil, fmt.Errorf("contract: build probe: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/assets", body)
	if err != nil {
		return nil, fmt.Errorf("contract: build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", apiKey)
	// Stub checksum so the AssetUploadInterceptor's duplicate-check
	// path runs to completion. Any 40-char hex works; the validator
	// runs after the checksum-lookup branch.
	req.Header.Set("x-immich-checksum", "0000000000000000000000000000000000000000")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contract: post /api/assets: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))

	if resp.StatusCode != http.StatusBadRequest {
		return nil, fmt.Errorf("contract: expected HTTP 400 from validator, got %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var errResp struct {
		Message    any    `json:"message"`
		Error      string `json:"error"`
		StatusCode int    `json:"statusCode"`
	}
	if err := json.Unmarshal(respBody, &errResp); err != nil {
		return nil, fmt.Errorf("contract: parse 400 body: %w (body: %s)", err, strings.TrimSpace(string(respBody)))
	}

	required, err := extractFieldNames(errResp.Message)
	if err != nil {
		return nil, fmt.Errorf("contract: extract field names: %w", err)
	}

	version, _ := fetchImmichVersion(ctx, httpClient, baseURL, apiKey)

	return &ImmichManifest{
		ImmichVersion: version,
		CapturedAt:    time.Now().UTC().Format(time.RFC3339),
		Endpoint:      "POST /api/assets",
		Required:      required,
	}, nil
}

// buildMinimalUploadProbe builds a multipart body with only enough
// to get past multer's file-parse stage and into the validator.
// Specifically: an assetData file part with a 2-byte JPEG header.
// All other fields are deliberately omitted so the validator's
// required-field complaints are the response.
func buildMinimalUploadProbe() (io.Reader, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// Minimal valid JPEG header bytes; just enough that file-type
	// detection on the server doesn't reject before validation.
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="assetData"; filename="probe.jpg"`)
	hdr.Set("Content-Type", "image/jpeg")
	part, err := w.CreatePart(hdr)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}); err != nil {
		return nil, "", err
	}

	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

// extractFieldNames walks the variants the Immich error endpoint
// emits in the `message` field of a 400 response and pulls out the
// unique set of field names that triggered a validation complaint.
//
// Three formats observed:
//   - String slice: ["fieldX must be a string", "fieldX should not be empty", ...]
//     (NestJS class-validator)
//   - Single string: "fieldX is required" or similar
//   - Object: {"path": [...], "message": "..."} — zod's error shape
func extractFieldNames(message any) ([]string, error) {
	seen := map[string]bool{}
	collect := func(s string) {
		if name := fieldNameFromMessage(s); name != "" {
			seen[name] = true
		}
	}
	switch m := message.(type) {
	case []any:
		for _, entry := range m {
			switch e := entry.(type) {
			case string:
				collect(e)
			case map[string]any:
				// Zod object shape: prefer the structured path
				// over the natural-language message (which contains
				// noise like "Expected object, received string").
				// Only fall back to message-parsing when path is
				// unavailable.
				if path, ok := e["path"].([]any); ok && len(path) > 0 {
					if name, ok := path[0].(string); ok {
						seen[name] = true
						continue
					}
				}
				if msg, ok := e["message"].(string); ok {
					collect(msg)
				}
			}
		}
	case string:
		collect(m)
	default:
		return nil, fmt.Errorf("unrecognised 400 message shape: %T", message)
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// fieldNameFromMessage extracts the field name from a class-validator
// error message like "deviceAssetId must be a string". Returns empty
// string if no field name can be identified (defensive against
// unknown error formats).
func fieldNameFromMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	// class-validator format: "<fieldName> <complaint>"
	// e.g. "deviceAssetId must be a string"
	//      "deviceAssetId should not be empty"
	//      "fileCreatedAt must be a date string"
	parts := strings.Fields(msg)
	if len(parts) == 0 {
		return ""
	}
	candidate := strings.TrimRight(parts[0], ".,:")
	// Field names are camelCase identifiers. Reject anything with
	// spaces, slashes, or starting with a non-letter to avoid
	// misparsing free-form error messages.
	if candidate == "" {
		return ""
	}
	c := candidate[0]
	if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
		return ""
	}
	return candidate
}

// fetchImmichVersion reads /api/server/version for the manifest.
// Best-effort: returns "unknown" + nil on any error rather than
// failing the probe.
func fetchImmichVersion(ctx context.Context, client *http.Client, baseURL, apiKey string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/server/version", nil)
	if err != nil {
		return "unknown", err
	}
	req.Header.Set("x-api-key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return "unknown", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	var v struct {
		Major int `json:"major"`
		Minor int `json:"minor"`
		Patch int `json:"patch"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "unknown", err
	}
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch), nil
}
