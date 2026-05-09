package drift

import (
	"reflect"
	"testing"
)

type addr struct {
	Street string `json:"street"`
	City   string `json:"city"`
}

type user struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Address  addr   `json:"address"`
	Internal string `json:"-"`
	hidden   string //nolint:unused
}

type userOmit struct {
	ID    string `json:"id,omitempty"`
	Email string `json:"email,omitempty"`
}

type group struct {
	Members []user `json:"members"`
}

type pointed struct {
	Sender *addr `json:"sender,omitempty"`
}

// customTime simulates a struct type whose JSON shape is a scalar
// (custom UnmarshalJSON). The fields are irrelevant to the test;
// what matters is that filter passes the raw value through when
// the schema declares a struct but the wire value isn't a map.
type customTime struct{}

type withTime struct {
	When customTime `json:"when"`
	Tag  string     `json:"tag"`
}

type bare struct {
	Untagged string
	Skipped  string `json:"-"`
}

func TestFilterDropsUnknownKeys(t *testing.T) {
	raw := map[string]any{
		"id":       "abc",
		"name":     "alice",
		"vendorOK": true, // not in user struct; should be dropped
	}
	got := Filter(raw, reflect.TypeOf(user{})).(map[string]any)
	want := map[string]any{
		"id":   "abc",
		"name": "alice",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestFilterKeepsKnownKeysOnly(t *testing.T) {
	raw := map[string]any{
		"id":   "abc",
		"name": "alice",
		"address": map[string]any{
			"street":  "1 Main",
			"city":    "GR",
			"zip":     "49503", // unknown in addr; dropped
			"country": "US",    // unknown in addr; dropped
		},
		"extra": "vendor field",
	}
	got := Filter(raw, reflect.TypeOf(user{})).(map[string]any)
	addrM, ok := got["address"].(map[string]any)
	if !ok {
		t.Fatalf("address missing or wrong type: %T", got["address"])
	}
	if _, ok := addrM["zip"]; ok {
		t.Errorf("address.zip should be dropped")
	}
	if _, ok := addrM["country"]; ok {
		t.Errorf("address.country should be dropped")
	}
	if addrM["street"] != "1 Main" {
		t.Errorf("address.street lost")
	}
	if _, ok := got["extra"]; ok {
		t.Errorf("top-level extra should be dropped")
	}
}

func TestFilterRespectsJSONDashTag(t *testing.T) {
	raw := map[string]any{
		"id":       "abc",
		"name":     "alice",
		"Internal": "should be dropped (json:\"-\")",
	}
	got := Filter(raw, reflect.TypeOf(user{})).(map[string]any)
	if _, ok := got["Internal"]; ok {
		t.Errorf("Internal field marked json:\"-\" should not pass through")
	}
}

func TestFilterRespectsOmitemptyTag(t *testing.T) {
	raw := map[string]any{
		"id":     "abc",
		"email":  "a@b.c",
		"vendor": true,
	}
	got := Filter(raw, reflect.TypeOf(userOmit{})).(map[string]any)
	if got["id"] != "abc" || got["email"] != "a@b.c" {
		t.Errorf("omitempty tag's name part lost: %v", got)
	}
	if _, ok := got["vendor"]; ok {
		t.Errorf("vendor (not in struct) should be dropped")
	}
}

func TestFilterSliceOfStruct(t *testing.T) {
	raw := map[string]any{
		"members": []any{
			map[string]any{"id": "a", "name": "x", "extra": 1},
			map[string]any{"id": "b", "name": "y", "extra": 2},
		},
	}
	got := Filter(raw, reflect.TypeOf(group{})).(map[string]any)
	mems, ok := got["members"].([]any)
	if !ok || len(mems) != 2 {
		t.Fatalf("members not a length-2 slice: %T %v", got["members"], got["members"])
	}
	for i, m := range mems {
		mm := m.(map[string]any)
		if _, ok := mm["extra"]; ok {
			t.Errorf("member[%d].extra should be dropped", i)
		}
		if mm["id"] == nil || mm["name"] == nil {
			t.Errorf("member[%d] lost id/name: %v", i, mm)
		}
	}
}

func TestFilterPointerField(t *testing.T) {
	raw := map[string]any{
		"sender": map[string]any{
			"street": "1 Main",
			"city":   "GR",
			"vendor": "extra",
		},
	}
	got := Filter(raw, reflect.TypeOf(pointed{})).(map[string]any)
	s := got["sender"].(map[string]any)
	if _, ok := s["vendor"]; ok {
		t.Errorf("sender.vendor should be dropped via pointer-deref recursion")
	}
	if s["street"] != "1 Main" || s["city"] != "GR" {
		t.Errorf("sender field values lost: %v", s)
	}
}

func TestFilterCustomUnmarshalledStructPassesRawThrough(t *testing.T) {
	// withTime.When is a Go struct, but on the wire it would be a
	// scalar/string for a hypothetical UnmarshalJSON. Simulate by
	// passing a string through where a struct schema is expected.
	raw := map[string]any{
		"when": "2026-05-08T12:34:56Z",
		"tag":  "labeled",
	}
	got := Filter(raw, reflect.TypeOf(withTime{})).(map[string]any)
	if got["when"] != "2026-05-08T12:34:56Z" {
		t.Errorf("custom-unmarshalled value should pass through: %v", got["when"])
	}
	if got["tag"] != "labeled" {
		t.Errorf("tag lost: %v", got)
	}
}

func TestFilterUntaggedFieldUsesGoName(t *testing.T) {
	raw := map[string]any{
		"Untagged": "kept",
		"Skipped":  "should drop",
		"vendor":   "should drop",
	}
	got := Filter(raw, reflect.TypeOf(bare{})).(map[string]any)
	if got["Untagged"] != "kept" {
		t.Errorf("untagged field with Go name not preserved: %v", got)
	}
	if _, ok := got["Skipped"]; ok {
		t.Errorf("Skipped (json:\"-\") should be dropped")
	}
	if _, ok := got["vendor"]; ok {
		t.Errorf("vendor (not in struct) should be dropped")
	}
}

func TestFilterNilSchema(t *testing.T) {
	raw := map[string]any{"x": "y"}
	got := Filter(raw, nil)
	if !reflect.DeepEqual(got, raw) {
		t.Errorf("nil schema should pass raw through unchanged: %v", got)
	}
}

func TestFilterRawMissingKey(t *testing.T) {
	// raw lacks the "address" field; struct declares it. Output
	// should not synthesise a nil entry; the key should be absent.
	raw := map[string]any{"id": "abc", "name": "alice"}
	got := Filter(raw, reflect.TypeOf(user{})).(map[string]any)
	if _, ok := got["address"]; ok {
		t.Errorf("missing-in-raw key should not appear in output")
	}
}

func TestFilterTypeMismatchReturnsRaw(t *testing.T) {
	// Schema says address is a struct; raw says it's a string. We
	// preserve the raw value so the shape signature surfaces the
	// type mismatch (rather than silently zero-ing it).
	raw := map[string]any{
		"id":      "abc",
		"name":    "alice",
		"address": "not a struct",
	}
	got := Filter(raw, reflect.TypeOf(user{})).(map[string]any)
	if got["address"] != "not a struct" {
		t.Errorf("type-mismatched field should pass raw through; got %v", got["address"])
	}
}
