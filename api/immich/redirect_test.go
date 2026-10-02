package immich

import (
	"testing"

	"gitlab.com/dunn.dev/bairn/internal/safehttp/safehttptest"
)

func TestRedirectPolicy(t *testing.T) {
	safehttptest.AssertPolicy(t, New("https://immich.example.test", "fake-key").httpClient)
}
