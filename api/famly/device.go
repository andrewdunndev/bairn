package famly

import (
	"crypto/md5"
	"fmt"
	"net"
	"os"
)

// DeriveDeviceID returns a stable, per-host UUID-shaped string
// suitable for Famly's `DeviceId` scalar.
//
// The vendor's GraphQL surface validates DeviceId as a UUID; an
// arbitrary string like "bairn-default-device" is rejected with
// 400 "Error during variable coercion." We derive a deterministic
// UUID from the host's MAC address (or hostname as fallback) so
// the same machine sends the same DeviceId across runs without
// requiring the operator to configure one.
//
// Matches jacobbunk/famly-fetch's approach for compatibility:
// MD5(MAC) formatted as 8-4-4-4-12 hex.
func DeriveDeviceID() string {
	seed := hostSeed()
	d := md5.Sum(seed)
	return fmt.Sprintf("%x-%x-%x-%x-%x", d[0:4], d[4:6], d[6:8], d[8:10], d[10:16])
}

// hostSeed returns a stable byte string identifying this host.
// Prefers the first non-loopback MAC address; falls back to the
// hostname when interfaces can't be enumerated (containers, etc.).
func hostSeed() []byte {
	if ifaces, err := net.Interfaces(); err == nil {
		for _, ifc := range ifaces {
			if ifc.Flags&net.FlagLoopback != 0 {
				continue
			}
			if len(ifc.HardwareAddr) >= 6 {
				return ifc.HardwareAddr
			}
		}
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "bairn-unknown-host"
	}
	return []byte(host)
}
