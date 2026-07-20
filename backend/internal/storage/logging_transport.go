package storage

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

type loggingTransport struct {
	base http.RoundTripper
}

// newLoggingTransport wraps an HTTP RoundTripper to log outgoing HTTP/WebDAV requests and responses.
func newLoggingTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &loggingTransport{base: base}
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	sanitizedURL := req.URL.Redacted()
	depth := req.Header.Get("Depth")
	depthStr := ""
	if depth != "" {
		depthStr = fmt.Sprintf(" [Depth: %s]", depth)
	}

	log.Printf("[HTTP Request] %s %s%s\n", req.Method, sanitizedURL, depthStr)
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	duration := time.Since(start)

	if err != nil {
		log.Printf("[HTTP Response] %s %s -> ERROR: %v (%v)\n", req.Method, sanitizedURL, err, duration)
	} else {
		log.Printf("[HTTP Response] %s %s -> %s (%v)\n", req.Method, sanitizedURL, resp.Status, duration)
	}
	return resp, err
}
