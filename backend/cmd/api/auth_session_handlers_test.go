package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSessionUserAgentKeepsUTF8BoundaryWithinByteCap(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("User-Agent", strings.Repeat("a", 510)+"€")

	got := sessionUserAgent(req)
	if len(got) != 510 {
		t.Fatalf("sessionUserAgent byte length = %d, want 510", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("sessionUserAgent returned invalid UTF-8: %q", got)
	}
}
