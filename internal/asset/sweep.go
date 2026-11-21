package asset

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SweepStaleTemps removes in-flight download files (TempPrefix, ".part")
// under root that were last modified more than olderThan ago, the
// leftovers of a killed or OOM-killed run. It returns how many it
// removed; errors on individual entries are skipped.
func SweepStaleTemps(root string, olderThan time.Duration) int {
	cutoff := time.Now().Add(-olderThan)
	n := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // best effort
		}
		name := d.Name()
		if !strings.HasPrefix(name, TempPrefix) || !strings.HasSuffix(name, ".part") {
			return nil
		}
		if fi, err := d.Info(); err != nil || fi.ModTime().After(cutoff) {
			return nil
		}
		if os.Remove(path) == nil {
			n++
		}
		return nil
	})
	return n
}
