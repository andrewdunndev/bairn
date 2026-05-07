package asset

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

func TestFilenameForContentType(t *testing.T) {
	cases := []struct {
		filename, ct, want string
	}{
		{"img.jpg", "image/jpeg", "img.jpg"},
		{"img.jpg", "image/png", "img.png"},
		{"img.jpg", "image/heic", "img.heic"},
		{"img.jpg", "image/heif", "img.heic"}, // Apple convention: .heic for both
		{"img.jpg", "image/webp", "img.webp"},
		{"img.jpg", "video/mp4", "img.mp4"},
		{"img.jpg", "image/jpeg; charset=binary", "img.jpg"},
		{"img.jpg", "", "img.jpg"},
		{"img.jpg", "application/octet-stream", "img.jpg"},
		{"abc.heic", "image/jpeg", "abc.jpg"},
	}
	for _, c := range cases {
		got := filenameForContentType(c.filename, c.ct)
		if got != c.want {
			t.Errorf("filenameForContentType(%q, %q) = %q, want %q",
				c.filename, c.ct, got, c.want)
		}
	}
}

func TestDiscoverImagePicksTime(t *testing.T) {
	created := time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC)
	img := famly.Image{
		ImageID:   "img-001",
		URL:       "https://fixture/image/img-001",
		BigURL:    "https://fixture/image/img-001/big",
		CreatedAt: famly.ImageTime{Date: famly.FamlyTime{Time: created.Add(time.Minute)}},
	}
	item := famly.FeedItem{
		FeedItemID:  "post-001",
		Body:        "captioned",
		CreatedDate: famly.FamlyTime{Time: created},
		Sender:      &famly.Sender{Name: "Educator A"},
	}
	d := DiscoverImage(img, item)
	if d.FamlyImageID() != "img-001" {
		t.Errorf("FamlyImageID = %q", d.FamlyImageID())
	}
	if d.URL() != "https://fixture/image/img-001/big" {
		t.Errorf("URL should prefer big variant, got %q", d.URL())
	}
	if !d.FileCreatedAt().Equal(created.Add(time.Minute)) {
		t.Errorf("FileCreatedAt = %v", d.FileCreatedAt())
	}
	if d.SenderName() != "Educator A" {
		t.Errorf("SenderName = %q", d.SenderName())
	}

	// EXIF derivation
	ef := d.EXIF("bairn 0.1")
	if !ef.DateTimeOriginal.Equal(created.Add(time.Minute)) {
		t.Errorf("EXIF.DateTimeOriginal = %v", ef.DateTimeOriginal)
	}
	if ef.Artist != "Educator A" {
		t.Errorf("EXIF.Artist = %q", ef.Artist)
	}
	if ef.UserComment != "captioned - Educator A" {
		t.Errorf("EXIF.UserComment = %q", ef.UserComment)
	}
	if ef.Software != "bairn 0.1" {
		t.Errorf("EXIF.Software = %q", ef.Software)
	}
}

// makeJPEG returns the bytes of a tiny solid-colour JPEG.
func makeJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{0xff, 0x80, 0x40, 0xff})
		}
	}
	f, _ := os.CreateTemp("", "fixture-*.jpg")
	defer f.Close()
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatalf("encode: %v", err)
	}
	body, _ := os.ReadFile(f.Name())
	_ = os.Remove(f.Name())
	return body
}

func TestPipelineSavedOnly(t *testing.T) {
	jpegBytes := makeJPEG(t)

	// Famly side: serve the JPEG.
	famlyMux := http.NewServeMux()
	famlyMux.HandleFunc("/image/img-001/big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(jpegBytes)
	})
	famlySrv := httptest.NewServer(famlyMux)
	t.Cleanup(famlySrv.Close)

	// Save dir + state
	saveRoot := t.TempDir()
	disk, err := sink.NewDisk(saveRoot, "", "")
	if err != nil {
		t.Fatalf("NewDisk: %v", err)
	}
	st, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("Open state: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Build asset and run transitions.
	created := time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC)
	img := famly.Image{
		ImageID:   "img-001",
		BigURL:    famlySrv.URL + "/image/img-001/big",
		CreatedAt: famly.ImageTime{Date: famly.FamlyTime{Time: created}},
	}
	item := famly.FeedItem{
		FeedItemID:  "post-001",
		Body:        "x",
		CreatedDate: famly.FamlyTime{Time: created},
		Sender:      &famly.Sender{Name: "Educator A"},
	}

	disc := DiscoverImage(img, item)
	if err := st.Discover(context.Background(), disc.FamlyImageID(), state.Asset{
		Source:     string(disc.Source()),
		FeedItemID: disc.FeedItemID(),
	}); err != nil {
		t.Fatalf("state.Discover: %v", err)
	}

	dl, err := disc.Download(context.Background(), nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer dl.Cleanup()
	if dl.Size() == 0 {
		t.Errorf("Size = 0")
	}

	saved, err := dl.Save(context.Background(), disk, "bairn 0.1-test")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.FinalPath() == "" {
		t.Error("FinalPath empty")
	}

	rec, err := saved.Record(context.Background(), st)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if rec.RecordedAt().IsZero() {
		t.Error("RecordedAt should be set")
	}
	if rec.Uploaded() != nil {
		t.Error("Saved.Record should leave Uploaded nil")
	}

	// Verify state and disk.
	saved2, err := st.IsSaved(context.Background(), "img-001")
	if err != nil || !saved2 {
		t.Errorf("IsSaved = (%v, %v)", saved2, err)
	}
	uploaded, err := st.IsUploaded(context.Background(), "img-001")
	if err != nil || uploaded {
		t.Errorf("IsUploaded should be false; got (%v, %v)", uploaded, err)
	}
	if _, err := os.Stat(saved.FinalPath()); err != nil {
		t.Errorf("file at %s missing: %v", saved.FinalPath(), err)
	}
}
