package famly

import (
	"crypto/rand"
	"fmt"
	"regexp"
)

// The vendor's GraphQL surface validates DeviceId as a UUID; an
// arbitrary string like "bairn-default-device" is rejected with
// 400 "Error during variable coercion."
var deviceIDShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidDeviceID reports whether s has the UUID shape (8-4-4-4-12 hex)
// Famly's DeviceId scalar accepts.
func ValidDeviceID(s string) bool { return deviceIDShape.MatchString(s) }

// NewDeviceID returns a random UUIDv4 for Famly's `DeviceId` scalar.
// It is generated once and then stored by the caller: a value derived
// from the host (a MAC, say) is not stable in containers and CI, where
// it changes on every run and Famly reports a new device each time.
func NewDeviceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("famly: generate device id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
