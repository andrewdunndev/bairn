package immich

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"testing"
	"time"
)

// TestUploadEmitsAllRequiredFields is the static gate that would
// have caught the v0.4.3 device-field regression.
//
// required-fields.json is a vendored manifest captured by the
// operator running `make refresh-immich-validator` against a live
// Immich. It records exactly which fields the live validator
// rejected as missing. This test asserts buildUploadBody emits a
// multipart field for each required name.
//
// Refresh cadence: alongside `make refresh-immich-spec` (operator
// action). When upstream Immich changes its required-field set,
// the operator captures a new manifest, and this test fails until
// buildUploadBody is updated to match.
//
// Source-of-truth chain:
//
//	upstream Immich validator (controller-layer + zod, partial)
//	  → live probe via internal/contract.ProbeImmichUploadRequiredFields
//	  → vendored manifest at api/immich/required-fields.json
//	  → this test
//	  → buildUploadBody must emit each required field
func TestUploadEmitsAllRequiredFields(t *testing.T) {
	data, err := os.ReadFile("required-fields.json")
	if err != nil {
		// Skip rather than fail when the manifest is absent. A fresh
		// clone or a fork that hasn't run refresh-immich-validator
		// shouldn't see a red test for "you didn't run this".
		// Operator-side enforcement: don't tag a release without
		// the manifest committed.
		t.Skipf("required-fields.json not found; run `make refresh-immich-validator` to capture")
	}

	var manifest struct {
		ImmichVersion string   `json:"immich_version"`
		CapturedAt    string   `json:"captured_at"`
		Endpoint      string   `json:"endpoint"`
		Required      []string `json:"required"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("required-fields.json: %v", err)
	}
	if len(manifest.Required) == 0 {
		t.Skip("required-fields.json has no required fields; nothing to assert")
	}

	emitted := emittedUploadFields(t)
	for _, req := range manifest.Required {
		if !emitted[req] {
			t.Errorf("Immich (server %s, captured %s) requires field %q on POST /api/assets; buildUploadBody does not emit it",
				manifest.ImmichVersion, manifest.CapturedAt, req)
		}
	}
}

// emittedUploadFields builds a representative upload via
// buildUploadBody, parses the multipart, and returns the set of
// field names bairn writes. Used by both the contract test and
// downstream operator-facing diagnostics.
func emittedUploadFields(t *testing.T) map[string]bool {
	t.Helper()

	body, contentType, err := buildUploadBody(UploadInput{
		Data:           []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'},
		Filename:       "contract-test.jpg",
		FileCreatedAt:  time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC),
		FileModifiedAt: time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC),
		DeviceID:       "bairn",
		DeviceAssetID:  "test",
		Metadata:       map[string]string{"famlyImageId": "test"},
	})
	if err != nil {
		t.Fatalf("buildUploadBody: %v", err)
	}

	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse content-type: %v", err)
	}
	mr := multipart.NewReader(body, params["boundary"])
	out := map[string]bool{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("multipart part: %v", err)
		}
		out[p.FormName()] = true
		_, _ = io.Copy(io.Discard, p)
	}
	return out
}
