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

	"gitlab.com/dunn.dev/bairn/internal/retry"
)

// fakeImmich captures the most recent upload request for assertions.
type fakeImmich struct {
	srv             *httptest.Server
	lastChecksum    string
	lastAPIKey      string
	lastFilename    string
	lastMetadata    map[string]string
	lastFileCreated string
	sawDeviceField  bool
	lastSidecar     []byte
	sidecarFilename string
	assetFilename   string

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
		f.lastFilename = ""
		unsupported := ""
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
			// Immich checks each file part's type by body.filename when
			// set, else by the part's own filename.
			name := f.lastFilename
			if name == "" {
				name = p.FileName()
			}
			switch p.FormName() {
			case "assetData":
				f.assetFilename = p.FileName()
			case "sidecarData":
				f.lastSidecar = body
				f.sidecarFilename = p.FileName()
				if !strings.HasSuffix(name, ".xmp") {
					unsupported = name
				}
			}
			switch p.FormName() {
			case "fileCreatedAt":
				f.lastFileCreated = string(body)
			case "filename":
				f.lastFilename = string(body)
			case "deviceId", "deviceAssetId":
				f.sawDeviceField = true
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

		if unsupported != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"Unsupported file type ` + unsupported + `"}`))
			return
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
	if f.assetFilename != "img-001.jpg" || f.lastFilename != "" {
		t.Errorf("assetData filename = %q, filename field = %q", f.assetFilename, f.lastFilename)
	}
	if f.sawDeviceField {
		t.Error("deviceId/deviceAssetId sent; Immich 3.x dropped them")
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

func TestUploadForbiddenIsUnauthorized(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/assets", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(srv.URL, "narrow-key")
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

func TestUploadSidecarField(t *testing.T) {
	f := newFakeImmich(t)
	c := New(f.srv.URL, "test-key")
	now := time.Date(2026, 5, 6, 14, 30, 0, 0, time.UTC)
	in := UploadInput{
		Data: []byte("fake video bytes"), Filename: "vid-001.mp4",
		FileCreatedAt: now, FileModifiedAt: now,
		Sidecar: []byte("<x:xmpmeta/>"),
	}
	if _, err := c.Upload(context.Background(), in); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if string(f.lastSidecar) != "<x:xmpmeta/>" || f.sidecarFilename == "" {
		t.Errorf("sidecarData = %q (filename %q)", f.lastSidecar, f.sidecarFilename)
	}

	f.lastSidecar = nil
	in.Sidecar = nil
	if _, err := c.Upload(context.Background(), in); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if f.lastSidecar != nil {
		t.Errorf("sidecarData sent without a sidecar: %q", f.lastSidecar)
	}
}

func fastRetry() Option {
	return WithRetry(retry.Policy{Attempts: 3, Initial: time.Millisecond, Max: 5 * time.Millisecond})
}

func uploadIn() UploadInput {
	return UploadInput{
		Data:           []byte("same bytes every attempt"),
		Filename:       "x.jpg",
		FileCreatedAt:  time.Now(),
		FileModifiedAt: time.Now(),
	}
}

// A 5xx is retried with the identical body and checksum; the server
// answering "duplicate" for an upload that did land is the success
// path of a retried POST.
func TestUploadRetriesWithIdenticalBody(t *testing.T) {
	var bodies []string
	var sums []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		sums = append(sums, r.Header.Get("x-immich-checksum"))
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"asset-001","status":"duplicate"}`))
	}))
	t.Cleanup(srv.Close)

	res, err := New(srv.URL, "k", fastRetry()).Upload(context.Background(), uploadIn())
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !res.Duplicate || res.ID != "asset-001" {
		t.Errorf("result = %+v", res)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || len(bodies[0]) == 0 || sums[0] != sums[1] {
		t.Errorf("attempts=%d, bodies equal=%v, sums=%v", len(bodies), len(bodies) == 2 && bodies[0] == bodies[1], sums)
	}
}

func TestUploadHonoursRetryAfterOn429(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"a","status":"created"}`))
	}))
	t.Cleanup(srv.Close)

	var waited []time.Duration
	p := retry.Policy{Attempts: 3, Initial: time.Millisecond, Max: 30 * time.Second,
		Sleep: func(_ context.Context, d time.Duration) error { waited = append(waited, d); return nil }}
	if _, err := New(srv.URL, "k", WithRetry(p)).Upload(context.Background(), uploadIn()); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if len(waited) != 1 || waited[0] != time.Second {
		t.Errorf("waited = %v, want [1s]", waited)
	}
}

func TestUploadClientErrorsNotRetried(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusUnprocessableEntity} {
		var n int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n++
			w.WriteHeader(code)
		}))
		_, err := New(srv.URL, "k", fastRetry()).Upload(context.Background(), uploadIn())
		srv.Close()
		if err == nil || n != 1 {
			t.Errorf("%d: err=%v attempts=%d, want an error after 1 attempt", code, err, n)
		}
	}
}

func TestUploadDoesNotFollowRedirectWithKey(t *testing.T) {
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("x-api-key") != ""
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/assets", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "secret-key", WithRetry(retry.Policy{Attempts: 1}))
	_, err := c.Upload(context.Background(), UploadInput{
		Data: []byte("x"), Filename: "x.jpg", FileCreatedAt: time.Now(), FileModifiedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("a redirect must not count as a successful upload")
	}
	if leaked {
		t.Error("API key was replayed to the redirect target")
	}
}
