package asset

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

func TestUploadFromDiskRefusesPathOutsideRoot(t *testing.T) {
	ctx := context.Background()
	st, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Discover(ctx, "x", state.Asset{Source: "feed-image"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSaved(ctx, "x", "/etc/hosts", time.Now()); err != nil {
		t.Fatal(err)
	}
	// An unreachable Immich: the refusal must come before any request.
	im := sink.NewImmich(immich.New("http://127.0.0.1:1", "k"))
	_, err = UploadFromDisk(ctx, im, st, t.TempDir(), "x")
	if err == nil || !strings.Contains(err.Error(), "outside the save directory") {
		t.Fatalf("err = %v, want refusal outside the save directory", err)
	}
}
