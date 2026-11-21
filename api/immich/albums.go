package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gitlab.com/dunn.dev/bairn/internal/retry"
)

// The album surface bairn uses, with the API-key permission each call
// needs at the release pinned as IMMICH_VERSION:
//
//	GET  /albums?isOwned=true&name=   album.read
//	POST /albums                      album.create
//	PUT  /albums/{id}/assets          albumAsset.create
//
// bairn never removes an asset from an album and never deletes an
// album; no method here can.

// AlbumRoleEditor is the role bairn grants the users it shares a new
// album with.
const AlbumRoleEditor = "editor"

// FindOwnedAlbum returns the ID of the album the key's owner owns whose
// name is exactly name, or "" when there is none. The server filters by
// exact name and ownership, so no user lookup is needed. When several
// match (Immich allows duplicate names), the first is returned.
func (c *Client) FindOwnedAlbum(ctx context.Context, name string) (string, error) {
	q := url.Values{"isOwned": {"true"}, "name": {name}}
	var albums []struct {
		ID        string `json:"id"`
		AlbumName string `json:"albumName"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/albums?"+q.Encode(), nil, &albums, true); err != nil {
		return "", err
	}
	for _, a := range albums {
		if a.AlbumName == name {
			return a.ID, nil
		}
	}
	return "", nil
}

// createAttempts bounds how often CreateAlbum posts.
const createAttempts = 3

// CreateAlbum creates an album named name, shared with userIDs as
// editors, and returns its ID.
//
// The POST itself is never blindly repeated: a lost response may hide
// a create that landed, and a second POST would make an unshared twin.
// After a transport error or a 5xx the album is looked up again by
// exact owned name; it is created again only while still absent, for
// at most createAttempts posts. A 4xx is final and returned at once.
func (c *Client) CreateAlbum(ctx context.Context, name string, userIDs []string) (string, error) {
	type albumUser struct {
		UserID string `json:"userId"`
		Role   string `json:"role"`
	}
	users := make([]albumUser, 0, len(userIDs))
	for _, id := range userIDs {
		users = append(users, albumUser{UserID: id, Role: AlbumRoleEditor})
	}
	in := struct {
		AlbumName  string      `json:"albumName"`
		AlbumUsers []albumUser `json:"albumUsers,omitempty"`
	}{name, users}

	wait := c.retry.Initial
	var lastErr error
	for attempt := 1; attempt <= createAttempts; attempt++ {
		var out struct {
			ID string `json:"id"`
		}
		err := c.doJSON(ctx, http.MethodPost, "/albums", in, &out, false)
		if err == nil {
			if out.ID == "" {
				return "", fmt.Errorf("immich: create album: response carried no id")
			}
			return out.ID, nil
		}
		if !ambiguousCreate(ctx, err) {
			return "", err
		}
		lastErr = err
		c.logger.Warn("immich: create album did not confirm; checking whether it landed", "attempt", attempt, "err", err)
		sleep := c.retry.Sleep
		if sleep == nil {
			sleep = retry.Sleep
		}
		if err := sleep(ctx, wait); err != nil {
			return "", err
		}
		wait *= 2
		id, ferr := c.FindOwnedAlbum(ctx, name)
		if ferr != nil {
			return "", fmt.Errorf("immich: create album %w; lookup before retry also failed: %v", lastErr, ferr)
		}
		if id != "" {
			return id, nil
		}
	}
	return "", lastErr
}

// ambiguousCreate reports whether err leaves it unknown whether the
// album was created: a transport error, an undecodable reply or a 5xx.
func ambiguousCreate(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, ErrUnauthorized) {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.Status >= 500
	}
	return true
}

// AddAssetsToAlbum adds assets to an album and returns the assets
// Immich refused, mapped to its per-item reason (for example
// "no_permission" for an asset deleted or trashed since upload). An
// asset already in the album is reported as "duplicate" and counts as
// added. Only "no_permission" and "not_found" are final; any other
// reason (for example "unknown") may be transient and is returned as an
// error so the whole batch is retried (assets already added then read
// as duplicates). The error is non-nil when the call failed, so a retry
// is meaningful.
func (c *Client) AddAssetsToAlbum(ctx context.Context, albumID string, assetIDs []string) (map[string]string, error) {
	if len(assetIDs) == 0 {
		return nil, nil
	}
	in := struct {
		IDs []string `json:"ids"`
	}{assetIDs}
	var out []bulkResult
	if err := c.doJSON(ctx, http.MethodPut, "/albums/"+url.PathEscape(albumID)+"/assets", in, &out, true); err != nil {
		return nil, err
	}
	return refusals(out, "add assets to album")
}

// bulkResult is one entry of a BulkIdResponseDto list.
type bulkResult struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// refusals sorts a bulk answer: the final refusals by asset, or an
// error when any entry failed for a reason that may be transient.
func refusals(out []bulkResult, what string) (map[string]string, error) {
	var refused map[string]string
	var transient []string
	for _, r := range out {
		switch r.Error {
		case "", "duplicate":
		case "no_permission", "not_found":
			if refused == nil {
				refused = map[string]string{}
			}
			refused[r.ID] = r.Error
		default:
			transient = append(transient, r.Error)
		}
	}
	if len(transient) > 0 {
		return nil, fmt.Errorf("immich: %s: %d assets failed with a non-final reason (%s)", what, len(transient), transient[0])
	}
	return refused, nil
}

// StatusError is a non-2xx answer from Immich.
type StatusError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("immich: %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// doJSON sends one JSON request and decodes a 2xx JSON response into
// out. With retry, the shared policy applies; without, one attempt.
func (c *Client) doJSON(ctx context.Context, method, path string, in, out any, retry bool) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return fmt.Errorf("immich: encode %s %s: %w", method, path, err)
		}
	}
	send := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("immich: build request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("x-api-key", c.apiKey)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return c.httpClient.Do(req)
	}
	var resp *http.Response
	var err error
	if retry {
		resp, err = c.retry.Do(ctx, send)
	} else {
		resp, err = send()
	}
	if err != nil {
		return fmt.Errorf("immich: %s %s: %w", method, strings.SplitN(path, "?", 2)[0], err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &StatusError{Method: method, Path: strings.SplitN(path, "?", 2)[0], Status: resp.StatusCode, Body: strings.TrimSpace(string(buf))}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("immich: decode %s response: %w", method, err)
	}
	return nil
}
