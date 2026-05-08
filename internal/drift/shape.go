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

// Shape returns a deterministic JSON-key-only signature for v.
// Maps preserve their key set; arrays become a [merged-shape, "<n=N>"]
// pair representing a union of the first 5 elements; primitives become
// the sentinel strings "str", "int", "float", "bool", "null".
//
// v is expected to be the result of json.Unmarshal into any. Numeric
// values arrive as float64 and are tagged "int" or "float" by their
// integer-ness.
func Shape(v any) any {
	return shape(v, 0)
}

func shape(v any, depth int) any {
	if depth > maxDepth {
		return "..."
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = shape(vv, depth+1)
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
			sh := shape(t[i], depth+1)
			if shMap, ok := sh.(map[string]any); ok {
				if merged == nil {
					merged = copyMap(shMap)
				} else if mergedMap, ok := merged.(map[string]any); ok {
					for k, vv := range shMap {
						if _, exists := mergedMap[k]; !exists {
							mergedMap[k] = vv
						}
					}
				}
			} else {
				merged = sh
				break
			}
		}
		return []any{merged, "<n=" + strconv.Itoa(len(t)) + ">"}
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

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Diff returns a human-readable list of differences between two
// signatures. Empty slice means a and b are identical.
//
// Output order is deterministic: removed keys first (sorted), then
// added keys (sorted), then recursive descent into shared keys
// (sorted). Type changes at the leaf surface as `path: a -> b`.
func Diff(a, b any) []string {
	return diffShapes(a, b, "")
}

func diffShapes(a, b any, path string) []string {
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
