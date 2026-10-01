package main

import (
	"testing"
	"time"
)

func TestResolveZone(t *testing.T) {
	if z, err := resolveZone(""); err != nil || z != time.Local {
		t.Errorf("empty = %v, %v; want time.Local", z, err)
	}
	z, err := resolveZone("America/Detroit")
	if err != nil || z.String() != "America/Detroit" {
		t.Errorf("Detroit = %v, %v", z, err)
	}
	if _, err := resolveZone("Not/AZone"); err == nil {
		t.Error("bad zone accepted")
	}
}
