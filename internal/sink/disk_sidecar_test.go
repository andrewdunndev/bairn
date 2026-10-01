package sink

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func videoInput(t *testing.T, sidecar string) PutInput {
	return PutInput{
		FamlyImageID: "vid-001", Source: "feed-video", FeedItemID: "post-001",
		SourcePath: writeTmpJPEG(t), Filename: "vid-001.mp4",
		FileCreatedAt: time.Date(2026, 5, 6, 14, 30, 0, 0, time.UTC),
		Sidecar:       []byte(sidecar),
	}
}

func TestDiskPutWritesMissingSidecarBesideExistingMedia(t *testing.T) {
	d, _ := NewDisk(t.TempDir(), "", "")
	r, err := d.Put(context.Background(), videoInput(t, "<a/>"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(r.DestPath + ".xmp"); err != nil {
		t.Fatal(err)
	}
	r2, err := d.Put(context.Background(), videoInput(t, "<a/>"))
	if err != nil || r2.Status != "duplicate" {
		t.Fatalf("second Put = %+v, %v", r2, err)
	}
	if got, err := os.ReadFile(r.DestPath + ".xmp"); err != nil || string(got) != "<a/>" {
		t.Fatalf("sidecar = %q, err %v", got, err)
	}
}

func TestDiskPutSidecarPrecedesMedia(t *testing.T) {
	d, _ := NewDisk(t.TempDir(), "", "")
	in := videoInput(t, "<a/>")
	in.SourcePath = filepath.Join(t.TempDir(), "missing") // media step fails
	if _, err := d.Put(context.Background(), in); err == nil {
		t.Fatal("want error from missing source")
	}
	matches, _ := filepath.Glob(filepath.Join(d.root, "*", "*.xmp"))
	if len(matches) != 1 {
		t.Fatalf("sidecar not written before the media step: %v", matches)
	}
}

func TestDiskPutRefusesPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	d, _ := NewDisk(root, "{{.ID}}.{{.Ext}}", "")
	in := videoInput(t, "")
	in.FamlyImageID = "../../escape"
	_, err := d.Put(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("err = %v, want outside the save directory", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.mp4")); err == nil {
		t.Fatal("file written outside root")
	}
}

func TestDiskDirsArePrivate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archive")
	d, _ := NewDisk(root, "", "")
	r, err := d.Put(context.Background(), videoInput(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, filepath.Dir(r.DestPath)} {
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v, err %v, want 0700", dir, fi.Mode().Perm(), err)
		}
	}
}
