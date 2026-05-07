package famly

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fixture loads a fixture file from testdata. Tests use this to
// avoid embedding large JSON literals inline.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("load fixture %s: %v", name, err)
	}
	return b
}

// fakeFamly returns a httptest.Server that serves the given path
// → fixture map. Returns 404 for unknown paths and 401 if no token
// header is set, so tests can exercise both happy and failure paths.
func fakeFamly(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, fname := range routes {
		body := fixture(t, fname)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(authHeader) == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		})
	}
	return httptest.NewServer(mux)
}

func TestMe(t *testing.T) {
	srv := fakeFamly(t, map[string]string{"/api/me/me/me": "me.json"})
	t.Cleanup(srv.Close)

	c := New(NewStaticToken("test-token"), WithBaseURL(srv.URL))
	me, err := c.Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if me.LoginID != "00000000-0000-0000-0000-000000000001" {
		t.Errorf("LoginID = %q", me.LoginID)
	}
	if got, want := len(me.Roles2), 2; got != want {
		t.Errorf("len(Roles2) = %d, want %d", got, want)
	}
	if me.Roles2[0].TargetID == "" {
		t.Errorf("Roles2[0].TargetID empty")
	}
}

func TestRelations(t *testing.T) {
	srv := fakeFamly(t, map[string]string{"/api/v2/relations": "relations.json"})
	t.Cleanup(srv.Close)

	c := New(NewStaticToken("test-token"), WithBaseURL(srv.URL))
	rels, err := c.Relations(context.Background(), "00000000-0000-0000-0000-00000000c001")
	if err != nil {
		t.Fatalf("Relations: %v", err)
	}
	if got, want := len(rels), 2; got != want {
		t.Fatalf("len = %d, want %d", got, want)
	}
	for i, r := range rels {
		if r.LoginID == "" {
			t.Errorf("rels[%d].LoginID empty", i)
		}
	}
}

func TestUnauthorizedInvalidatesToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/me/me/me", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tok := NewStaticToken("test-token")
	c := New(tok, WithBaseURL(srv.URL))
	_, err := c.Me(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	// Static token by itself doesn't auto-invalidate; the caller
	// (or higher-level retry helper) is responsible for that. But
	// if we explicitly invalidate, the next Token call should fail
	// with the prescriptive error.
	tok.Invalidate()
	_, err = tok.Token(context.Background())
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expected ErrTokenExpired after Invalidate, got %v", err)
	}
}

func TestRefreshingTokenRefreshes(t *testing.T) {
	calls := 0
	src := NewRefreshingToken("alice@example.com", "pw", "device-1",
		func(ctx context.Context, email, password, deviceID string) (string, error) {
			calls++
			return "fresh-token", nil
		})

	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	if tok != "fresh-token" {
		t.Errorf("first Token = %q", tok)
	}
	if calls != 1 {
		t.Errorf("login calls after first = %d, want 1", calls)
	}

	tok2, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if tok2 != "fresh-token" {
		t.Errorf("second Token = %q", tok2)
	}
	if calls != 1 {
		t.Errorf("login calls after second = %d, want 1 (cached)", calls)
	}

	src.Invalidate()
	_, err = src.Token(context.Background())
	if err != nil {
		t.Fatalf("post-invalidate Token: %v", err)
	}
	if calls != 2 {
		t.Errorf("login calls after invalidate = %d, want 2", calls)
	}
}
