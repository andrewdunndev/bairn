package famly

import (
	"testing"

	"gitlab.com/dunn.dev/bairn/internal/safehttp/safehttptest"
)

func TestRedirectPolicy(t *testing.T) {
	t.Run("client", func(t *testing.T) {
		safehttptest.AssertPolicy(t, New(nil).httpClient)
	})
	t.Run("login", func(t *testing.T) {
		safehttptest.AssertPolicy(t, loginHTTPClient())
	})
}
