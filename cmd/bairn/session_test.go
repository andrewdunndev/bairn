package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/session"
)

const (
	pinnedID = "11111111-1111-4111-8111-111111111111"
	fileID   = "22222222-2222-4222-8222-222222222222"
)

const minted = "minted-token-for-test"

// sentDevice records the deviceId of the last Authenticate request.
var sentDevice atomic.Value

// replyDevice is the deviceId the fake Famly answers with.
var replyDevice atomic.Value

func fakeFamlyLogin(t *testing.T, logins *atomic.Int32) *httptest.Server {
	t.Helper()
	sentDevice.Store("")
	replyDevice.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		var req struct {
			Variables struct {
				DeviceID string `json:"deviceId"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sentDevice.Store(req.Variables.DeviceID)
		dev := replyDevice.Load().(string)
		if dev == "" {
			dev = req.Variables.DeviceID
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"me":{"authenticateWithPassword":{"__typename":"AuthenticationSucceeded","status":"Succeeded","accessToken":"`+minted+`","deviceId":"`+dev+`"}}}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sessCfg(t *testing.T, base string) *config.Config {
	return &config.Config{
		FamlyEmail: "a@example.com", FamlyPassword: "pw", FamlyBaseURL: base,
		FamlyDeviceID: pinnedID, SessionPath: filepath.Join(t.TempDir(), "state", session.FileName),
	}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestSessionFirstRunLogsInAndPersistsThenReuses(t *testing.T) {
	var logins atomic.Int32
	cfg := sessCfg(t, fakeFamlyLogin(t, &logins).URL)
	tok, err := sessionTokenSource(cfg, false, true, quiet).Token(context.Background())
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
	tok, err = sessionTokenSource(cfg, false, true, quiet).Token(context.Background())
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
	src := sessionTokenSource(cfg, false, true, quiet)
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
	if tok, _ := sessionTokenSource(cfg, false, true, quiet).Token(context.Background()); tok != "env-token" {
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
	if tok, err := sessionTokenSource(cfg, true, true, quiet).Token(context.Background()); err != nil || tok != minted {
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
	if tok, err := sessionTokenSource(cfg, false, true, quiet).Token(context.Background()); err != nil || tok != minted {
		t.Fatalf("%q %v", tok, err)
	}
}

func TestDeviceIDGeneratedOnceStoredAndSent(t *testing.T) {
	var logins atomic.Int32
	cfg := sessCfg(t, fakeFamlyLogin(t, &logins).URL)
	cfg.FamlyDeviceID = ""
	if _, err := sessionTokenSource(cfg, false, true, quiet).Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, _ := sentDevice.Load().(string)
	stored, _ := session.Open(cfg.SessionPath, true).Load()
	if !famly.ValidDeviceID(first) || stored.DeviceID != first {
		t.Fatalf("sent %q, stored %q; want one stored valid id", first, stored.DeviceID)
	}
	// A later run with an expired token sends the same id.
	_ = session.Open(cfg.SessionPath, false).Save(session.Session{DeviceID: first})
	if _, err := sessionTokenSource(cfg, false, true, quiet).Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again, _ := sentDevice.Load().(string); again != first {
		t.Errorf("second run sent %q, want the stored %q", again, first)
	}
}

func TestDeviceIDPrecedenceEnvOverFile(t *testing.T) {
	var logins atomic.Int32
	cfg := sessCfg(t, fakeFamlyLogin(t, &logins).URL) // env = pinnedID
	_ = session.Open(cfg.SessionPath, false).Save(session.Session{DeviceID: fileID})
	if _, err := sessionTokenSource(cfg, false, true, quiet).Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := sentDevice.Load().(string); got != pinnedID {
		t.Errorf("sent %q, want env id", got)
	}
	if st, _ := session.Open(cfg.SessionPath, true).Load(); st.DeviceID != fileID {
		t.Errorf("env id overwrote the stored one: %q", st.DeviceID)
	}
	// With no env, the file id is sent.
	cfg.FamlyDeviceID = ""
	_ = session.Open(cfg.SessionPath, false).Save(session.Session{DeviceID: fileID})
	if _, err := sessionTokenSource(cfg, false, true, quiet).Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := sentDevice.Load().(string); got != fileID {
		t.Errorf("sent %q, want file id", got)
	}
}

func TestDeviceIDFromAuthenticateReplyIsStored(t *testing.T) {
	var logins atomic.Int32
	cfg := sessCfg(t, fakeFamlyLogin(t, &logins).URL)
	cfg.FamlyDeviceID = ""
	replyDevice.Store(fileID)
	if _, err := sessionTokenSource(cfg, false, true, quiet).Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, _ := session.Open(cfg.SessionPath, true).Load(); st.DeviceID != fileID {
		t.Errorf("stored %q, want the id Famly returned", st.DeviceID)
	}
}

// The drift gate hands the token to Probe as a string, so it must not
// start from a stale env or cached token: seed=false logs in fresh.
func TestSessionUnseededIgnoresStaleEnvAndFileToken(t *testing.T) {
	var logins atomic.Int32
	cfg := sessCfg(t, fakeFamlyLogin(t, &logins).URL)
	cfg.FamlyAccessToken = "stale-env"
	if err := session.Open(cfg.SessionPath, false).Save(session.Session{AccessToken: "stale-file"}); err != nil {
		t.Fatal(err)
	}
	tok, err := sessionTokenSource(cfg, true, false, quiet).Token(context.Background())
	if err != nil || tok != minted || logins.Load() != 1 {
		t.Fatalf("unseeded: tok=%q err=%v logins=%d, want a fresh login", tok, err, logins.Load())
	}
	// Seeded (fetch): env wins over credentials, no login until a 401.
	logins.Store(0)
	tok, _ = sessionTokenSource(cfg, true, true, quiet).Token(context.Background())
	if tok != "stale-env" || logins.Load() != 0 {
		t.Fatalf("seeded: tok=%q logins=%d, want the env token without login", tok, logins.Load())
	}
}
