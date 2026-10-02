// Package privdir creates the directories bairn owns (save root, state
// dir) with mode 0700. MkdirAll leaves a pre-existing directory at its
// old mode, so the chmod is explicit.
package privdir

import (
	"log/slog"
	"os"
)

// WarnLoose logs a warning when dir is accessible to group or other. It
// never changes the mode: dir was chosen by the user and may be shared.
func WarnLoose(dir string) {
	if fi, err := os.Stat(dir); err == nil && fi.Mode().Perm()&0o077 != 0 {
		slog.Warn("directory is accessible to other users; bairn leaves its mode unchanged",
			"dir", dir, "mode", fi.Mode().Perm().String())
	}
}

// Ensure creates dir and any missing parents, then sets dir itself to
// 0700. Parents are never chmodded: callers pass only a directory
// bairn owns, never $HOME or a shared parent.
func Ensure(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}
