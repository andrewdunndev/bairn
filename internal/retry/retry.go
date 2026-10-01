// Package retry is bairn's one HTTP retry policy, shared by the Famly
// and Immich clients.
//
// A request is retried on transport errors, 408, 429 and 5xx, with
// exponential backoff and jitter, up to Policy.Attempts tries. A
// Retry-After header on 429 or 503 sets the wait instead of the
// backoff. Every other status, 4xx included, is returned to the
// caller untouched; auth refresh is the caller's concern.
package retry

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// Policy bounds the retries. The zero value is not useful; use
// Default.
type Policy struct {
	Attempts int           // total tries including the first
	Initial  time.Duration // first backoff, doubled each retry
	Max      time.Duration // cap on any single wait, Retry-After included
	Sleep    func(context.Context, time.Duration) error
}

// Default is 5 tries, 500ms doubling to a 30s cap. Roughly a minute
// of patience per request in the worst case.
func Default() Policy {
	return Policy{Attempts: 5, Initial: 500 * time.Millisecond, Max: 30 * time.Second, Sleep: Sleep}
}

// Sleep waits d or until ctx is done.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Do calls send until it yields a response that is not retryable or
// the attempts run out. send must build a fresh request (and body)
// on every call. Intermediate responses are closed. When attempts
// run out on a retryable status the last response is returned
// unread, so the caller reports it like any other failure; when
// they run out on a transport error that error is returned.
func (p Policy) Do(ctx context.Context, send func() (*http.Response, error)) (*http.Response, error) {
	if p.Sleep == nil {
		p.Sleep = Sleep
	}
	wait := p.Initial
	for attempt := 1; ; attempt++ {
		resp, err := send()
		if err == nil && !retryable(resp.StatusCode) {
			return resp, nil
		}
		if ctx.Err() != nil {
			if resp != nil {
				resp.Body.Close()
			}
			return nil, ctx.Err()
		}
		if attempt >= p.Attempts {
			return resp, err
		}
		d := jitter(wait)
		if resp != nil {
			if ra, ok := retryAfter(resp.Header.Get("Retry-After")); ok {
				d = ra
			}
			resp.Body.Close()
		}
		if err := p.Sleep(ctx, min(d, p.Max)); err != nil {
			return nil, err
		}
		wait = min(wait*2, p.Max)
	}
}

func retryable(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

// jitter spreads d across +-20% so parallel callers do not align.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// retryAfter parses seconds or an HTTP date.
func retryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0), true
	}
	return 0, false
}
