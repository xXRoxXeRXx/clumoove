package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeadersMiddlewareHSTS(t *testing.T) {
	tests := []struct {
		name           string
		trustedProxy   bool
		forwardedProto string
		directTLS      bool
		wantHSTS       bool
	}{
		{name: "direct HTTP", wantHSTS: false},
		{name: "direct TLS", directTLS: true, wantHSTS: true},
		{name: "untrusted forwarded HTTPS", forwardedProto: "https", wantHSTS: false},
		{name: "trusted forwarded HTTPS", trustedProxy: true, forwardedProto: "https", wantHSTS: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &APIServer{trustedProxy: tt.trustedProxy}
			req := httptest.NewRequest(http.MethodGet, "http://api.example.test/api/settings", nil)
			if tt.directTLS {
				req.TLS = &tls.ConnectionState{}
			}
			if tt.forwardedProto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.forwardedProto)
			}
			rec := httptest.NewRecorder()

			s.securityHeadersMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)

			gotHSTS := rec.Header().Get("Strict-Transport-Security") != ""
			if gotHSTS != tt.wantHSTS {
				t.Fatalf("HSTS present = %v, want %v", gotHSTS, tt.wantHSTS)
			}
		})
	}
}
