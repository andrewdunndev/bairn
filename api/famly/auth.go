package famly

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// TokenSource produces a session token on demand. Implementations
// may return a static token or refresh from email+password
// credentials when previous attempts failed.
//
// Token must be safe for concurrent use.
type TokenSource interface {
	// Token returns a usable session token. Implementations should
	// surface refresh failures via the returned error, including a
	// next-action hint in the message.
	Token(ctx context.Context) (string, error)

	// Invalidate is called by Client when a token is rejected
	// (HTTP 401). Implementations that support refresh can use this
	// signal to drop the cached token and re-authenticate on the
	// next Token call. Static implementations may no-op.
	Invalidate()
}

// StaticToken is a TokenSource that returns the same token on every
// call. It does not refresh; on Invalidate it remembers the
// invalidation so the next Token call returns ErrTokenExpired with
// guidance.
type StaticToken struct {
	value      string
	invalid    bool
	mu         sync.Mutex
}

// NewStaticToken constructs a StaticToken from a session string.
// Suitable for short-lived runs and tests; production cron should
// prefer RefreshingToken.
func NewStaticToken(value string) *StaticToken {
	return &StaticToken{value: value}
}

// Token returns the configured token unless it has been invalidated.
func (s *StaticToken) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invalid {
		return "", ErrTokenExpired
	}
	return s.value, nil
}

// Invalidate records that the token was rejected. Subsequent Token
// calls return ErrTokenExpired.
func (s *StaticToken) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalid = true
}

// ErrTokenExpired indicates the static token was rejected and
// cannot be refreshed without credentials. The error message
// includes an actionable next step for the operator.
var ErrTokenExpired = errors.New(
	"famly: session token rejected; refresh by setting FAMLY_EMAIL and " +
		"FAMLY_PASSWORD or by pasting a fresh token into FAMLY_ACCESS_TOKEN",
)

// RefreshingToken is a TokenSource backed by email + password. It
// caches a session token in memory and refreshes via the
// Authenticate GraphQL mutation when invalidated.
//
// The newly minted token is never persisted to disk; cron runs are
// short, and persisting a refreshed token introduces a stale-cache
// failure mode that costs more than it saves.
type RefreshingToken struct {
	email    string
	password string
	deviceID string
	login    func(ctx context.Context, email, password, deviceID string) (string, error)

	mu     sync.Mutex
	cached string
}

// NewRefreshingToken constructs a RefreshingToken. login is the
// callback that exchanges credentials for a token; in production
// this calls the Famly Authenticate mutation. Tests inject a stub.
func NewRefreshingToken(email, password, deviceID string, login func(ctx context.Context, email, password, deviceID string) (string, error)) *RefreshingToken {
	return &RefreshingToken{
		email:    email,
		password: password,
		deviceID: deviceID,
		login:    login,
	}
}

// Token returns a cached token, refreshing if absent.
func (r *RefreshingToken) Token(ctx context.Context) (string, error) {
	r.mu.Lock()
	if r.cached != "" {
		t := r.cached
		r.mu.Unlock()
		return t, nil
	}
	r.mu.Unlock()

	tok, err := r.login(ctx, r.email, r.password, r.deviceID)
	if err != nil {
		return "", fmt.Errorf("famly: refresh token: %w", err)
	}
	r.mu.Lock()
	r.cached = tok
	r.mu.Unlock()
	return tok, nil
}

// Invalidate drops the cached token so the next Token call refreshes.
func (r *RefreshingToken) Invalidate() {
	r.mu.Lock()
	r.cached = ""
	r.mu.Unlock()
}
