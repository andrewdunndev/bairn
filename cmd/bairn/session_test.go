package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/session"
)

const minted = "minted-token-for-test"

func fakeFamlyLogin(t *testing.T, logins *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"me":{"authenticateWithPassword":{"__typename":"AuthenticationSucceeded","status":"Succeeded","accessToken":"`+minted+`","deviceId":"d-1"}}}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sessCfg(t *testing.T, base string) *config.Config {
	return &config.Config{
		FamlyEmail: "a@example.com", FamlyPassword: "pw", FamlyBaseURL: base,
		FamlyDeviceID: "d-1", SessionPath: filepath.Join(t.TempDir(), "state", session.FileName),
	}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestSessionFirstRunLogsInAndPersistsThenReuses(t *testing.T) {
	var logins atomic.Int32
	cfg := sessCfg(t, fakeFamlyLogin(t, &logins).URL)
	tok, err := sessionTokenSource(cfg, false, quiet).Token(context.Background())
	if err != nil || tok != minted || logins.Load() != 1 {
		t.Fatalf("first run: tok=%q err=%v logins=%d", tok, err, logins.Load())
	}
	got, _ := session.Open(cfg.SessionPath, true).Load()
	if got.AccessToken != minted {
		t.Fatalf("session not persisted: %+v", got)
	}
	fi, _ := os.Stat(cfg.SessionPath)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	// A second run reuses the cached token: no password login.
	tok, err = sessionTokenSource(cfg, false, quiet).Token(context.Background())
	if err != nil || tok != minted || logins.Load() != 1 {
		t.Fatalf("second run: tok=%q err=%v logins=%d, want reuse", tok, err, logins.Load())
	}
}

func TestSessionStaleTokenReloginPersistsNew(t *testing.T) {
	var logins atomic.Int32
	cfg := sessCfg(t, fakeFamlyLogin(t, &logins).URL)
	if err := session.Open(cfg.SessionPath, false).Save(session.Session{AccessToken: "stale"}); err != nil {
		t.Fatal(err)
	}
	src := sessionTokenSource(cfg, false, quiet)
	if tok, _ := src.Token(context.Background()); tok != "stale" {
		t.Fatalf("not seeded from file: %q", tok)
	}
	src.Invalidate() // what Client does on a 401
	if tok, err := src.Token(context.Background()); err != nil || tok != minted {
		t.Fatalf("relogin: %q %v", tok, err)
	}
	got, _ := session.Open(cfg.SessionPath, true).Load()
	if got.AccessToken != minted {
		t.Errorf("new token not persisted: %+v", got)
	}
}

func TestSessionEnvTokenWinsAndIsNeverWritten(t *testing.T) {
	var logins atomic.Int32
	cfg := sessCfg(t, fakeFamlyLogin(t, &logins).URL)
	cfg.FamlyAccessToken = "env-token"
	if err := session.Open(cfg.SessionPath, false).Save(session.Session{AccessToken: "file-token"}); err != nil {
		t.Fatal(err)
	}
	if tok, _ := sessionTokenSource(cfg, false, quiet).Token(context.Background()); tok != "env-token" {
		t.Fatalf("token = %q, want the env token", tok)
	}
	b, _ := os.ReadFile(cfg.SessionPath)
	if strings.Contains(string(b), "env-token") {
		t.Error("env token written to the session file")
	}
}

func TestSessionReadOnlyReusesButNeverWrites(t *testing.T) {
	var logins atomic.Int32
	cfg := sessCfg(t, fakeFamlyLogin(t, &logins).URL)
	if tok, err := sessionTokenSource(cfg, true, quiet).Token(context.Background()); err != nil || tok != minted {
		t.Fatalf("%q %v", tok, err)
	}
	if _, err := os.Stat(cfg.SessionPath); err == nil {
		t.Error("read-only run wrote the session file")
	}
}

func TestSessionUnwritablePathDoesNotFailRun(t *testing.T) {
	var logins atomic.Int32
	cfg := sessCfg(t, fakeFamlyLogin(t, &logins).URL)
	blocker := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(blocker, nil, 0o600)
	cfg.SessionPath = filepath.Join(blocker, "sub", session.FileName) // parent is a file
	if tok, err := sessionTokenSource(cfg, false, quiet).Token(context.Background()); err != nil || tok != minted {
		t.Fatalf("%q %v", tok, err)
	}
}
