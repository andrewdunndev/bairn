package immich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestFindOwnedAlbumQueriesOwnedExactName(t *testing.T) {
	var q map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		if r.Header.Get("x-api-key") != "k" {
			t.Errorf("missing api key")
		}
		_, _ = w.Write([]byte(`[{"id":"other","albumName":"X 2026 b"},{"id":"a-1","albumName":"X 2026"}]`))
	}))
	defer srv.Close()
	id, err := New(srv.URL, "k", fastRetry()).FindOwnedAlbum(context.Background(), "X 2026")
	if err != nil {
		t.Fatal(err)
	}
	if id != "a-1" {
		t.Errorf("id = %q, want a-1 (exact name only)", id)
	}
	if q["isOwned"][0] != "true" || q["name"][0] != "X 2026" {
		t.Errorf("query = %v", q)
	}
}

func TestFindOwnedAlbumNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) }))
	defer srv.Close()
	id, err := New(srv.URL, "k", fastRetry()).FindOwnedAlbum(context.Background(), "X")
	if err != nil || id != "" {
		t.Errorf("id=%q err=%v, want empty and nil", id, err)
	}
}

func TestCreateAlbumSharesAsEditor(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/albums" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"id":"new-1"}`))
	}))
	defer srv.Close()
	id, err := New(srv.URL, "k", fastRetry()).CreateAlbum(context.Background(), "A", []string{"u-1", "u-2"})
	if err != nil || id != "new-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	users := body["albumUsers"].([]any)
	if len(users) != 2 || users[0].(map[string]any)["role"] != "editor" || users[1].(map[string]any)["userId"] != "u-2" {
		t.Errorf("albumUsers = %v", users)
	}
	if body["albumName"] != "A" {
		t.Errorf("albumName = %v", body["albumName"])
	}
}

// fakeImmichAlbums is a server whose POST /albums creates the album and
// then drops the response (lost) for the first lose posts.
type fakeImmichAlbums struct {
	mu     sync.Mutex
	albums []string
	posts  int
	lose   int
	status int // when non-zero, POST answers this status without creating
}

func (f *fakeImmichAlbums) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/albums":
		out := []map[string]string{}
		for i, n := range f.albums {
			if n == r.URL.Query().Get("name") {
				out = append(out, map[string]string{"id": fmt.Sprintf("al-%d", i), "albumName": n})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && r.URL.Path == "/albums":
		f.posts++
		if f.status != 0 {
			http.Error(w, "no", f.status)
			return
		}
		var in struct{ AlbumName string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.albums = append(f.albums, in.AlbumName)
		if f.posts <= f.lose {
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":"al-%d"}`, len(f.albums)-1)
	default:
		http.NotFound(w, r)
	}
}

func TestCreateAlbumLostResponseMakesExactlyOneAlbum(t *testing.T) {
	f := &fakeImmichAlbums{lose: 1}
	srv := httptest.NewServer(f)
	defer srv.Close()
	id, err := New(srv.URL, "k", fastRetry()).CreateAlbum(context.Background(), "A", []string{"u-1"})
	if err != nil || id != "al-0" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if len(f.albums) != 1 || f.posts != 1 {
		t.Errorf("albums=%d posts=%d, want 1 and 1 (found, not recreated)", len(f.albums), f.posts)
	}
}

func TestCreateAlbumRetriesWhenStillAbsent(t *testing.T) {
	f := &fakeImmichAlbums{status: http.StatusInternalServerError}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := New(srv.URL, "k", fastRetry())
	if _, err := c.CreateAlbum(context.Background(), "A", nil); err == nil {
		t.Fatal("want error when every attempt fails")
	}
	if f.posts != createAttempts || len(f.albums) != 0 {
		t.Errorf("posts=%d albums=%d, want %d posts and none created", f.posts, len(f.albums), createAttempts)
	}
}

func TestCreateAlbumClientErrorIsFinal(t *testing.T) {
	f := &fakeImmichAlbums{status: http.StatusBadRequest}
	srv := httptest.NewServer(f)
	defer srv.Close()
	if _, err := New(srv.URL, "k", fastRetry()).CreateAlbum(context.Background(), "A", nil); err == nil {
		t.Fatal("want error")
	}
	if f.posts != 1 {
		t.Errorf("posts = %d, want 1 (4xx is not retried)", f.posts)
	}
}

func TestAddAssetsToAlbumTreatsDuplicateAsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/albums/al-1/assets" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		var in struct{ IDs []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if len(in.IDs) != 2 {
			t.Errorf("ids = %v", in.IDs)
		}
		_, _ = w.Write([]byte(`[{"id":"a","success":true},{"id":"b","success":false,"error":"duplicate"}]`))
	}))
	defer srv.Close()
	refused, err := New(srv.URL, "k", fastRetry()).AddAssetsToAlbum(context.Background(), "al-1", []string{"a", "b"})
	if err != nil || len(refused) != 0 {
		t.Errorf("duplicate must count as added: refused=%v err=%v", refused, err)
	}
}

func TestAddAssetsToAlbumReturnsRefusalsPerAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"a","success":true},{"id":"b","success":false,"error":"no_permission"},{"id":"c","success":false,"error":"duplicate"}]`))
	}))
	defer srv.Close()
	refused, err := New(srv.URL, "k", fastRetry()).AddAssetsToAlbum(context.Background(), "al-1", []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("per-item refusal must not fail the call: %v", err)
	}
	if len(refused) != 1 || refused["b"] != "no_permission" {
		t.Errorf("refused = %v, want only b: no_permission", refused)
	}
}

func TestAddAssetsToAlbumUnknownReasonIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"a","success":true},{"id":"b","success":false,"error":"unknown"},{"id":"c","success":false,"error":"no_permission"}]`))
	}))
	defer srv.Close()
	refused, err := New(srv.URL, "k", fastRetry()).AddAssetsToAlbum(context.Background(), "al-1", []string{"a", "b", "c"})
	if err == nil {
		t.Fatalf("unknown reason must be a retryable error, got refused=%v", refused)
	}
	if refused != nil {
		t.Errorf("nothing may be marked refused on a retryable batch: %v", refused)
	}
}

func TestAddAssetsToAlbumWholeCallFailureIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	defer srv.Close()
	if _, err := New(srv.URL, "k", fastRetry()).AddAssetsToAlbum(context.Background(), "al-1", []string{"a"}); err == nil {
		t.Error("HTTP 400 must be an error")
	}
}

func TestAlbumCallsForbiddenIsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer srv.Close()
	_, err := New(srv.URL, "k", fastRetry()).FindOwnedAlbum(context.Background(), "X")
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}
