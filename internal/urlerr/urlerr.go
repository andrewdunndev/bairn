// Package urlerr strips the query string from errors that carry a
// request URL. net/http wraps transport failures in *url.Error, whose
// text includes the full URL; a signed CDN link would then reach the
// log and the state file.
package urlerr

import (
	"errors"
	"net/url"
	"strings"
)

// Redact returns err with the query string removed from any *url.Error
// in its chain. Other errors are returned unchanged.
func Redact(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	clean := *ue
	if i := strings.IndexByte(clean.URL, '?'); i >= 0 {
		clean.URL = clean.URL[:i] + "?<redacted>"
	}
	return &clean
}
