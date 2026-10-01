// Package drift implements bairn's vendor API drift detection:
// hits a manifest of endpoints, records JSON-key-only signatures
// (no values), and compares them against a prior baseline so
// vendor schema changes surface before they break a fetch.
//
// Output format matches the discovery/probe/shape.py prototype
// byte-for-byte under JSON marshalling, so signatures written by
// either tool are interchangeable.
package drift

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

const maxDepth = 6

// ShapeOpts configures Shape's behavior.
type ShapeOpts struct {
	// AnonymizeCounts replaces actual array lengths in the
	// "<n=N>" sentinel with "<n=*>". Useful when baselines are
	// committed publicly and the count of household relations,
	// roles, or feed items is operator-private context that
	// should not leak via cardinality diffs.
	AnonymizeCounts bool
}

// Shape returns a deterministic JSON-key-only signature for v.
// Maps preserve their key set; arrays become a [merged-shape, "<n=N>"]
// pair representing a union of the first 5 elements; primitives become
// the sentinel strings "str", "int", "float", "bool", "null".
//
// v is expected to be the result of json.Unmarshal into any. Numeric
// values arrive as float64 and are tagged "int" or "float" by their
// integer-ness.
func Shape(v any, opts ShapeOpts) any {
	return shape(v, 0, opts)
}

func shape(v any, depth int, opts ShapeOpts) any {
	if depth > maxDepth {
		return "..."
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = shape(vv, depth+1, opts)
		}
		return out
	case []any:
		if len(t) == 0 {
			return []any{"<empty>"}
		}
		var merged any
		n := len(t)
		if n > 5 {
			n = 5
		}
		for i := 0; i < n; i++ {
			merged = mergeShapes(merged, shape(t[i], depth+1, opts))
		}
		countMarker := "<n=" + strconv.Itoa(len(t)) + ">"
		if opts.AnonymizeCounts {
			countMarker = "<n=*>"
		}
		return []any{merged, countMarker}
	case bool:
		return "bool"
	case float64:
		// encoding/json decodes JSON numbers as float64. Tag by
		// integer-ness so "id":42 and "ratio":3.14 stay distinct.
		if t == float64(int64(t)) {
			return "int"
		}
		return "float"
	case string:
		return "str"
	case nil:
		return "null"
	default:
		return reflect.TypeOf(v).String()
	}
}

// isWildcard reports whether a shape carries no type information:
// an empty array element ("<empty>") or a JSON null. Either may
// stand in for any real shape, because which one a feed shows
// depends on the content of the week.
func isWildcard(v any) bool {
	s, ok := v.(string)
	return ok && (s == "<empty>" || s == "null")
}

// mergeShapes unions two shapes. Wildcards yield to the other side,
// maps merge key by key (recursively), arrays merge their element
// shape and keep a's count marker; otherwise a wins.
func mergeShapes(a, b any) any {
	if a == nil || isWildcard(a) {
		if b == nil {
			return a
		}
		return b
	}
	if b == nil || isWildcard(b) {
		return a
	}
	switch ta := a.(type) {
	case map[string]any:
		tb, ok := b.(map[string]any)
		if !ok {
			return a
		}
		out := make(map[string]any, len(ta))
		for k, v := range ta {
			out[k] = v
		}
		for k, v := range tb {
			if cur, exists := out[k]; exists {
				out[k] = mergeShapes(cur, v)
			} else {
				out[k] = v
			}
		}
		return out
	case []any:
		tb, ok := b.([]any)
		if !ok || len(ta) == 0 || len(tb) == 0 {
			return a
		}
		if len(ta) == 1 && isWildcard(ta[0]) {
			// a saw no elements, so it has no count marker; b whole.
			return b
		}
		out := append([]any{mergeShapes(ta[0], tb[0])}, ta[1:]...)
		return out
	}
	return a
}

// Diff returns a human-readable list of differences between two
// signatures. Empty slice means a and b are identical.
//
// Output order is deterministic: removed keys first (sorted), then
// added keys (sorted), then recursive descent into shared keys
// (sorted). "<empty>" and "null" shapes match anything. Type changes at the leaf surface as `path: a -> b`.
func Diff(a, b any) []string {
	return diffShapes(a, b, "")
}

func diffShapes(a, b any, path string) []string {
	if isWildcard(a) || isWildcard(b) {
		return nil
	}
	if reflect.TypeOf(a) != reflect.TypeOf(b) {
		p := path
		if p == "" {
			p = "."
		}
		return []string{fmt.Sprintf("%s: type %T -> %T", p, a, b)}
	}
	switch ta := a.(type) {
	case map[string]any:
		tb := b.(map[string]any)
		return diffMaps(ta, tb, path)
	case []any:
		tb := b.([]any)
		if len(ta) == 0 || len(tb) == 0 {
			return nil
		}
		return diffShapes(ta[0], tb[0], path+"[]")
	default:
		if !reflect.DeepEqual(a, b) {
			return []string{fmt.Sprintf("%s: %v -> %v", path, jsonish(a), jsonish(b))}
		}
		return nil
	}
}

func diffMaps(a, b map[string]any, path string) []string {
	var out []string
	var removed, added, common []string
	for k := range a {
		if _, ok := b[k]; ok {
			common = append(common, k)
		} else {
			removed = append(removed, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			added = append(added, k)
		}
	}
	sort.Strings(removed)
	sort.Strings(added)
	sort.Strings(common)
	for _, k := range removed {
		out = append(out, fmt.Sprintf("%s/%s: removed", path, k))
	}
	for _, k := range added {
		out = append(out, fmt.Sprintf("%s/%s: added", path, k))
	}
	for _, k := range common {
		out = append(out, diffShapes(a[k], b[k], path+"/"+k)...)
	}
	return out
}

// jsonish renders leaf values for diff output.
func jsonish(v any) string {
	switch t := v.(type) {
	case string:
		return strconv.Quote(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}
