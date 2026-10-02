package main

import (
	"testing"

	"gitlab.com/dunn.dev/bairn/internal/safehttp/safehttptest"
)

func TestSmokeHTTPClientRedirectPolicy(t *testing.T) {
	safehttptest.AssertPolicy(t, smokeHTTPClient())
}
