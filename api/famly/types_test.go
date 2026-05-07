package famly

import (
	"testing"
	"time"
)

func TestImageTimeOffsetString(t *testing.T) {
	cases := []struct {
		name string
		zone string
		when time.Time
		want string
	}{
		{"empty falls back to UTC", "", time.Time{}, "+00:00"},
		{"UTC literal", "UTC", time.Time{}, "+00:00"},
		{"unknown zone falls back to UTC", "Not/A/Zone", time.Now(), "+00:00"},
		{"detroit in summer", "America/Detroit",
			time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC), "-04:00"},
		{"detroit in winter", "America/Detroit",
			time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC), "-05:00"},
		{"copenhagen summer (Famly HQ)", "Europe/Copenhagen",
			time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC), "+02:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it := ImageTime{Date: FamlyTime{Time: c.when}, Timezone: c.zone}
			if got := it.OffsetString(); got != c.want {
				t.Errorf("OffsetString = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFeedItemIsSystemGenerated(t *testing.T) {
	if (FeedItem{}).IsSystemGenerated() {
		t.Error("empty FeedItem should not be flagged system-generated")
	}
	if !(FeedItem{SystemPostTypeClass: "CheckIn"}).IsSystemGenerated() {
		t.Error("non-empty SystemPostTypeClass should flag system-generated")
	}
}

func TestImageBestURL(t *testing.T) {
	cases := []struct {
		name string
		img  Image
		want string
	}{
		{
			name: "rewrites size segment to original dimensions",
			img: Image{
				BigURL: "https://img.famly.co/image/HASH/1024x768/archive/2026/file.jpg?expires=X",
				Width:  2560, Height: 1920,
			},
			want: "https://img.famly.co/image/HASH/2560x1920/archive/2026/file.jpg?expires=X",
		},
		{
			name: "falls back to BigURL unchanged when no dimensions",
			img: Image{
				BigURL: "https://img.famly.co/image/HASH/1024x768/file.jpg",
			},
			want: "https://img.famly.co/image/HASH/1024x768/file.jpg",
		},
		{
			name: "falls back to URL when BigURL is empty",
			img: Image{
				URL:   "https://img.famly.co/image/HASH/600x450/file.jpg",
				Width: 1920, Height: 1080,
			},
			want: "https://img.famly.co/image/HASH/1920x1080/file.jpg",
		},
		{
			name: "leaves URL unchanged when no size segment matches",
			img: Image{
				BigURL: "https://example.com/no-size-here/file.jpg",
				Width:  2560, Height: 1920,
			},
			want: "https://example.com/no-size-here/file.jpg",
		},
		{
			name: "empty when both URLs are empty",
			img:  Image{Width: 100, Height: 100},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.img.BestURL()
			if got != c.want {
				t.Errorf("BestURL() = %q, want %q", got, c.want)
			}
		})
	}
}
