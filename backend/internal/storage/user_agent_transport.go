package storage

import (
	"net/http"
	"strings"

	"backend/internal/version"
)

type userAgentTransport struct {
	base      http.RoundTripper
	userAgent string
}

// newUserAgentTransport wraps an http.RoundTripper to ensure outgoing HTTP
// requests carry the standardized Clumoove User-Agent header.
func newUserAgentTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &userAgentTransport{
		base:      base,
		userAgent: version.UserAgent(),
	}
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	currentUA := req.Header.Get("User-Agent")
	if currentUA == "" || strings.HasPrefix(currentUA, "Go-http-client") {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", t.userAgent)
	}
	return t.base.RoundTrip(req)
}

var _ http.RoundTripper = (*userAgentTransport)(nil)
