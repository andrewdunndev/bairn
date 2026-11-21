package famly

import "testing"

func TestNewDeviceIDIsUUIDv4AndDistinct(t *testing.T) {
	a, err := NewDeviceID()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewDeviceID()
	if !ValidDeviceID(a) || a[14] != '4' || (a[19] != '8' && a[19] != '9' && a[19] != 'a' && a[19] != 'b') {
		t.Errorf("NewDeviceID() = %q, want a UUIDv4", a)
	}
	if a == b {
		t.Errorf("two ids equal: %q", a)
	}
}

func TestValidDeviceID(t *testing.T) {
	for s, want := range map[string]bool{
		"0b6f3c1e-9d2a-4c1b-8f3e-1a2b3c4d5e6f": true,
		"bairn-default-device":                 false,
		"":                                     false,
		"0b6f3c1e9d2a4c1b8f3e1a2b3c4d5e6f":     false,
	} {
		if got := ValidDeviceID(s); got != want {
			t.Errorf("ValidDeviceID(%q) = %v, want %v", s, got, want)
		}
	}
}
