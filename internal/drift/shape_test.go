package drift

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestShapePrimitives(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"string", "hello", "str"},
		{"int", float64(42), "int"},
		{"float", 3.14, "float"},
		{"bool true", true, "bool"},
		{"bool false", false, "bool"},
		{"null", nil, "null"},
		{"empty array", []any{}, []any{"<empty>"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Shape(c.in, ShapeOpts{})
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Shape(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestShapeObjectUnion(t *testing.T) {
	in := []any{
		map[string]any{"a": "x", "b": float64(1)},
		map[string]any{"b": float64(2), "c": true},
	}
	got := Shape(in, ShapeOpts{})
	wantArr, ok := got.([]any)
	if !ok || len(wantArr) != 2 {
		t.Fatalf("expected [merged, count], got %v", got)
	}
	mergedMap, ok := wantArr[0].(map[string]any)
	if !ok {
		t.Fatalf("merged is not a map: %T", wantArr[0])
	}
	want := map[string]any{"a": "str", "b": "int", "c": "bool"}
	if !reflect.DeepEqual(mergedMap, want) {
		t.Errorf("merged = %v, want %v", mergedMap, want)
	}
	if wantArr[1] != "<n=2>" {
		t.Errorf("count = %v, want <n=2>", wantArr[1])
	}
}

func TestShapeRecursionLimit(t *testing.T) {
	// Build a 9-deep nested map. maxDepth is 6, so recursion should
	// emit "..." somewhere in the chain.
	v := any("leaf")
	for i := 0; i < 9; i++ {
		v = map[string]any{"x": v}
	}
	got := Shape(v, ShapeOpts{})
	// Walk down maxDepth+1 times; the value at that depth should be
	// the "..." sentinel.
	cursor := got
	for i := 0; i < maxDepth+1; i++ {
		m, ok := cursor.(map[string]any)
		if !ok {
			t.Fatalf("at depth %d expected map, got %T", i, cursor)
		}
		cursor = m["x"]
	}
	if cursor != "..." {
		t.Errorf("expected '...' beyond maxDepth, got %v", cursor)
	}
}

func TestDiffIdentical(t *testing.T) {
	a := map[string]any{"x": "str", "n": "int"}
	b := map[string]any{"x": "str", "n": "int"}
	if d := Diff(a, b); len(d) != 0 {
		t.Errorf("expected no diff, got %v", d)
	}
}

func TestDiffAddedRemoved(t *testing.T) {
	a := map[string]any{"x": "str", "y": "int"}
	b := map[string]any{"x": "str", "z": "bool"}
	got := Diff(a, b)
	want := []string{"/y: removed", "/z: added"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestDiffTypeChange(t *testing.T) {
	a := map[string]any{"x": "str"}
	b := map[string]any{"x": "int"}
	got := Diff(a, b)
	want := []string{`/x: "str" -> "int"`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestDiffArrayDescent(t *testing.T) {
	a := []any{map[string]any{"x": "str"}, "<n=3>"}
	b := []any{map[string]any{"x": "int"}, "<n=4>"}
	got := Diff(a, b)
	want := []string{`[]/x: "str" -> "int"`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestShapeAnonymizedCounts(t *testing.T) {
	in := map[string]any{
		"relations": []any{
			map[string]any{"id": "x", "name": "y"},
			map[string]any{"id": "z", "name": "w"},
			map[string]any{"id": "p", "name": "q"},
		},
		"empty": []any{},
	}
	got := Shape(in, ShapeOpts{AnonymizeCounts: true}).(map[string]any)
	relArr := got["relations"].([]any)
	if relArr[1] != "<n=*>" {
		t.Errorf("expected anonymized count <n=*>, got %v", relArr[1])
	}
	emptyArr := got["empty"].([]any)
	if emptyArr[0] != "<empty>" {
		t.Errorf("empty array should still be <empty>, got %v", emptyArr[0])
	}
}

func TestSignatureRoundTripsThroughJSON(t *testing.T) {
	sig := Shape(map[string]any{
		"users": []any{
			map[string]any{"id": "abc", "n": float64(5)},
			map[string]any{"id": "def", "n": float64(7), "extra": "y"},
		},
		"empty": []any{},
		"null":  nil,
	}, ShapeOpts{})
	b, err := json.Marshal(sig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTripped any
	if err := json.Unmarshal(b, &roundTripped); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d := Diff(sig, roundTripped); len(d) != 0 {
		t.Errorf("roundtrip changed signature: %v", d)
	}
}

func TestDiffEmptyAndNullAreWildcards(t *testing.T) {
	base := map[string]any{
		"images": []any{"<empty>"},
		"body":   "null",
	}
	live := map[string]any{
		"images": []any{map[string]any{"id": "str"}, "<n=*>"},
		"body":   map[string]any{"text": "str"},
	}
	if d := Diff(base, live); len(d) != 0 {
		t.Fatalf("baseline wildcards vs live: %v", d)
	}
	if d := Diff(live, base); len(d) != 0 {
		t.Fatalf("live wildcards vs baseline: %v", d)
	}
	// A wildcard must not hide a real type change elsewhere.
	live["id"] = "str"
	base["id"] = "int"
	if d := Diff(base, live); len(d) != 1 {
		t.Fatalf("want one type change, got %v", d)
	}
}

func TestShapeMergesNestedAcrossItems(t *testing.T) {
	feed := []any{
		map[string]any{"images": []any{}, "sender": map[string]any{"id": "x"}},
		map[string]any{
			"images": []any{map[string]any{"url": "u"}},
			"sender": map[string]any{"name": "n"},
		},
	}
	got := Shape(feed, ShapeOpts{AnonymizeCounts: true}).([]any)
	item := got[0].(map[string]any)
	imgs := item["images"].([]any)
	img, ok := imgs[0].(map[string]any)
	if !ok || img["url"] != "str" {
		t.Fatalf("images not merged from later item: %v", imgs)
	}
	sender := item["sender"].(map[string]any)
	if sender["id"] != "str" || sender["name"] != "str" {
		t.Fatalf("sender not unioned: %v", sender)
	}
	if len(imgs) != 2 || imgs[1] != "<n=*>" {
		t.Fatalf("count marker lost when first item is empty: %v", imgs)
	}
	// Item 1 empty images vs item 2 image map, in either order, diff clean.
	rev := []any{feed[1], feed[0]}
	if d := Diff(got, Shape(rev, ShapeOpts{AnonymizeCounts: true})); len(d) != 0 {
		t.Fatalf("order-dependent shape: %v", d)
	}
}

func TestDiffReportsRenamedKeyInsideSeededArray(t *testing.T) {
	base := Shape([]any{
		map[string]any{"images": []any{}},
		map[string]any{"images": []any{map[string]any{"url_big": "u"}}},
	}, ShapeOpts{AnonymizeCounts: true})
	live := Shape([]any{
		map[string]any{"images": []any{map[string]any{"urlBig": "u"}}},
	}, ShapeOpts{AnonymizeCounts: true})
	if d := Diff(base, live); len(d) == 0 {
		t.Fatal("renamed image key not reported")
	}
}
