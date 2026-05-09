package immich

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeImmich captures the most recent upload request for assertions.
type fakeImmich struct {
	srv               *httptest.Server
	lastChecksum      string
	lastAPIKey        string
	lastFilename      string
	lastMetadata      map[string]string
	lastFileCreated   string
	lastDeviceID      string
	lastDeviceAssetID string

	respondWith struct {
		statusCode int
		body       string
	}
}

func newFakeImmich(t *testing.T) *fakeImmich {
	t.Helper()
	f := &fakeImmich{}
	f.respondWith.statusCode = 201
	f.respondWith.body = `{"id":"asset-001","status":"created"}`

	mux := http.NewServeMux()
	mux.HandleFunc("/assets", func(w http.ResponseWriter, r *http.Request) {
		f.lastChecksum = r.Header.Get("x-immich-checksum")
		f.lastAPIKey = r.Header.Get("x-api-key")

		// Parse multipart to extract fields
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("parse media type: %v", err)
			return
		}
		if !strings.HasPrefix(mediaType, "multipart/") {
			t.Errorf("content type: %s", mediaType)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		f.lastMetadata = map[string]string{}
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("part: %v", err)
				return
			}
			body, _ := io.ReadAll(p)
			switch p.FormName() {
			case "fileCreatedAt":
				f.lastFileCreated = string(body)
			case "filename":
				f.lastFilename = string(body)
			case "deviceId":
				f.lastDeviceID = string(body)
			case "deviceAssetId":
				f.lastDeviceAssetID = string(body)
			case "metadata":
				// v2.7.5+: single field, JSON-encoded array of
				// {key, value} where value is an object wrapping
				// the string ({"value":"<str>"}).
				var items []struct {
					Key   string         `json:"key"`
					Value map[string]any `json:"value"`
				}
				_ = json.Unmarshal(body, &items)
				for _, item := range items {
					if v, ok := item.Value["value"].(string); ok {
						f.lastMetadata[item.Key] = v
					}
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.respondWith.statusCode)
		_, _ = w.Write([]byte(f.respondWith.body))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func TestUploadCreated(t *testing.T) {
	f := newFakeImmich(t)
	c := New(f.srv.URL, "test-key")

	data := []byte("fake image bytes")
	now := time.Date(2026, 5, 6, 14, 30, 0, 0, time.UTC)
	res, err := c.Upload(context.Background(), UploadInput{
		Data:           data,
		Filename:       "img-001.jpg",
		FileCreatedAt:  now,
		FileModifiedAt: now,
		DeviceID:       "bairn",
		DeviceAssetID:  "img-001",
		Metadata:       map[string]string{"famlyImageId": "img-001"},
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if res.ID != "asset-001" || res.Status != "created" || res.Duplicate {
		t.Errorf("result = %+v", res)
	}
	// Verify the server saw what we sent.
	if f.lastAPIKey != "test-key" {
		t.Errorf("api key = %q", f.lastAPIKey)
	}
	want := sha1Hex(data)
	if f.lastChecksum != want {
		t.Errorf("checksum = %q, want %q", f.lastChecksum, want)
	}
	if f.lastFilename != "img-001.jpg" {
		t.Errorf("filename = %q", f.lastFilename)
	}
	if f.lastDeviceID != "bairn" {
		t.Errorf("deviceId = %q", f.lastDeviceID)
	}
	if f.lastDeviceAssetID != "img-001" {
		t.Errorf("deviceAssetId = %q", f.lastDeviceAssetID)
	}
	if f.lastMetadata["famlyImageId"] != "img-001" {
		t.Errorf("metadata.famlyImageId = %q", f.lastMetadata["famlyImageId"])
	}
}

func TestUploadDuplicate(t *testing.T) {
	f := newFakeImmich(t)
	f.respondWith.statusCode = 200
	f.respondWith.body = `{"id":"asset-001","status":"duplicate"}`
	c := New(f.srv.URL, "test-key")

	res, err := c.Upload(context.Background(), UploadInput{
		Data:           []byte("any"),
		Filename:       "img.jpg",
		FileCreatedAt:  time.Now(),
		FileModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !res.Duplicate || res.Status != "duplicate" {
		t.Errorf("result = %+v", res)
	}
}

func TestUploadUnauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/assets", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL, "bad-key")
	_, err := c.Upload(context.Background(), UploadInput{
		Data:           []byte("x"),
		Filename:       "x.jpg",
		FileCreatedAt:  time.Now(),
		FileModifiedAt: time.Now(),
	})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

// independent SHA1 helper to assert client.go's sha1Hex matches
// the canonical encoding callers will compute.
func TestSHA1HexMatchesCanonical(t *testing.T) {
	data := []byte("hello")
	got := sha1Hex(data)
	sum := sha1.Sum(data)
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Errorf("sha1Hex = %s, want %s", got, want)
	}
}
