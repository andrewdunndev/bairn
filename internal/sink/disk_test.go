package sink

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/exif"
)

func miniJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{0xff, 0x80, 0x40, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func writeTmpJPEG(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jpg")
	if err := os.WriteFile(src, miniJPEG(t), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	return src
}

func TestDiskPutDefaults(t *testing.T) {
	root := t.TempDir()
	d, err := NewDisk(root, "", "")
	if err != nil {
		t.Fatalf("NewDisk: %v", err)
	}
	src := writeTmpJPEG(t)
	when := time.Date(2026, 5, 6, 14, 30, 0, 0, time.UTC)
	r, err := d.Put(context.Background(), PutInput{
		FamlyImageID:  "img-001",
		Source:        "feed-image",
		FeedItemID:    "post-001",
		SourcePath:    src,
		Filename:      "img-001.jpg",
		SHA1:          "abc",
		FileCreatedAt: when,
		EXIF: exif.Fields{
			DateTimeOriginal: when,
			ImageDescription: "captioned",
			Software:         "bairn 0.1",
		},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if r.Status != "created" {
		t.Errorf("Status = %q", r.Status)
	}
	if !strings.Contains(r.DestPath, "2026-05-06") {
		t.Errorf("DestPath should include date dir: %s", r.DestPath)
	}
	if !strings.HasSuffix(r.DestPath, "feed-image-2026-05-06_14-30-00-img-001.jpg") {
		t.Errorf("DestPath filename does not match pattern: %s", r.DestPath)
	}
	// File must exist and be non-empty.
	stat, err := os.Stat(r.DestPath)
	if err != nil || stat.Size() == 0 {
		t.Errorf("dest file missing or empty: stat=%v err=%v", stat, err)
	}
	// FS mtime should be the FileCreatedAt.
	if !stat.ModTime().Equal(when) {
		t.Errorf("mtime = %v, want %v", stat.ModTime(), when)
	}
}

func TestDiskPutIdempotent(t *testing.T) {
	root := t.TempDir()
	d, _ := NewDisk(root, "", "")
	src := writeTmpJPEG(t)
	when := time.Date(2026, 5, 6, 14, 30, 0, 0, time.UTC)
	in := PutInput{
		FamlyImageID:  "img-002",
		Source:        "feed-image",
		FeedItemID:    "post-002",
		SourcePath:    src,
		Filename:      "img-002.jpg",
		FileCreatedAt: when,
		EXIF:          exif.Fields{DateTimeOriginal: when},
	}
	r1, err := d.Put(context.Background(), in)
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	r2, err := d.Put(context.Background(), in)
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if r2.Status != "duplicate" {
		t.Errorf("second Put Status = %q, want duplicate", r2.Status)
	}
	if r1.DestPath != r2.DestPath {
		t.Errorf("DestPath differed across Puts: %s vs %s", r1.DestPath, r2.DestPath)
	}
}

func TestRenderPattern(t *testing.T) {
	when := time.Date(2026, 5, 6, 14, 30, 45, 0, time.UTC)
	got, err := renderPattern("{{.Source}}-%Y-%m-%d_%H-%M-%S-{{.ID}}.{{.Ext}}", PutInput{
		FamlyImageID:  "abc-123",
		Source:        "feed-image",
		Filename:      "abc-123.jpg",
		FileCreatedAt: when,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := "feed-image-2026-05-06_14-30-45-abc-123.jpg"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderPatternMissingKeyErrors(t *testing.T) {
	_, err := renderPattern("{{.NotAField}}-%Y", PutInput{FileCreatedAt: time.Now()})
	if err == nil {
		t.Fatal("expected error for unknown template key")
	}
}
