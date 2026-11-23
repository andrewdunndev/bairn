package immich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

type known struct {
	id      string
	trashed bool
}

// checkFake enforces the v3.4.2 request shape (POST, JSON, x-api-key,
// {"assets":[{"id","checksum"}]} with no other fields, a 40-char hex
// checksum) and answers from a checksum table, replying in reverse
// order. Anything malformed is a 400.
func checkFake(t *testing.T, table map[string]known, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		bad := func(msg string) { http.Error(w, `{"message":"`+msg+`"}`, http.StatusBadRequest) }
		if r.Method != http.MethodPost || r.URL.Path != "/assets/bulk-upload-check" {
			bad("route")
			return
		}
		if r.Header.Get("x-api-key") != "k" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			bad("headers")
			return
		}
		var in struct {
			Assets []struct {
				ID       string `json:"id"`
				Checksum string `json:"checksum"`
			} `json:"assets"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || len(in.Assets) == 0 {
			bad("body")
			return
		}
		type res struct {
			ID        string `json:"id"`
			Action    string `json:"action"`
			Reason    string `json:"reason,omitempty"`
			AssetID   string `json:"assetId,omitempty"`
			IsTrashed *bool  `json:"isTrashed,omitempty"`
		}
		var out []res
		for _, a := range in.Assets {
			if a.ID == "" || !hex40.MatchString(a.Checksum) {
				bad("item")
				return
			}
			if k, ok := table[a.Checksum]; ok {
				tr := k.trashed
				out = append([]res{{a.ID, "reject", "duplicate", k.id, &tr}}, out...)
			} else {
				out = append([]res{{ID: a.ID, Action: "accept"}}, out...)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": out})
	}))
}

func sum(n int) string { return fmt.Sprintf("%040x", n) }

func TestBulkUploadCheckMapsAcceptAndRejectAndSendsHexAsIs(t *testing.T) {
	var calls atomic.Int32
	srv := checkFake(t, map[string]known{sum(1): {"A", false}, sum(2): {"B", true}}, &calls)
	defer srv.Close()
	got, err := New(srv.URL, "k", fastRetry()).BulkUploadCheck(context.Background(), []CheckItem{
		{"s1", sum(1)}, {"s2", sum(2)}, {"s3", sum(3)},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]CheckResult{
		"s1": {ID: "s1", Duplicate: true, AssetID: "A"},
		"s2": {ID: "s2", Duplicate: true, AssetID: "B", IsTrashed: true},
		"s3": {ID: "s3"},
	}
	if len(got) != 3 {
		t.Fatalf("got %d results", len(got))
	}
	for _, r := range got {
		if r != want[r.ID] {
			t.Errorf("%s: got %+v want %+v", r.ID, r, want[r.ID])
		}
	}
}

func TestBulkUploadCheckRefusesOversizedAndBadItemsBeforeSending(t *testing.T) {
	var calls atomic.Int32
	srv := checkFake(t, nil, &calls)
	defer srv.Close()
	c := New(srv.URL, "k", fastRetry())
	big := make([]CheckItem, MaxBulkCheck+1)
	for i := range big {
		big[i] = CheckItem{fmt.Sprint(i), sum(i)}
	}
	for name, items := range map[string][]CheckItem{
		"oversized": big,
		"empty id":  {{"", sum(1)}},
		"empty sum": {{"a", ""}},
		"dup id":    {{"a", sum(1)}, {"a", sum(2)}},
	} {
		if _, err := c.BulkUploadCheck(context.Background(), items); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("%d requests sent", calls.Load())
	}
	if _, err := c.BulkUploadCheck(context.Background(), big[:MaxBulkCheck]); err != nil {
		t.Errorf("a full batch: %v", err)
	}
}

func TestBulkUploadCheckErrors(t *testing.T) {
	cases := map[string]struct {
		h    http.HandlerFunc
		want error
	}{
		"401":        {func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }, ErrUnauthorized},
		"403":        {func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }, ErrUnauthorized},
		"5xx":        {func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }, nil},
		"not json":   {func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }, nil},
		"no results": {func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }, nil},
		"dropped id": {func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"results":[{"id":"a","action":"accept"}]}`))
		}, nil},
		"foreign id": {func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"results":[{"id":"a","action":"accept"},{"id":"zzz","action":"accept"}]}`))
		}, nil},
		"repeated id": {func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"results":[{"id":"a","action":"accept"},{"id":"a","action":"accept"}]}`))
		}, nil},
		"reject without asset": {func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"results":[{"id":"a","action":"reject","reason":"duplicate"},{"id":"b","action":"accept"}]}`))
		}, nil},
		"odd action": {func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"results":[{"id":"a","action":"maybe"},{"id":"b","action":"accept"}]}`))
		}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(tc.h)
			defer srv.Close()
			res, err := New(srv.URL, "SECRET-KEY", fastRetry()).BulkUploadCheck(context.Background(), []CheckItem{{"a", sum(1)}, {"b", sum(2)}})
			if err == nil || res != nil {
				t.Fatalf("res=%v err=%v, want an error and no results", res, err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "SECRET-KEY") {
				t.Error("error leaks the api key")
			}
		})
	}
}
