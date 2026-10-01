package sink

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestDiskPutWritesSidecar(t *testing.T) {
	d, err := NewDisk(t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("NewDisk: %v", err)
	}
	src := writeTmpJPEG(t) // content is irrelevant for a non-JPEG name
	in := PutInput{
		FamlyImageID: "vid-001", Source: "feed-video", FeedItemID: "post-001",
		SourcePath: src, Filename: "vid-001.mp4",
		FileCreatedAt: time.Date(2026, 5, 6, 14, 30, 0, 0, time.UTC),
		Sidecar:       []byte("<x:xmpmeta/>"),
	}
	r, err := d.Put(context.Background(), in)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := os.ReadFile(r.DestPath + ".xmp")
	if err != nil || string(got) != "<x:xmpmeta/>" {
		t.Fatalf("sidecar = %q, err %v", got, err)
	}
	if _, err := os.Stat(r.DestPath + ".xmp.tmp"); err == nil {
		t.Error("tmp sidecar left behind")
	}
}
