package drift

import (
	"reflect"
	"strings"
)

// Filter returns raw with any keys not present in schema (via json
// tags) removed. Walks structs, slices, and pointers; primitives and
// custom-unmarshalled types pass through unchanged.
//
// schema is the reflect.Type of a Go struct (typically obtained via
// reflect.TypeOf(YourStruct{})). When schema is nil, raw is returned
// unchanged.
//
// The intent is to scope shape signatures to "fields bairn's typed
// decoder reads," dropping vendor-side keys bairn ignores at decode
// time. Endpoints without a registered schema receive the full
// vendor shape, which is the right default for ad-hoc operator
// endpoints in manifest.local.toml.
func Filter(raw any, schema reflect.Type) any {
	return filter(raw, schema)
}

func filter(raw any, t reflect.Type) any {
	if raw == nil || t == nil {
		return raw
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := raw.(map[string]any)
		if !ok {
			// Custom-unmarshalled or scalar wire value
			// (e.g. FamlyTime decodes a JSON string into a Go
			// struct via UnmarshalJSON). Preserve raw shape.
			return raw
		}
		return filterStruct(m, t)
	case reflect.Slice, reflect.Array:
		arr, ok := raw.([]any)
		if !ok {
			return raw
		}
		elem := t.Elem()
		out := make([]any, len(arr))
		for i, v := range arr {
			out[i] = filter(v, elem)
		}
		return out
	default:
		return raw
	}
}

func filterStruct(raw map[string]any, t reflect.Type) any {
	out := make(map[string]any)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, ok := jsonFieldName(f)
		if !ok {
			continue
		}
		v, present := raw[name]
		if !present {
			continue
		}
		out[name] = filter(v, f.Type)
	}
	return out
}

// jsonFieldName returns the wire key encoding/json would use for f,
// plus false if f is excluded by a "-" tag. Mirrors encoding/json's
// fallback to the Go field name when no tag is present. Anonymous
// embedded fields are not flattened; bairn's famly types do not rely
// on embedded-field promotion at the JSON layer.
func jsonFieldName(f reflect.StructField) (string, bool) {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return f.Name, true
	}
	if tag == "-" {
		return "", false
	}
	if i := strings.IndexByte(tag, ','); i >= 0 {
		tag = tag[:i]
	}
	if tag == "" {
		return f.Name, true
	}
	return tag, true
}
