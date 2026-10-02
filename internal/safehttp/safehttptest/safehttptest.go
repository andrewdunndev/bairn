// Package safehttptest asserts that an http.Client enforces the
// safehttp redirect policy, so each credential-carrying client
// constructor can be checked with one call.
package safehttptest

import (
	"net/http"
	"testing"
)

// AssertPolicy fails t unless c refuses cross-host redirects, https to
// http downgrades and chains past the cap, while allowing a same-host
// https hop.
func AssertPolicy(t *testing.T, c *http.Client) {
	t.Helper()
	if c == nil || c.CheckRedirect == nil {
		t.Fatal("client has no CheckRedirect")
	}
	mk := func(u string) *http.Request {
		r, err := http.NewRequest("GET", u, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := mk("https://api.example.test/a")
	via := []*http.Request{first}
	if err := c.CheckRedirect(mk("https://api.example.test/b"), via); err != nil {
		t.Errorf("same-host https refused: %v", err)
	}
	if err := c.CheckRedirect(mk("https://evil.example.test/b"), via); err == nil {
		t.Error("cross-host redirect allowed")
	}
	if err := c.CheckRedirect(mk("http://api.example.test/b"), via); err == nil {
		t.Error("https to http downgrade allowed")
	}
	long := make([]*http.Request, 10)
	for i := range long {
		long[i] = first
	}
	if err := c.CheckRedirect(mk("https://api.example.test/b"), long); err == nil {
		t.Error("redirect cap not enforced")
	}
}
