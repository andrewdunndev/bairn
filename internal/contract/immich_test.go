package contract

import (
	"reflect"
	"testing"
)

func TestFieldNameFromMessage(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// class-validator format Andy reported on Immich v2.7.5
		{"deviceAssetId must be a string", "deviceAssetId"},
		{"deviceAssetId should not be empty", "deviceAssetId"},
		{"deviceId must be a string", "deviceId"},
		{"fileCreatedAt must be a date string", "fileCreatedAt"},
		// nested field paths
		{"metadata.0.value must be an object", "metadata.0.value"},
		// trailing punctuation
		{"foo, must be present", "foo"},
		// empty / unknown
		{"", ""},
		{"  ", ""},
		// non-camelCase leading token (defensive: don't grab "Bad")
		{"Bad Request", "Bad"},
		{"123 numeric leader", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := fieldNameFromMessage(c.in)
			if got != c.want {
				t.Errorf("fieldNameFromMessage(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestExtractFieldNames_ClassValidator(t *testing.T) {
	// Exact shape Andy reported.
	in := []any{
		"deviceAssetId must be a string",
		"deviceAssetId should not be empty",
		"deviceId must be a string",
		"deviceId should not be empty",
	}
	got, err := extractFieldNames(in)
	if err != nil {
		t.Fatalf("extractFieldNames: %v", err)
	}
	want := []string{"deviceAssetId", "deviceId"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExtractFieldNames_DeduplicatesAndSorts(t *testing.T) {
	in := []any{
		"zeta must be a string",
		"alpha should not be empty",
		"alpha must be a string",
		"mu must be a number",
	}
	got, err := extractFieldNames(in)
	if err != nil {
		t.Fatalf("extractFieldNames: %v", err)
	}
	want := []string{"alpha", "mu", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExtractFieldNames_ZodObjectShape(t *testing.T) {
	// Zod's error responses use a different shape: array of objects
	// with `path` (array) + `message` (string). Defensive parsing
	// handles both since the error format may move with Immich
	// versions.
	in := []any{
		map[string]any{
			"path":    []any{"fileCreatedAt"},
			"message": "Required",
		},
		map[string]any{
			"path":    []any{"metadata", float64(0), "value"},
			"message": "Expected object, received string",
		},
	}
	got, err := extractFieldNames(in)
	if err != nil {
		t.Fatalf("extractFieldNames: %v", err)
	}
	// fileCreatedAt comes from path[0]; metadata likewise.
	want := []string{"fileCreatedAt", "metadata"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExtractFieldNames_SingleString(t *testing.T) {
	got, err := extractFieldNames("fileCreatedAt is required")
	if err != nil {
		t.Fatalf("extractFieldNames: %v", err)
	}
	want := []string{"fileCreatedAt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExtractFieldNames_RejectsUnknownShape(t *testing.T) {
	_, err := extractFieldNames(map[string]any{"unexpected": true})
	if err == nil {
		t.Error("expected error on unrecognised shape, got nil")
	}
}

func TestBuildMinimalUploadProbe(t *testing.T) {
	body, contentType, err := buildMinimalUploadProbe()
	if err != nil {
		t.Fatalf("buildMinimalUploadProbe: %v", err)
	}
	if contentType == "" {
		t.Error("contentType empty")
	}
	if body == nil {
		t.Error("body nil")
	}
}
