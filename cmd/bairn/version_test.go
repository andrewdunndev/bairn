package main

import "testing"

func TestSoftware(t *testing.T) {
	tests := map[string]string{
		"dev":                  "bairn dev",
		"v0.6.0":               "bairn 0.6.0",
		"0.6.0":                "bairn 0.6.0",
		"v0.5.0-3-gabc1-dirty": "bairn 0.5.0-3-gabc1-dirty",
		"abc1234":              "bairn abc1234",
	}
	for in, want := range tests {
		if got := software(in); got != want {
			t.Errorf("software(%q) = %q, want %q", in, got, want)
		}
	}
	if software(Version) != "bairn dev" {
		t.Errorf("default Version should read dev, got %q", Version)
	}
}
