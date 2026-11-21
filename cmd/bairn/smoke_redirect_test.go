package main

import (
	"testing"

	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/safehttp/safehttptest"
)

func TestSmokeHTTPClientRedirectPolicy(t *testing.T) {
	safehttptest.AssertPolicy(t, smokeHTTPClient())
}

func TestResolveSmokeImmichConfigTrimsAPISuffix(t *testing.T) {
	for _, in := range []string{"https://h.test", "https://h.test/", "https://h.test/api", "https://h.test/api/"} {
		if got := resolveSmokeImmichConfig(&config.Config{}, in, "", "", "").host; got != "https://h.test" {
			t.Errorf("host %q -> %q", in, got)
		}
	}
}
