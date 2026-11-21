package safehttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// Download client timeouts. There is deliberately no total timeout: a
// large video on a slow link may legitimately take a long time, so a
// body is only abandoned when no bytes arrive for DownloadIdleTimeout.
const (
	DownloadDialTimeout   = 30 * time.Second
	DownloadTLSTimeout    = 15 * time.Second
	DownloadHeaderTimeout = 60 * time.Second
	DownloadIdleTimeout   = 90 * time.Second
)

// ErrIdle is returned by a response body read when the server sent no
// bytes for the idle window.
var ErrIdle = errors.New("download stalled: no data received within the idle timeout")

// NewDownloadClient returns a client for streaming media downloads: connect, TLS
// and response-header timeouts, an idle-read deadline on the body, and
// the CheckRedirect policy. It carries no credential, but a CDN link
// has no reason to leave its host or drop to cleartext.
func NewDownloadClient() *http.Client {
	return newDownloadClient(DownloadDialTimeout, DownloadTLSTimeout, DownloadHeaderTimeout, DownloadIdleTimeout)
}

func newDownloadClient(dial, tlsT, header, idle time.Duration) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = (&net.Dialer{Timeout: dial, KeepAlive: 30 * time.Second}).DialContext
	base.TLSHandshakeTimeout = tlsT
	base.ResponseHeaderTimeout = header
	return &http.Client{
		Transport:     &idleTransport{base: base, idle: idle},
		CheckRedirect: CheckRedirect,
	}
}

// idleTransport cancels a request whose body goes quiet for idle.
type idleTransport struct {
	base http.RoundTripper
	idle time.Duration
}

// The idle timer starts before the round trip, so the idle value also
// caps dial, TLS and response headers (never longer than their own
// limits sum to); between reads, time spent on a slow local write counts
// as idle.
func (t *idleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	timer := time.AfterFunc(t.idle, func() { cancel(ErrIdle) })
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		timer.Stop()
		cancel(err)
		if errors.Is(context.Cause(ctx), ErrIdle) {
			err = ErrIdle
		}
		return nil, err
	}
	timer.Reset(t.idle)
	resp.Body = &idleBody{rc: resp.Body, timer: timer, idle: t.idle, ctx: ctx, cancel: cancel}
	return resp, nil
}

type idleBody struct {
	rc     io.ReadCloser
	timer  *time.Timer
	idle   time.Duration
	ctx    context.Context
	cancel context.CancelCauseFunc
	once   sync.Once
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.timer.Reset(b.idle)
	}
	if err != nil && err != io.EOF && errors.Is(context.Cause(b.ctx), ErrIdle) {
		err = ErrIdle
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.once.Do(func() { b.timer.Stop() })
	err := b.rc.Close()
	b.cancel(nil)
	return err
}
