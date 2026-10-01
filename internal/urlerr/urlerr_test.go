package urlerr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestRedactDropsQuery(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1/img.jpg?Signature=SECRET&Expires=1", nil)
	_, err := http.DefaultClient.Do(req)
	if err == nil || !strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("fixture error should carry the query: %v", err)
	}
	got := Redact(fmt.Errorf("download: %w", err))
	if strings.Contains(got.Error(), "SECRET") || !strings.Contains(got.Error(), "/img.jpg") {
		t.Errorf("redacted = %q", got)
	}
}

func TestRedactLeavesOtherErrors(t *testing.T) {
	e := errors.New("plain")
	if Redact(e) != e {
		t.Error("non-url error changed")
	}
}
