package drift

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"
)

// ProbeResult captures one endpoint's result from a probe run.
type ProbeResult struct {
	ID        string
	Status    int
	BodySize  int
	Signature any      // nil if Error is set or response wasn't JSON
	NotJSON   bool     // true if response did not parse as JSON
	Drift     []string // populated only when ProbeOptions.Compare returns a prior signature
	Error     string   // populated if request failed
}

// ProbeOptions configures Probe.
type ProbeOptions struct {
	HTTPClient *http.Client
	Token      string                      // overrides auth_env when set
	Logger     *slog.Logger
	Compare    func(id string) (any, bool) // returns prior signature if available
	Sleep      func(d time.Duration)       // injected for tests; defaults to time.Sleep
	Shape      ShapeOpts                   // forwarded to Shape() for each response body
	// Schemas binds endpoint ids to Go struct types. When an
	// endpoint has an entry, its response is filtered to keys
	// declared in the struct (via json tags) before shape is
	// computed. Endpoints without an entry receive the full
	// vendor shape (the right default for ad-hoc operator
	// endpoints in manifest.local.toml).
	Schemas map[string]reflect.Type
}

// Probe runs every endpoint in m in order and returns one
// ProbeResult per endpoint.
func Probe(ctx context.Context, m *Manifest, opts ProbeOptions) ([]ProbeResult, error) {
	if opts.Sleep == nil {
		opts.Sleep = time.Sleep
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	token := opts.Token
	if token == "" && m.AuthEnv != "" {
		token = os.Getenv(m.AuthEnv)
	}
	if token == "" {
		return nil, fmt.Errorf("auth token not available (env %s unset)", m.AuthEnv)
	}

	delay := time.Duration(m.DelaySec * float64(time.Second))
	ua := m.UserAgent
	if ua == "" {
		ua = "bairn-drift/0.0"
	}

	results := make([]ProbeResult, 0, len(m.Endpoints))
	for i, ep := range m.Endpoints {
		if i > 0 && delay > 0 {
			opts.Sleep(delay)
		}
		var schema reflect.Type
		if opts.Schemas != nil {
			schema = opts.Schemas[ep.ID]
		}
		results = append(results, hit(ctx, client, m, ep, token, ua, opts.Compare, opts.Shape, schema))
	}
	return results, nil
}

func hit(ctx context.Context, client *http.Client, m *Manifest, ep Endpoint, token, ua string, compare func(string) (any, bool), shapeOpts ShapeOpts, schema reflect.Type) ProbeResult {
	res := ProbeResult{ID: ep.ID}

	path, err := ExpandEnv(ep.Path)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	bodyStr := ""
	if ep.BodyJSON != "" {
		bodyStr, err = ExpandEnv(ep.BodyJSON)
		if err != nil {
			res.Error = err.Error()
			return res
		}
	}
	method := strings.ToUpper(ep.Method)
	if method == "" {
		method = "GET"
	}
	url := strings.TrimRight(m.BaseURL, "/") + path

	var body io.Reader
	if bodyStr != "" {
		body = bytes.NewBufferString(bodyStr)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		res.Error = fmt.Sprintf("build request: %v", err)
		return res
	}
	req.Header.Set(m.AuthHeader, token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ua)

	resp, err := client.Do(req)
	if err != nil {
		res.Error = fmt.Sprintf("send: %v", err)
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		res.Error = fmt.Sprintf("read body: %v", err)
		return res
	}
	res.Status = resp.StatusCode
	res.BodySize = len(respBody)

	var parsed any
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		res.NotJSON = true
		res.Signature = "<not-json>"
	} else {
		if schema != nil {
			parsed = Filter(parsed, schema)
		}
		res.Signature = Shape(parsed, shapeOpts)
	}

	if compare != nil {
		if prior, ok := compare(ep.ID); ok {
			if diffs := Diff(prior, res.Signature); len(diffs) > 0 {
				res.Drift = diffs
			}
		}
	}
	return res
}
