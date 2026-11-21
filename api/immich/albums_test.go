package immich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if err := New(srv.URL, "k", fastRetry()).AddAssetsToAlbum(context.Background(), "al-1", []string{"a", "b"}); err != nil {
		t.Errorf("duplicate must not be an error: %v", err)
	}
}

func TestAddAssetsToAlbumReportsOtherRefusals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"a","success":false,"error":"no_permission"}]`))
	}))
	defer srv.Close()
	err := New(srv.URL, "k", fastRetry()).AddAssetsToAlbum(context.Background(), "al-1", []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "no_permission") {
		t.Errorf("err = %v", err)
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
