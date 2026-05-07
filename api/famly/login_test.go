package famly

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginSuccess(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "auth-success.json"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tok, err := Login(context.Background(), srv.URL, "alice@example.com", "pw", "device-1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if tok != "fresh-access-token-from-login" {
		t.Errorf("token = %q", tok)
	}
}

func TestLoginFailedInvalidPassword(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "auth-failed.json"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, err := Login(context.Background(), srv.URL, "alice@example.com", "wrong", "device-1")
	if err == nil {
		t.Fatal("Login: want error, got nil")
	}
	if !strings.Contains(err.Error(), "FailedInvalidPassword") {
		t.Errorf("error should mention FailedInvalidPassword status: %v", err)
	}
}

func TestRefreshingTokenFromCredentialsRefreshes(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "auth-success.json"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	src := NewRefreshingTokenFromCredentials(srv.URL, "alice@example.com", "pw", "device-1")
	t1, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	if t1 != "fresh-access-token-from-login" {
		t.Errorf("token = %q", t1)
	}
	if calls != 1 {
		t.Errorf("login calls = %d, want 1", calls)
	}

	// Cached: no further GraphQL calls.
	_, _ = src.Token(context.Background())
	if calls != 1 {
		t.Errorf("login calls (cached) = %d, want 1", calls)
	}

	// After invalidate, refresh runs again.
	src.Invalidate()
	_, _ = src.Token(context.Background())
	if calls != 2 {
		t.Errorf("login calls (refreshed) = %d, want 2", calls)
	}
}
