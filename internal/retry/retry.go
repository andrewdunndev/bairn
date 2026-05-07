// Package retry wraps cenkalti/backoff/v5 with bairn-specific
// defaults and an HTTP-aware predicate.
//
// We don't reinvent the backoff math; we add only the policy that
// makes sense for our two operations (HTTP requests and file
// writes).
package retry

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v5"
)

// DefaultExponential returns a sensible policy for transient
// failures: 250ms initial, factor 2, capped at 30s, with 0.2 jitter.
// Suitable for both HTTP and filesystem retries.
func DefaultExponential() *backoff.ExponentialBackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 250 * time.Millisecond
	b.MaxInterval = 30 * time.Second
	b.RandomizationFactor = 0.2
	b.Multiplier = 2.0
	return b
}

// Do retries op with exponential backoff up to MaxTries attempts.
// Errors wrapped with backoff.Permanent stop retries immediately.
// Errors created via backoff.RetryAfter(seconds) signal a specific
// next-attempt delay (used for HTTP 429).
func Do[T any](ctx context.Context, op func() (T, error), opts ...backoff.RetryOption) (T, error) {
	defaults := []backoff.RetryOption{
		backoff.WithBackOff(DefaultExponential()),
		backoff.WithMaxTries(5),
		backoff.WithMaxElapsedTime(2 * time.Minute),
	}
	return backoff.Retry(ctx, op, append(defaults, opts...)...)
}

// HTTPDo runs an HTTP operation with retry semantics tuned to a
// vendor surface: do not retry 4xx auth errors (the caller should
// refresh credentials), do retry 5xx and connection-reset, honour
// Retry-After on 429.
//
// The op closure must produce a fresh *http.Request internally; we
// can't safely reuse a Request whose Body has been consumed.
func HTTPDo(ctx context.Context, op func(context.Context) (*http.Response, error), opts ...backoff.RetryOption) (*http.Response, error) {
	wrapped := func() (*http.Response, error) {
		resp, err := op(ctx)
		if err != nil {
			// Network errors are transient by default. Caller can
			// wrap with Permanent if they know better.
			return nil, err
		}
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return resp, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			// Honour Retry-After if present; default to a moderate
			// retry otherwise.
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			_ = resp.Body.Close()
			if retryAfter > 0 {
				return nil, backoff.RetryAfter(retryAfter)
			}
			return nil, fmt.Errorf("429 Too Many Requests")
		case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode >= 500:
			body := drainTo(resp, 256)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
		default:
			// 4xx other than 408/429: don't retry.
			body := drainTo(resp, 256)
			_ = resp.Body.Close()
			return nil, backoff.Permanent(fmt.Errorf("HTTP %d: %s", resp.StatusCode, body))
		}
	}
	return Do(ctx, wrapped, opts...)
}

// parseRetryAfter accepts both seconds-as-int and HTTP-date forms.
// Returns 0 if unparseable; caller should fall back to backoff.
func parseRetryAfter(v string) int {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return n
	}
	if t, err := http.ParseTime(v); err == nil {
		s := int(time.Until(t).Seconds())
		if s > 0 {
			return s
		}
	}
	return 0
}

// drainTo reads up to limit bytes from the response body for
// inclusion in error messages. Best-effort.
func drainTo(resp *http.Response, limit int64) string {
	if resp.Body == nil {
		return ""
	}
	buf := make([]byte, limit)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}
