// Package safehttp holds the redirect policy for every outbound client
// that carries a credential. net/http replays custom auth headers and
// request bodies across redirects, so a hostile or misconfigured
// upstream could otherwise move a token or password to another host or
// onto cleartext.
package safehttp

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// MaxRedirects caps the redirect chain length.
const MaxRedirects = 10

// CheckRedirect refuses a redirect to a different host or from https to
// http, and stops chains longer than MaxRedirects. It is an
// http.Client.CheckRedirect.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return fmt.Errorf("refusing redirect: more than %d redirects", MaxRedirects)
	}
	if len(via) == 0 {
		return nil
	}
	first := via[0].URL
	if req.URL.Host != first.Host {
		return errors.New("refusing cross-host redirect")
	}
	if first.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("refusing https to http redirect")
	}
	return nil
}

// NewClient returns an http.Client with the given timeout and the
// CheckRedirect policy above.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: CheckRedirect}
}
