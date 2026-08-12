package observability

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRedact(t *testing.T) {
	value := Redact("Bearer abc\r\nhttps://user:pass@example.test/callback?state=secret&code=value token=abc cookie=x")
	for _, forbidden := range []string{"user:pass", "state=secret", "code=value", "token=abc", "cookie=x", "Bearer abc", "\r", "\n"} {
		if strings.Contains(value, forbidden) {
			t.Errorf("Redact() retained %q in %q", forbidden, value)
		}
	}
}

func TestRedactBearerToken(t *testing.T) {
	for _, input := range []string{
		"Authorization: Bearer eyJabc.def.ghi",
		"provider returned Bearer eyJabc.def.ghi while refreshing",
		"provider returned Bearer\teyJabc.def.ghi while refreshing",
		"Authorization: Basic dXNlcjpwYXNz",
	} {
		if output := Redact(input); strings.Contains(output, "eyJabc") || strings.Contains(output, "dXNlcjpwYXNz") {
			t.Errorf("Redact() leaked scheme token: %q -> %q", input, output)
		}
	}
}

func TestRedactCustomSensitiveMessageKeys(t *testing.T) {
	if !isSensitiveKey("x_api_key") {
		t.Fatal("x_api_key was not considered sensitive")
	}
	output := Redact("my_token=secret auth_token=another x_api_key=key error_code=SAFE")
	for _, forbidden := range []string{"my_token=secret", "auth_token=another", "x_api_key=key"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("Redact() leaked custom sensitive value %q in %q", forbidden, output)
		}
	}
	if !strings.Contains(output, "error_code=SAFE") {
		t.Errorf("Redact() redacted safe operational label: %q", output)
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

func TestNewRequestIDFallbackUsesUUIDShape(t *testing.T) {
	id := newRequestID(func([]byte) (int, error) { return 0, errors.New("entropy unavailable") }, func() time.Time {
		return time.Unix(0, 123)
	})
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[14] != '4' || id[18] != '-' || id[23] != '-' {
		t.Fatalf("fallback request ID = %q, want UUID shape", id)
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
		{"authorization message", errors.New("forbidden: access denied"), "authorization"},
		{"HTTP status", testHTTPStatusError{status: 403}, "authorization"},
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

type testHTTPStatusError struct{ status int }

func (e testHTTPStatusError) Error() string   { return "provider failure" }
func (e testHTTPStatusError) HTTPStatus() int { return e.status }

func TestSensitiveKeyRedaction(t *testing.T) {
	var buf bytes.Buffer
	handler := &redactingHandler{next: slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})}
	logger := slog.New(handler)
	logger.With(slog.String("access_token", "secret-value")).Debug("test", slog.String("password", "hunter2"), slog.String("api_key", "api-key-value"), slog.String("path", "/documents/ok"))

	output := buf.String()
	if strings.Contains(output, "secret-value") {
		t.Errorf("sensitive value leaked: %s", output)
	}
	if strings.Contains(output, "hunter2") {
		t.Errorf("password leaked: %s", output)
	}
	if strings.Contains(output, "api-key-value") {
		t.Errorf("API key leaked: %s", output)
	}
	if !strings.Contains(output, "/documents/ok") {
		t.Errorf("debug level should keep path in record attr: %s", output)
	}
}

func TestSafeSensitiveFragmentKeysRemainVisible(t *testing.T) {
	var buf bytes.Buffer
	handler := &redactingHandler{next: slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})}
	logger := slog.New(handler)
	logger.Error("test", slog.String("token_type", "refresh"), slog.String("error_code", "OAUTH_EXCHANGE_FAILED"))

	output := buf.String()
	for _, wanted := range []string{`"token_type":"refresh"`, `"error_code":"OAUTH_EXCHANGE_FAILED"`} {
		if !strings.Contains(output, wanted) {
			t.Errorf("safe operational key was redacted: %s", output)
		}
	}
	message := Redact("token_type=refresh error_code=OAUTH_EXCHANGE_FAILED")
	if !strings.Contains(message, "token_type=refresh") || !strings.Contains(message, "error_code=OAUTH_EXCHANGE_FAILED") {
		t.Errorf("safe operational label was redacted from message: %s", message)
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

func TestWithAttrsKeepsPathsAtDebug(t *testing.T) {
	var buf bytes.Buffer
	handler := &redactingHandler{next: slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})}
	slog.New(handler).With(slog.String("path", "/documents/ok")).Debug("test")
	if output := buf.String(); !strings.Contains(output, "/documents/ok") {
		t.Errorf("DEBUG level should keep paths in With attrs: %s", output)
	}
}

func TestWithAttrsPreservesGroupPlacement(t *testing.T) {
	var buf bytes.Buffer
	handler := &redactingHandler{next: slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})}
	logger := slog.New(handler).With(slog.String("root", "value")).WithGroup("request").With(slog.String("bound", "value"))
	logger.Info("test", slog.String("record", "value"))

	output := buf.String()
	for _, wanted := range []string{`"root":"value"`, `"request":{"bound":"value","record":"value"}`} {
		if !strings.Contains(output, wanted) {
			t.Errorf("logger grouping changed: %s", output)
		}
	}
}

func TestRedactRootedWindowsPathsWithoutMatchingRelativeValues(t *testing.T) {
	output := Redact(`processed 2024/01/15 at C:\Users\alice\report.txt and /etc`)
	if !strings.Contains(output, "2024/01/15") {
		t.Errorf("relative value was mistaken for a path: %s", output)
	}
	for _, forbidden := range []string{`C:\Users\alice\report.txt`, "/etc"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("rooted path leaked %q in %s", forbidden, output)
		}
	}
}

func TestLegacyWriterUsesDebugRedactionAndConfiguredThreshold(t *testing.T) {
	t.Run("debug preserves path", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(&redactingHandler{next: slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})})
		if _, err := (legacyWriter{logger: logger}).Write([]byte("failed at /documents/ok\n")); err != nil {
			t.Fatal(err)
		}
		if output := buf.String(); !strings.Contains(output, "/documents/ok") {
			t.Errorf("legacy DEBUG log stripped path: %s", output)
		}
	})

	t.Run("error remains visible", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(&redactingHandler{next: slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})})
		if _, err := (legacyWriter{logger: logger}).Write([]byte("schema migration failed\n")); err != nil {
			t.Fatal(err)
		}
		if output := buf.String(); !strings.Contains(output, `"level":"ERROR"`) {
			t.Errorf("legacy log was not emitted at ERROR threshold: %s", output)
		}
	})
}

func TestParseLevel(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  slog.Level
		valid bool
	}{
		{"", slog.LevelInfo, true},
		{"debug", slog.LevelDebug, true},
		{"WARNING", slog.LevelWarn, true},
		{"ERROR", slog.LevelError, true},
		{"verbose", 0, false},
	} {
		got, err := parseLevel(tt.input)
		if (err == nil) != tt.valid || tt.valid && got != tt.want {
			t.Errorf("parseLevel(%q) = (%v, %v), want (%v, valid=%t)", tt.input, got, err, tt.want, tt.valid)
		}
	}
}

func TestConfigureRejectsInvalidLevel(t *testing.T) {
	t.Setenv("LOG_LEVEL", "verbose")
	if _, err := Configure("test"); err == nil {
		t.Fatal("Configure() accepted invalid LOG_LEVEL")
	}
}

func TestLoggerNilContextFallback(t *testing.T) {
	if Logger(nil) == nil {
		t.Fatal("Logger(nil) returned nil")
	}
	if WithLogger(nil, slog.Default()) == nil {
		t.Fatal("WithLogger(nil, logger) returned nil context")
	}
}
