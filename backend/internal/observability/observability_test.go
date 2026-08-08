package observability

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	value := Redact("Bearer abc\r\nhttps://user:pass@example.test/callback?state=secret&code=value token=abc cookie=x")
	for _, forbidden := range []string{"user:pass", "state=secret", "code=value", "token=abc", "cookie=x", "\r", "\n"} {
		if strings.Contains(value, forbidden) {
			t.Errorf("Redact() retained %q in %q", forbidden, value)
		}
	}
}

func TestRedactPersonalMetadata(t *testing.T) {
	value := Redact("user@example.test failed to read /documents/private/report.pdf")
	for _, forbidden := range []string{"user@example.test", "/documents/private/report.pdf"} {
		if strings.Contains(value, forbidden) {
			t.Errorf("Redact() retained %q in %q", forbidden, value)
		}
	}
}

func TestRedactMessageKeepsPathsInDebug(t *testing.T) {
	msg := "failed to read /documents/private/report.pdf for user@example.test"
	redacted := redactMessage(msg, true)
	if !strings.Contains(redacted, "/documents/private/report.pdf") {
		t.Errorf("debug redaction stripped path: %q", redacted)
	}
	if !strings.Contains(redacted, "user@example.test") {
		t.Errorf("debug redaction stripped email: %q", redacted)
	}
	if strings.Contains(redacted, "secret=") {
		t.Errorf("debug redaction retained secret assignment: %q", redacted)
	}
}

func TestRedactMessageStripsPathsOutsideDebug(t *testing.T) {
	msg := "failed to read /documents/private/report.pdf for user@example.test secret=abc"
	redacted := redactMessage(msg, false)
	if strings.Contains(redacted, "/documents/private/report.pdf") {
		t.Errorf("non-debug redaction retained path: %q", redacted)
	}
	if strings.Contains(redacted, "user@example.test") {
		t.Errorf("non-debug redaction retained email: %q", redacted)
	}
	if strings.Contains(redacted, "secret=abc") {
		t.Errorf("non-debug redaction retained secret: %q", redacted)
	}
}

func TestNewRequestID(t *testing.T) {
	id := NewRequestID()
	if len(id) != 36 || id[14] != '4' {
		t.Fatalf("NewRequestID() = %q, want UUIDv4", id)
	}
}

func TestErrorKindClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"canceled", context.Canceled, "canceled"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"sql no rows", sql.ErrNoRows, "not_found"},
		{"not found message", errors.New("resource not found"), "not_found"},
		{"conflict message", errors.New("409 conflict: already exists"), "conflict"},
		{"rate limit message", errors.New("rate limit exceeded"), "rate_limited"},
		{"auth message", errors.New("unauthorized: invalid token"), "authentication"},
		{"timeout message", errors.New("operation timed out"), "timeout"},
		{"integrity message", errors.New("checksum mismatch"), "integrity"},
		{"database message", errors.New("pq: connection refused"), "network"},
		{"unknown", errors.New("unexpected boom"), "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ErrorKind(tt.err); got != tt.want {
				t.Errorf("ErrorKind() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSensitiveKeyRedaction(t *testing.T) {
	var buf bytes.Buffer
	handler := &redactingHandler{next: slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})}
	logger := slog.New(handler)
	logger.With(slog.String("access_token", "secret-value")).Debug("test", slog.String("password", "hunter2"), slog.String("path", "/documents/ok"))

	output := buf.String()
	if strings.Contains(output, "secret-value") {
		t.Errorf("sensitive value leaked: %s", output)
	}
	if strings.Contains(output, "hunter2") {
		t.Errorf("password leaked: %s", output)
	}
	if !strings.Contains(output, "/documents/ok") {
		t.Errorf("debug level should keep path in record attr: %s", output)
	}
}

func TestNonDebugStripsPathsInAttrs(t *testing.T) {
	var buf bytes.Buffer
	handler := &redactingHandler{next: slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})}
	logger := slog.New(handler)
	logger.With(slog.String("normal", "/path/ok")).Info("test")

	output := buf.String()
	if strings.Contains(output, "/path/ok") {
		t.Errorf("INFO level should strip paths: %s", output)
	}
}
