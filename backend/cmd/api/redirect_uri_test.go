package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(trustedProxy bool) *APIServer {
	return &APIServer{trustedProxy: trustedProxy}
}

func TestPublicHost(t *testing.T) {
	cases := []struct {
		name     string
		trusted  bool
		xfh      string
		host     string
		proto    string
		want     string
	}{
		{name: "untrusted uses request host with port", trusted: false, host: "example.com:8000", want: "example.com:8000"},
		{name: "untrusted uses request host without port", trusted: false, host: "example.com", want: "example.com"},
		{name: "trusted uses forwarded host", trusted: true, xfh: "real.example", host: "example.com:8000", want: "real.example"},
		{name: "trusted preserves non-default port", trusted: true, xfh: "app.example.com:8443", host: "example.com:8000", want: "app.example.com:8443"},
		{name: "trusted first of comma list", trusted: true, xfh: "a.example, b.example", host: "example.com:8000", want: "a.example"},
		{name: "trusted rejects path", trusted: true, xfh: "evil.example/path", host: "example.com:8000", want: "example.com:8000"},
		{name: "trusted rejects whitespace", trusted: true, xfh: "a b", host: "example.com:8000", want: "example.com:8000"},
		{name: "trusted rejects CRLF", trusted: true, xfh: "host\r\nX-Injected: y", host: "example.com:8000", want: "example.com:8000"},
		{name: "empty forwarded falls back", trusted: true, xfh: "", host: "example.com:8000", want: "example.com:8000"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(tc.trusted)
			req := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/x", nil)
			if tc.xfh != "" {
				req.Header.Set("X-Forwarded-Host", tc.xfh)
			}
			if got := s.publicHost(req); got != tc.want {
				t.Fatalf("publicHost = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGetRedirectURIScheme(t *testing.T) {
	// Not secure: plain HTTP host -> http:// redirect URI.
	s := newTestServer(false)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/x", nil)
	if got := s.getRedirectURI(req); got != "http://example.com/api/oauth/callback" {
		t.Fatalf("getRedirectURI = %q, want http://example.com/api/oauth/callback", got)
	}

	// Secure behind a trusted proxy: X-Forwarded-Proto https + forwarded host.
	s2 := newTestServer(true)
	reqSecure := httptest.NewRequest(http.MethodGet, "http://example.com/x", nil)
	reqSecure.Header.Set("X-Forwarded-Proto", "https")
	reqSecure.Header.Set("X-Forwarded-Host", "app.example.com")
	if got := s2.getRedirectURI(reqSecure); got != "https://app.example.com/api/oauth/callback" {
		t.Fatalf("getRedirectURI = %q, want https://app.example.com/api/oauth/callback", got)
	}

	// Trusted proxy that does NOT forward X-Forwarded-Proto still yields https,
	// because the proxy is the TLS endpoint.
	s3 := newTestServer(true)
	reqNoProto := httptest.NewRequest(http.MethodGet, "http://example.com/x", nil)
	if got := s3.getRedirectURI(reqNoProto); got != "https://example.com/api/oauth/callback" {
		t.Fatalf("getRedirectURI = %q, want https://example.com/api/oauth/callback", got)
	}

	// An explicit X-Forwarded-Proto: http is honored even behind a trusted proxy.
	s4 := newTestServer(true)
	reqHttp := httptest.NewRequest(http.MethodGet, "http://example.com/x", nil)
	reqHttp.Header.Set("X-Forwarded-Proto", "http")
	if got := s4.getRedirectURI(reqHttp); got != "http://example.com/api/oauth/callback" {
		t.Fatalf("getRedirectURI = %q, want http://example.com/api/oauth/callback", got)
	}
}
