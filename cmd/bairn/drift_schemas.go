package main

import (
	"reflect"

	"gitlab.com/dunn.dev/bairn/api/famly"
)

// driftSchemas binds drift manifest endpoint ids to bairn's typed
// response structs. The drift command filters response payloads to
// keys present in these structs (via json tags) before computing
// shape signatures, so committed baselines only describe fields
// bairn actually decodes.
//
// To register a new endpoint:
//  1. Add it to discovery/probe/manifest.toml.
//  2. Bind the response struct here.
//  3. Re-seed: `bairn drift --anonymize --out-dir discovery/baselines/main`
//
// Endpoints not present here receive the full vendor shape. That is
// the right default for ad-hoc operator endpoints in
// manifest.local.toml; it is the wrong default for the committed
// manifest, which should always have a binding.
var driftSchemas = map[string]reflect.Type{
	"me":         reflect.TypeOf(famly.Me{}),
	"feed-page1": reflect.TypeOf(famly.FeedPage{}),
}
