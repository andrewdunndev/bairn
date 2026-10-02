package contract

import (
	"testing"

	"gitlab.com/dunn.dev/bairn/internal/safehttp/safehttptest"
)

func TestDefaultClientRedirectPolicy(t *testing.T) {
	safehttptest.AssertPolicy(t, defaultClient())
}
