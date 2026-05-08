// Package immich is bairn's typed client for Immich's asset upload
// surface.
//
// The generated client in gen.go provides typed responses for all
// operations declared in the vendored OpenAPI spec. This file adds
// a thin operator-friendly wrapper for the asset-upload flow,
// including SHA1-based dedup via the x-immich-checksum header that
// modern Immich expects.
package immich

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strings"
	"time"
)

// Client is bairn's wrapper around the generated Immich client.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the default *http.Client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.logger = l } }

// New constructs a Client. baseURL is the Immich instance root
// (e.g. "https://immich.home.example/api"). apiKey is the Immich
// API key (managed under user settings; sent as x-api-key).
func New(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		logger:     slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// UploadInput is the bairn-friendly request shape for asset upload.
type UploadInput struct {
	// Data is the file body. The full byte slice is read once and
	// held for the duration of the upload (multipart needs Content-
	// Length). For very large videos a streaming variant could be
	// added; for nursery-feed assets, in-memory is fine.
	Data []byte

	// Filename preserves the original name (extension drives
	// Immich's mime detection).
	Filename string

	// FileCreatedAt and FileModifiedAt populate Immich's
	// fileCreatedAt and fileModifiedAt fields. Use the vendor's
	// reported createdAt for both unless you have a better signal.
	FileCreatedAt  time.Time
	FileModifiedAt time.Time

	// Metadata is an arbitrary key/value bag persisted with the
	// asset on the Immich side. bairn writes "famlyImageId" with
	// the vendor's image ID for traceability.
	Metadata map[string]string
}

// UploadResult is the bairn-friendly response shape.
type UploadResult struct {
	// ID is the Immich asset ID.
	ID string

	// Status is "created" for new uploads or "duplicate" when the
	// SHA1 already exists on the server. Both cases return the
	// same id (the existing asset's id when duplicate).
	Status string

	// Duplicate is true iff Status == "duplicate".
	Duplicate bool
}

// ErrUnauthorized is returned on 401. The operator should check
// IMMICH_API_KEY and the configured base URL.
var ErrUnauthorized = errors.New("immich: unauthorized; check IMMICH_API_KEY and IMMICH_BASE_URL")

// Upload posts an asset to Immich. The Content-Length-bearing
// multipart body is constructed in memory; the SHA1 of the file
// data is sent as x-immich-checksum so the server can return
// "duplicate" status when the hash already exists.
func (c *Client) Upload(ctx context.Context, in UploadInput) (*UploadResult, error) {
	body, contentType, err := buildUploadBody(in)
	if err != nil {
		return nil, fmt.Errorf("immich: build upload body: %w", err)
	}

	checksum := sha1Hex(in.Data)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/assets", body)
	if err != nil {
		return nil, fmt.Errorf("immich: build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("x-immich-checksum", checksum)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("immich: post /assets: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("immich: post /assets: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}

	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("immich: decode response: %w", err)
	}
	return &UploadResult{
		ID:        out.ID,
		Status:    out.Status,
		Duplicate: out.Status == "duplicate",
	}, nil
}

// buildUploadBody assembles the multipart payload Immich expects.
//
// Wire shape matches AssetMediaCreateDto in api/immich/openapi.json
// (vendored from immich-app/immich main; refresh via
// `make refresh-immich-spec`). Required fields: assetData,
// fileCreatedAt, fileModifiedAt. Metadata is one JSON-array field
// of {key, value:object} items per AssetMetadataUpsertItemDto.
//
// Wire format targets Immich >= v2.7.5 (post-zod-migration; upstream
// PR immich-app/immich#26597, April 2026). Older Immich versions
// were tolerant of looser shapes (per-entry metadata fields, string
// values without object wrap) under class-validator; the migration
// to zod hardened validation around the spec already published.
func buildUploadBody(in UploadInput) (io.Reader, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// Required time fields. Both must be present.
	if err := w.WriteField("fileCreatedAt", in.FileCreatedAt.UTC().Format(time.RFC3339)); err != nil {
		return nil, "", err
	}
	if err := w.WriteField("fileModifiedAt", in.FileModifiedAt.UTC().Format(time.RFC3339)); err != nil {
		return nil, "", err
	}

	// Optional filename.
	if in.Filename != "" {
		if err := w.WriteField("filename", in.Filename); err != nil {
			return nil, "", err
		}
	}

	// Metadata: a single field whose value is a JSON-encoded array
	// of {key, value} objects. Each `value` is an object (string
	// values get wrapped as {"value": "<string>"}) per the v2.7.5
	// AssetMetadataUpsertItemDto.value `type: object` constraint.
	if len(in.Metadata) > 0 {
		items := make([]map[string]any, 0, len(in.Metadata))
		for k, v := range in.Metadata {
			items = append(items, map[string]any{
				"key":   k,
				"value": map[string]any{"value": v},
			})
		}
		encoded, err := json.Marshal(items)
		if err != nil {
			return nil, "", err
		}
		if err := w.WriteField("metadata", string(encoded)); err != nil {
			return nil, "", err
		}
	}

	// File payload. Use a custom MIME header so we can set both
	// the form field name and the filename, which Immich requires.
	hdr := textproto.MIMEHeader{}
	fname := in.Filename
	if fname == "" {
		fname = "asset" + filepath.Ext(detectExt(in.Data))
	}
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="assetData"; filename=%q`, fname))
	hdr.Set("Content-Type", detectMime(fname, in.Data))
	part, err := w.CreatePart(hdr)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(in.Data); err != nil {
		return nil, "", err
	}

	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

func sha1Hex(b []byte) string {
	sum := sha1.Sum(b)
	return hex.EncodeToString(sum[:])
}

// detectExt returns ".jpg" for unknown content. Used only when no
// filename is supplied; downstream callers should always set
// in.Filename for production.
func detectExt(b []byte) string {
	switch http.DetectContentType(b) {
	case "image/jpeg":
		return "image/jpeg"
	case "image/png":
		return "image/png"
	case "video/mp4":
		return "video/mp4"
	}
	return "application/octet-stream"
}

// detectMime returns the most likely mime type, preferring the
// extension and falling back to content sniffing.
func detectMime(filename string, data []byte) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".heic":
		return "image/heic"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	}
	return http.DetectContentType(data)
}
