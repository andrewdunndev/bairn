package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ImmichSessionJSON sends one JSON request authenticated with the
// login session token (never an API key) and decodes a 2xx response
// into out when out is non-nil. The smoke uses it for what the
// narrow API key must not do: resolving users, reading albums back,
// and cleaning up.
func ImmichSessionJSON(ctx context.Context, client *http.Client, baseURL, token, method, path string, in, out any) error {
	if client == nil {
		client = defaultClient()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("contract: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("contract: %s %s HTTP %d: %s", method, path, resp.StatusCode, msg)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("contract: parse %s %s: %w", method, path, err)
	}
	return nil
}
