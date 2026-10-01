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
	vid := DiscoverVideo(famly.Video{VideoID: "vid-001", URL: "https://fixture/v"}, item, time.UTC)
	sc, err := vid.Sidecar("bairn")
	if err != nil || sc == nil {
		t.Fatalf("video sidecar = %q, err %v", sc, err)
	}
	s := string(sc)
	for _, want := range []string{"Nap time", "<photoshop:DateCreated>2026-05-06T14:00:00+00:00<", "<exif:DateTimeOriginal>2026-05-06T14:00:00+00:00<"} {
		if !strings.Contains(s, want) {
			t.Errorf("sidecar missing %q:\n%s", want, s)
		}
	}

	img := DiscoverImage(famly.Image{ImageID: "img-001"}, item, time.UTC)
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
		{DiscoverVideo(famly.Video{VideoID: "vid-001", URL: srv.URL + "/v"}, item, time.UTC), true},
		{DiscoverImage(famly.Image{ImageID: "img-001", BigURL: srv.URL + "/i"}, item, time.UTC), false},
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
	dl, err := DiscoverVideo(famly.Video{VideoID: "vid-001", URL: srv.URL + "/v"}, item, time.UTC).Download(context.Background(), nil)
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

func TestVideoSidecarOffsetFollowsSiblingImageZone(t *testing.T) {
	cases := []struct {
		name    string
		created time.Time
		want    string
	}{
		{"summer", time.Date(2026, 7, 4, 16, 0, 0, 0, time.UTC), "2026-07-04T12:00:00-04:00"},
		{"winter", time.Date(2026, 1, 15, 17, 0, 0, 0, time.UTC), "2026-01-15T12:00:00-05:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			item := famly.FeedItem{
				FeedItemID:  "post-001",
				CreatedDate: famly.FamlyTime{Time: c.created},
				Images:      []famly.Image{{ImageID: "img-001", CreatedAt: famly.ImageTime{Timezone: "America/Detroit"}}},
			}
			sc, err := DiscoverVideo(famly.Video{VideoID: "vid-001"}, item, nil).Sidecar("bairn")
			if err != nil || sc == nil {
				t.Fatalf("sidecar = %q, err %v", sc, err)
			}
			if !strings.Contains(string(sc), "<exif:DateTimeOriginal>"+c.want+"<") {
				t.Errorf("sidecar missing %q:\n%s", c.want, sc)
			}
		})
	}
}

func TestImageOffsetEvaluatedAtPickedInstant(t *testing.T) {
	// No image date: the post date is stamped, so its offset is used.
	item := famly.FeedItem{CreatedDate: famly.FamlyTime{Time: time.Date(2026, 1, 15, 17, 0, 0, 0, time.UTC)}}
	img := famly.Image{ImageID: "img-001", CreatedAt: famly.ImageTime{Timezone: "America/Detroit"}}
	if got := DiscoverImage(img, item, time.UTC).tzOffset; got != "-05:00" {
		t.Errorf("tzOffset = %q, want -05:00", got)
	}
}

func TestVideoOffsetDefaultsToZone(t *testing.T) {
	detroit, err := time.LoadLocation("America/Detroit")
	if err != nil {
		t.Skip("no tzdata")
	}
	cases := []struct {
		name    string
		created time.Time
		zone    *time.Location
		want    string
	}{
		{"summer dst", time.Date(2026, 7, 4, 16, 0, 0, 0, time.UTC), detroit, "-04:00"},
		{"winter", time.Date(2026, 1, 15, 17, 0, 0, 0, time.UTC), detroit, "-05:00"},
		{"utc zone", time.Date(2026, 7, 4, 16, 0, 0, 0, time.UTC), time.UTC, "+00:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			item := famly.FeedItem{FeedItemID: "p", CreatedDate: famly.FamlyTime{Time: c.created}}
			if got := DiscoverVideo(famly.Video{VideoID: "v"}, item, c.zone).tzOffset; got != c.want {
				t.Errorf("offset = %q, want %q", got, c.want)
			}
		})
	}
}

func TestVideoOffsetNilZoneIsLocal(t *testing.T) {
	old := time.Local
	t.Cleanup(func() { time.Local = old })
	time.Local = time.FixedZone("test", -7*3600)
	item := famly.FeedItem{CreatedDate: famly.FamlyTime{Time: time.Date(2026, 7, 4, 16, 0, 0, 0, time.UTC)}}
	if got := DiscoverVideo(famly.Video{VideoID: "v"}, item, nil).tzOffset; got != "-07:00" {
		t.Errorf("offset = %q, want -07:00", got)
	}
}

func TestImageOffsetDefaultsToZone(t *testing.T) {
	detroit, err := time.LoadLocation("America/Detroit")
	if err != nil {
		t.Skip("no tzdata")
	}
	cases := []struct {
		name string
		at   time.Time
		tz   string
		zone *time.Location
		want string
	}{
		{"zoneless summer", time.Date(2026, 7, 4, 16, 0, 0, 0, time.UTC), "", detroit, "-04:00"},
		{"zoneless winter", time.Date(2026, 1, 15, 17, 0, 0, 0, time.UTC), "", detroit, "-05:00"},
		{"own zone wins", time.Date(2026, 7, 4, 16, 0, 0, 0, time.UTC), "Europe/Paris", detroit, "+02:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			img := famly.Image{ImageID: "i", CreatedAt: famly.ImageTime{Date: famly.FamlyTime{Time: c.at}, Timezone: c.tz}}
			if got := DiscoverImage(img, famly.FeedItem{}, c.zone).tzOffset; got != c.want {
				t.Errorf("offset = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDownloadErrorOmitsSignedQuery(t *testing.T) {
	img := famly.Image{ImageID: "img-001", BigURL: "http://127.0.0.1:1/i.jpg?Signature=SECRET"}
	d := DiscoverImage(img, famly.FeedItem{}, time.UTC)
	_, err := d.Download(context.Background(), nil)
	if err == nil {
		t.Fatal("want a transport error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error carries the signed query: %v", err)
	}
}
