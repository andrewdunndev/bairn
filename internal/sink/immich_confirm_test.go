package sink

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/dunn.dev/bairn/api/immich"
)

// TestImmichConfirmOutcomes pins the mapping from bulk-upload-check
// results to outcomes. Only a duplicate under the entry's own asset id,
// not trashed, may read as Held: anything else reaching Held lets prune
// delete a file the remote does not keep.
func TestImmichConfirmOutcomes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[
			{"id":"held","action":"reject","reason":"duplicate","assetId":"r-held","isTrashed":false},
			{"id":"trashed","action":"reject","reason":"duplicate","assetId":"r-trashed","isTrashed":true},
			{"id":"mismatch","action":"reject","reason":"duplicate","assetId":"r-other","isTrashed":false},
			{"id":"mismatch-trashed","action":"reject","reason":"duplicate","assetId":"r-other","isTrashed":true},
			{"id":"absent","action":"accept"}]}`))
	}))
	defer srv.Close()

	ids := []string{"held", "trashed", "mismatch", "mismatch-trashed", "absent"}
	items := make([]ConfirmItem, len(ids))
	for k, id := range ids {
		items[k] = ConfirmItem{ID: id, RemoteID: "r-" + id, SHA1: strings.Repeat(string(rune('a'+k)), 40)}
	}
	res, err := NewImmich(immich.New(srv.URL, "k")).Confirm(context.Background(), items)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Outcome{
		"held": Held, "trashed": Trashed, "mismatch": Mismatch,
		"mismatch-trashed": Mismatch, "absent": Absent,
	}
	if len(res) != len(want) {
		t.Fatalf("got %d results, want %d", len(res), len(want))
	}
	for _, r := range res {
		if r.Outcome != want[r.ID] {
			t.Errorf("%s: outcome %v, want %v", r.ID, r.Outcome, want[r.ID])
		}
	}
}
