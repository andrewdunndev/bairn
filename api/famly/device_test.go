package famly

import (
	"regexp"
	"testing"
)

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestDeriveDeviceIDUUIDShape(t *testing.T) {
	id := DeriveDeviceID()
	if !uuidShape.MatchString(id) {
		t.Errorf("DeriveDeviceID() = %q, expected UUID 8-4-4-4-12 hex", id)
	}
}

func TestDeriveDeviceIDStable(t *testing.T) {
	a := DeriveDeviceID()
	b := DeriveDeviceID()
	if a != b {
		t.Errorf("DeriveDeviceID is not stable across calls: %q vs %q", a, b)
	}
}
