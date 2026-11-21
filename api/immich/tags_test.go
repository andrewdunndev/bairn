package immich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpsertTagSendsOneIdempotentPutAndReturnsTheID(t *testing.T) {
	var got struct{ Tags []string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/tags" {
			t.Errorf("%s %s, want PUT /tags", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "k" {
			t.Error("missing api key")
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`[{"id":"other","name":"x","value":"x"},{"id":"tag-1","name":"bairn","value":"bairn"}]`))
	}))
	defer srv.Close()
	id, err := New(srv.URL, "k", fastRetry()).UpsertTag(context.Background(), "bairn")
	if err != nil || id != "tag-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "bairn" {
		t.Errorf("body tags = %v", got.Tags)
	}
}

func TestUpsertTagRefusesAnAnswerWithoutTheTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) }))
	defer srv.Close()
	if _, err := New(srv.URL, "k", fastRetry()).UpsertTag(context.Background(), "bairn"); err == nil {
		t.Fatal("want an error")
	}
}

func TestUpsertTagMapsAKeyWithoutScopeToUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Missing required permission: tag.create"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := New(srv.URL, "k", fastRetry()).UpsertTag(context.Background(), "bairn")
	if err != ErrUnauthorized {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestTagAssetsRefusalsAndDuplicates(t *testing.T) {
	var path string
	var body struct{ IDs []string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`[{"id":"a","success":true},{"id":"b","success":false,"error":"duplicate"},{"id":"c","success":false,"error":"no_permission"},{"id":"d","success":false,"error":"not_found"}]`))
	}))
	defer srv.Close()
	refused, err := New(srv.URL, "k", fastRetry()).TagAssets(context.Background(), "tag-1", []string{"a", "b", "c", "d"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "PUT /tags/tag-1/assets" || len(body.IDs) != 4 {
		t.Errorf("request %q ids=%v", path, body.IDs)
	}
	if len(refused) != 2 || refused["c"] != "no_permission" || refused["d"] != "not_found" {
		t.Errorf("refused = %v, want c and d only (a duplicate counts as tagged)", refused)
	}
}

func TestTagAssetsTreatsAnUnknownReasonAsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"a","success":false,"error":"unknown"}]`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "k", fastRetry()).TagAssets(context.Background(), "t", []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "tag assets") {
		t.Errorf("err = %v, want a retryable batch error", err)
	}
	if r, err := New(srv.URL, "k", fastRetry()).TagAssets(context.Background(), "t", nil); r != nil || err != nil {
		t.Error("an empty batch must make no call")
	}
}
