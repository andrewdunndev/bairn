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
			got := Shape(c.in)
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
	got := Shape(in)
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
	got := Shape(v)
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

func TestSignatureRoundTripsThroughJSON(t *testing.T) {
	sig := Shape(map[string]any{
		"users": []any{
			map[string]any{"id": "abc", "n": float64(5)},
			map[string]any{"id": "def", "n": float64(7), "extra": "y"},
		},
		"empty": []any{},
		"null":  nil,
	})
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
