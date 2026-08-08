package storage

import (
	"log/slog"
	"net"
	"net/http"
	"time"

	"backend/internal/observability"
)

type loggingTransport struct {
	base http.RoundTripper
}

// newLoggingTransport emits bounded HTTP diagnostics without logging request
// URLs, query values, headers, bodies, or provider error text. Only the host
// category (loopback/private/public) is recorded, never the raw hostname,
// because internal infrastructure names are operational metadata.
func newLoggingTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &loggingTransport{base: base}
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	duration := time.Since(start)
	hostCategory := hostCategoryFromURL(req.URL.Hostname())

	if err != nil {
		level := slog.LevelWarn
		if observability.ErrorKind(err) == "canceled" {
			// context.Canceled during shutdown is expected, not a degradation.
			level = slog.LevelDebug
		}
		slog.LogAttrs(req.Context(), level, "provider_http_failed",
			slog.String("component", "storage_transport"),
			slog.String("operation", "http_request"),
			slog.String("provider_host_category", hostCategory),
			slog.String("method", req.Method),
			slog.Int64("duration_ms", duration.Milliseconds()),
			observability.Error(err),
			slog.String("error_kind", observability.ErrorKind(err)),
		)
	} else if resp.StatusCode >= http.StatusBadRequest {
		slog.WarnContext(req.Context(), "provider_http_completed",
			slog.String("component", "storage_transport"),
			slog.String("operation", "http_request"),
			slog.String("provider_host_category", hostCategory),
			slog.String("method", req.Method),
			slog.Int("http_status", resp.StatusCode),
			slog.Int64("duration_ms", duration.Milliseconds()),
		)
	}
	return resp, err
}

// hostCategoryFromURL returns a coarse IP category instead of the raw hostname
// so internal infrastructure names are not leaked into operational logs.
func hostCategoryFromURL(hostname string) string {
	if ip := net.ParseIP(hostname); ip != nil {
		if ip.IsLoopback() {
			return "loopback"
		}
		if ip.IsPrivate() {
			return "private"
		}
		return "public"
	}
	// DNS names cannot be reliably categorised without resolution; treat them as
	// "public" since they are user-provided provider endpoints reachable over
	// the internet by default. Loopback/private hostnames are uncommon for
	// configured providers and would already pass SSRF validation.
	return "public"
}

var _ http.RoundTripper = (*loggingTransport)(nil)
