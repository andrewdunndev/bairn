package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gitlab.com/dunn.dev/bairn/internal/config"
)

func runDriftAgainst(t *testing.T, h http.HandlerFunc) int {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	manifest := filepath.Join(dir, "m.toml")
	body := "base_url = \"" + srv.URL + "\"\nauth_header = \"x-token\"\nauth_env = \"BAIRN_TEST_DRIFT_TOKEN\"\n" +
		"[[endpoint]]\nid = \"ping\"\npath = \"/ping\"\n"
	if err := os.WriteFile(manifest, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BAIRN_TEST_DRIFT_TOKEN", "fake")
	return runDrift(context.Background(), &config.Config{}, slog.New(slog.DiscardHandler),
		[]string{"--manifest", manifest, "--out-dir", filepath.Join(dir, "out"), "--anonymize"})
}

func TestRunDriftHTMLErrorPageIsTransportFailure(t *testing.T) {
	code := runDriftAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestRunDriftNon2xxJSONIsTransportFailure(t *testing.T) {
	code := runDriftAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"x"}`))
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestRunDriftOKExitsZero(t *testing.T) {
	code := runDriftAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"a":1}`))
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
}
