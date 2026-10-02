package drift

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A depth-capped walk wrote "..." as a leaf. Shapes now walk every
// level, so a committed "..." would read as a change on every run.
func TestCommittedBaselinesHaveNoDepthSentinel(t *testing.T) {
	files, err := filepath.Glob("../../discovery/baselines/main/*.shape")
	if err != nil || len(files) == 0 {
		t.Fatalf("no baseline shapes found: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"..."`) {
			t.Errorf("%s holds a \"...\" depth sentinel", f)
		}
	}
}
