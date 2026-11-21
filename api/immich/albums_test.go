package immich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestCreateAlbumSharesAsEditorAndIsNotRetried(t *testing.T) {
	var body map[string]any
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/albums" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if calls == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"id":"new-1"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "k", fastRetry())
	if _, err := c.CreateAlbum(context.Background(), "A", []string{"u-1"}); err == nil {
		t.Fatal("want error on 500")
	}
	if calls != 1 {
		t.Errorf("calls after failure = %d, want 1 (no retry on create)", calls)
	}
	id, err := c.CreateAlbum(context.Background(), "A", []string{"u-1", "u-2"})
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
