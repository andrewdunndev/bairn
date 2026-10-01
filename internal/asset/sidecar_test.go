package asset

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/sink"
)

func TestVideoSidecarOnly(t *testing.T) {
	created := time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC)
	item := famly.FeedItem{
		FeedItemID: "post-001", Body: "Nap time", CreatedDate: famly.FamlyTime{Time: created},
	}
	vid := DiscoverVideo(famly.Video{VideoID: "vid-001", URL: "https://fixture/v"}, item)
	sc, err := vid.Sidecar("bairn")
	if err != nil || sc == nil {
		t.Fatalf("video sidecar = %q, err %v", sc, err)
	}
	s := string(sc)
	for _, want := range []string{"Nap time", "<photoshop:DateCreated>2026-05-06T14:00:00+00:00<"} {
		if !strings.Contains(s, want) {
			t.Errorf("sidecar missing %q:\n%s", want, s)
		}
	}

	img := DiscoverImage(famly.Image{ImageID: "img-001"}, item)
	if sc, err := img.Sidecar("bairn"); err != nil || sc != nil {
		t.Errorf("photo sidecar = %q, err %v; want none", sc, err)
	}
}

func TestVideoSidecarCarriesTags(t *testing.T) {
	vid := Discovered{source: SourceFeedVideo, body: "x", tagNames: []string{"Rory"},
		fileCreatedAt: time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC), tzOffset: "-04:00"}
	sc, err := vid.Sidecar("bairn")
	if err != nil {
		t.Fatal(err)
	}
	s := string(sc)
	for _, want := range []string{"<dc:subject><rdf:Bag><rdf:li>Rory<", "<digiKam:TagsList><rdf:Seq><rdf:li>Rory<", "2026-05-06T10:00:00-04:00"} {
		if !strings.Contains(s, want) {
			t.Errorf("sidecar missing %q:\n%s", want, s)
		}
	}
}

func TestSaveWritesVideoSidecarOnly(t *testing.T) {
	jpegBytes := makeJPEG(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("fake mp4"))
	})
	mux.HandleFunc("/i", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(jpegBytes) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	disk, err := sink.NewDisk(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC)
	item := famly.FeedItem{FeedItemID: "post-001", Body: "Nap time", CreatedDate: famly.FamlyTime{Time: created}}

	for _, c := range []struct {
		disc    Discovered
		sidecar bool
	}{
		{DiscoverVideo(famly.Video{VideoID: "vid-001", URL: srv.URL + "/v"}, item), true},
		{DiscoverImage(famly.Image{ImageID: "img-001", BigURL: srv.URL + "/i"}, item), false},
	} {
		dl, err := c.disc.Download(context.Background(), nil)
		if err != nil {
			t.Fatalf("Download: %v", err)
		}
		saved, err := dl.Save(context.Background(), disk, "bairn")
		dl.Cleanup()
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
		_, statErr := os.Stat(saved.FinalPath() + ".xmp")
		if (statErr == nil) != c.sidecar {
			t.Errorf("%s: sidecar present = %v, want %v", filepath.Base(saved.FinalPath()), statErr == nil, c.sidecar)
		}
		if c.sidecar && len(saved.sidecar) == 0 {
			t.Error("Saved did not retain the sidecar for upload")
		}
	}
}

func TestUploadSendsVideoSidecar(t *testing.T) {
	var got string
	mux := http.NewServeMux()
	mux.HandleFunc("/v", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("fake mp4")) })
	mux.HandleFunc("/assets", func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(p)
			if p.FormName() == "sidecarData" {
				got = string(b)
			}
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"a1","status":"created"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	disk, _ := sink.NewDisk(t.TempDir(), "", "")
	item := famly.FeedItem{FeedItemID: "p", Body: "Nap time",
		CreatedDate: famly.FamlyTime{Time: time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC)}}
	dl, err := DiscoverVideo(famly.Video{VideoID: "vid-001", URL: srv.URL + "/v"}, item).Download(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Cleanup()
	saved, err := dl.Save(context.Background(), disk, "bairn")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saved.Upload(context.Background(), sink.NewImmich(immich.New(srv.URL, "k"))); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !strings.Contains(got, "Nap time") {
		t.Errorf("sidecarData = %q", got)
	}
}
