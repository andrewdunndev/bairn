// Package session stores bairn's Famly session next to the state file:
// the device id Famly knows this install by and the last access token.
// Reusing both means a run does not log in with the password (and so
// does not trigger Famly's new-login e-mail) until the token expires.
//
// The file is bairn's own: written 0600 in a 0700-created directory,
// atomically, and never holding a token from FAMLY_ACCESS_TOKEN.
// Deleting it forces a fresh login on the next run.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileName is the session file's name within the state directory.
const FileName = "session.json"

// Session is the persisted content.
type Session struct {
	DeviceID    string `json:"deviceId,omitempty"`
	AccessToken string `json:"accessToken,omitempty"`
}

// Store reads and writes one session file. A read-only store loads but
// never writes, for dry runs and for a path that may not be writable.
type Store struct {
	path     string
	readOnly bool
}

// Open returns a Store for path.
func Open(path string, readOnly bool) *Store { return &Store{path: path, readOnly: readOnly} }

// Load returns the stored session. A missing file is an empty session
// with a nil error; an unreadable or corrupt file is an empty session
// with the error, so the caller can warn and carry on with a fresh
// login. The error never carries file content.
func (s *Store) Load() (Session, error) {
	if fi, err := os.Lstat(s.path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return Session{}, fmt.Errorf("session file %s is a symlink; refusing to follow it (delete it to log in afresh)", s.path)
	}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Session{}, nil
	}
	if err != nil {
		return Session{}, fmt.Errorf("read session file %s: %w", s.path, err)
	}
	var out Session
	if err := json.Unmarshal(b, &out); err != nil {
		return Session{}, fmt.Errorf("session file %s is not valid JSON (delete it to log in afresh)", s.path)
	}
	return out, nil
}

// Save writes sess atomically. On a read-only store it does nothing.
func (s *Store) Save(sess Session) error {
	if s.readOnly {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("session dir %s: %w", dir, err)
	}
	b, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("create session temp in %s: %w", dir, err)
	}
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write session file %s: %w", s.path, err)
	}
	return nil
}
