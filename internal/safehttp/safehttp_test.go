package safehttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func req(t *testing.T, url string) *http.Request {
	t.Helper()
	r, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCheckRedirect(t *testing.T) {
	via := []*http.Request{req(t, "https://api.example.test/a")}
	tests := []struct {
		name    string
		to      string
		wantErr bool
	}{
		{"same host https", "https://api.example.test/b", false},
		{"cross host", "https://evil.example.test/b", true},
		{"cross port", "https://api.example.test:8443/b", true},
		{"downgrade", "http://api.example.test/b", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckRedirect(req(t, tc.to), via)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestCheckRedirectCap(t *testing.T) {
	r := req(t, "https://api.example.test/a")
	via := make([]*http.Request, MaxRedirects-1)
	for i := range via {
		via[i] = r
	}
	if err := CheckRedirect(r, via); err != nil {
		t.Fatalf("%d prior hops should pass: %v", len(via), err)
	}
	via = append(via, r)
	if err := CheckRedirect(r, via); err == nil {
		t.Fatalf("%d prior hops should be refused", len(via))
	}
}

func TestNewClientEndToEnd(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("credential-bearing request reached other host")
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c := NewClient(time.Second)
	r := req(t, srv.URL)
	r.Header.Set("x-secret", "fake")
	if resp, err := c.Do(r); err == nil {
		_ = resp.Body.Close()
		t.Fatal("cross-host redirect followed")
	}
}
