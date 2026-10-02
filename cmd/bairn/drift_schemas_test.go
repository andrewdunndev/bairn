package main

import (
	"testing"

	"gitlab.com/dunn.dev/bairn/internal/drift"
)

// An endpoint without a schema binding emits the raw vendor shape to
// the public baseline log.
func TestCommittedManifestEndpointsHaveSchemas(t *testing.T) {
	m, err := drift.LoadManifest("../../discovery/probe/manifest.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Endpoints) == 0 {
		t.Fatal("manifest has no endpoints")
	}
	for _, ep := range m.Endpoints {
		if driftSchemas[ep.ID] == nil {
			t.Errorf("endpoint %q has no entry in driftSchemas", ep.ID)
		}
	}
}
