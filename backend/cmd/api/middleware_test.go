package main

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"
)

func TestRequestLogMiddlewareGeneratesRequestID(t *testing.T) {
	s := &APIServer{}
	req := httptest.NewRequest(http.MethodGet, "http://api.example.test/api/settings", nil)
	req.Header.Set("X-Request-ID", "client-controlled")
	rec := httptest.NewRecorder()

	s.requestLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Request-ID") != "client-controlled" {
			t.Fatal("middleware should not mutate request headers")
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)

	id := rec.Header().Get("X-Request-ID")
	if id == "client-controlled" || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("X-Request-ID = %q, want server-generated UUIDv4", id)
	}
}

func TestCORSMiddlewareExposesRequestID(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "http://api.example.test/api/settings", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()

	corsMiddleware(http.NotFoundHandler()).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "X-Request-ID" {
		t.Fatalf("Access-Control-Expose-Headers = %q, want X-Request-ID", got)
	}
}

func TestCORSMiddlewareHidesHeadersForUntrustedOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "http://api.example.test/api/settings", nil)
	req.Header.Set("Origin", "http://evil.example.test")
	rec := httptest.NewRecorder()

	corsMiddleware(http.NotFoundHandler()).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "" {
		t.Fatalf("Access-Control-Expose-Headers = %q, want empty for untrusted origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty for untrusted origin", got)
	}
}

// captureHandler records every log record emitted through the default logger so
// middleware tests can assert on level, status, duration and event name.
type captureHandler struct {
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, level slog.Level) bool { return level >= slog.LevelDebug }
func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler             { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler                  { return h }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}

func TestRequestLogMiddleware5xxLogsAtError(t *testing.T) {
	var captured captureHandler
	prev := slog.Default()
	defer slog.SetDefault(prev)
	slog.SetDefault(slog.New(&captured).With(slog.String("service", "test")))

	s := &APIServer{}
	req := httptest.NewRequest(http.MethodGet, "http://api.example.test/api/migration/1", nil)
	rec := httptest.NewRecorder()

	s.requestLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if len(captured.records) == 0 {
		t.Fatal("expected one log record, got none")
	}
	rec0 := captured.records[0]
	if rec0.Level != slog.LevelError {
		t.Fatalf("level = %s, want ERROR", rec0.Level)
	}
	if rec0.Message != "http_request_completed" {
		t.Fatalf("message = %q, want http_request_completed", rec0.Message)
	}
	var status int
	rec0.Attrs(func(a slog.Attr) bool {
		if a.Key == "http_status" {
			status = int(a.Value.Int64())
			return false
		}
		return true
	})
	if status != http.StatusInternalServerError {
		t.Fatalf("http_status attr = %d, want 500", status)
	}
}

func TestRequestLogMiddleware2xxLogsAtInfoWithDuration(t *testing.T) {
	var captured captureHandler
	prev := slog.Default()
	defer slog.SetDefault(prev)
	slog.SetDefault(slog.New(&captured).With(slog.String("service", "test")))

	s := &APIServer{}
	req := httptest.NewRequest(http.MethodPost, "http://api.example.test/api/auth/login", nil)
	rec := httptest.NewRecorder()

	s.requestLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	})).ServeHTTP(rec, req)

	if len(captured.records) != 1 {
		t.Fatalf("records = %d, want 1", len(captured.records))
	}
	rec0 := captured.records[0]
	if rec0.Level != slog.LevelInfo {
		t.Fatalf("level = %s, want INFO", rec0.Level)
	}
	var duration int64
	rec0.Attrs(func(a slog.Attr) bool {
		if a.Key == "duration_ms" {
			duration = a.Value.Int64()
			return false
		}
		return true
	})
	if duration <= 0 {
		t.Fatalf("duration_ms = %d, want > 0", duration)
	}
}

func TestRequestLogMiddlewareSSELogsOnClose(t *testing.T) {
	var captured captureHandler
	prev := slog.Default()
	defer slog.SetDefault(prev)
	slog.SetDefault(slog.New(&captured).With(slog.String("service", "test")))

	s := &APIServer{}
	req := httptest.NewRequest(http.MethodGet, "http://api.example.test/api/migration/1/stream", nil)
	rec := httptest.NewRecorder()

	closed := make(chan struct{})
	s.requestLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer must support flushing")
		}
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		_, _ = w.Write([]byte("data: ping\n\n"))
		flusher.Flush()
		close(closed)
	})).ServeHTTP(rec, req)

	<-closed
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(captured.records) != 1 {
		t.Fatalf("records = %d, want 1 (logged on stream close)", len(captured.records))
	}
}

func TestClientIPCategoryTrustedProxyChain(t *testing.T) {
	s := &APIServer{trustedProxy: true}
	req := httptest.NewRequest(http.MethodGet, "http://api.example.test/api/settings", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	req.RemoteAddr = "10.0.0.1:54321"

	if got := s.clientIPCategory(req); got != "public" {
		t.Fatalf("clientIPCategory = %q, want public (leftmost XFF IP)", got)
	}
}

func TestClientIPCategoryUntrustedIgnoresXFF(t *testing.T) {
	s := &APIServer{trustedProxy: false}
	req := httptest.NewRequest(http.MethodGet, "http://api.example.test/api/settings", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.RemoteAddr = "127.0.0.1:54321"

	if got := s.clientIPCategory(req); got != "loopback" {
		t.Fatalf("clientIPCategory = %q, want loopback (RemoteAddr, not XFF)", got)
	}
}

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
