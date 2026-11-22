package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestProgressLinesEveryNPagesAsJSON(t *testing.T) {
	feed := newDatedFeed(t, 35, nil, false) // pages of 10, 10, 10, 5, then empty
	e := newRetryEnv(t, feed)
	var buf bytes.Buffer
	e.deps.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	res, err := Run(context.Background(), e.deps, Options{Source: SourceAll, ProgressEvery: 2})
	if err != nil {
		t.Fatal(err)
	}
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", l)
		}
		if m["msg"] == "progress" {
			lines = append(lines, m)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("progress lines = %d, want 2 (after pages 2 and 4) of %d pages", len(lines), res.PagesWalked)
	}
	first := lines[0]
	if first["pages"] != float64(2) || first["saved"] != float64(20) || first["discovered"] != float64(20) {
		t.Errorf("first progress = %v", first)
	}
	if first["postDate"] != postTime(15).Format("2006-01-02T15:04:05Z") {
		t.Errorf("postDate = %v, want the oldest post of page 2", first["postDate"])
	}
	for _, k := range []string{"uploaded", "skipped", "errors", "elapsed"} {
		if _, ok := first[k]; !ok {
			t.Errorf("progress line lacks %q", k)
		}
	}
}

func TestProgressOffByDefaultInOptions(t *testing.T) {
	feed := newDatedFeed(t, 25, nil, false)
	e := newRetryEnv(t, feed)
	var buf bytes.Buffer
	e.deps.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	if _, err := Run(context.Background(), e.deps, Options{Source: SourceAll}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `"msg":"progress"`) {
		t.Error("progress logged with ProgressEvery 0")
	}
}
