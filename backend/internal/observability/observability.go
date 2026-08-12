// Package observability provides the application's structured, privacy-safe logs.
package observability

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

type contextKey struct{}

var sensitiveName = regexp.MustCompile(`(?i)(authorization|bearer|token|access_token|refresh_token|password|secret|api_key|cookie|state|code|signature|sig|credential|host_key)\s*[:=]\s*[^\s,&]+`)
var urlValue = regexp.MustCompile(`https?://[^\s"']+`)
var emailValue = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
var pathValue = regexp.MustCompile(`(?:[A-Za-z]:)?/[A-Za-z0-9._~%+@=-]+(?:/[A-Za-z0-9._~%+@=-]+)+`)

// sensitiveKeys are attribute keys whose value is always replaced, regardless
// of log level, because they carry secret material.
var sensitiveKeys = []string{"token", "secret", "password", "cookie", "credential", "authorization", "state", "code", "sig", "signature", "host_key"}

// Configure installs the process-wide JSON logger. An invalid LOG_LEVEL is a
// startup error so deployments cannot silently run at an unexpected verbosity.
func Configure(service string) (*slog.Logger, error) {
	level, err := parseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return nil, err
	}
	attrs := []slog.Attr{slog.String("service", service)}
	// Operator-defined deployment labels are not secrets and must not be
	// redacted, otherwise the correlation identifiers become unusable.
	if value := os.Getenv("INSTANCE_ID"); value != "" {
		attrs = append(attrs, slog.String("instance_id", value))
	}
	if value := os.Getenv("LOG_ENVIRONMENT"); value != "" {
		attrs = append(attrs, slog.String("environment", value))
	}
	logger := slog.New(&redactingHandler{next: slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})})
	for _, attr := range attrs {
		logger = logger.With(attr)
	}
	slog.SetDefault(logger)
	// During the incremental migration legacy log calls remain JSON and pass the
	// same redaction boundary instead of writing plaintext to stderr.
	log.SetFlags(0)
	log.SetOutput(legacyWriter{logger: logger})
	return logger, nil
}

// redactMessage redacts the message text. Path and email stripping is skipped at
// DEBUG level so operators can temporarily enable path/email diagnostics for
// error analysis. Control characters, URL credentials/query and sensitive
// assignments are always removed.
func redactMessage(message string, debug bool) string {
	if debug {
		return redactCore(message)
	}
	return Redact(message)
}

// redactCore applies the always-on redaction: control characters, URL
// credentials/query, and sensitive name=value assignments. Path and email
// values are preserved so DEBUG-level diagnosis can use them.
func redactCore(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	value = urlValue.ReplaceAllStringFunc(value, func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "[redacted-url]"
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	})
	return sensitiveName.ReplaceAllString(value, "$1=[redacted]")
}

func parseLevel(value string) (slog.Level, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "INFO":
		return slog.LevelInfo, nil
	case "DEBUG":
		return slog.LevelDebug, nil
	case "WARN", "WARNING":
		return slog.LevelWarn, nil
	case "ERROR":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid LOG_LEVEL %q: expected DEBUG, INFO, WARN, or ERROR", value)
	}
}

// IsDebugEnabled reports whether the configured logger emits DEBUG records.
// Callers use this to decide whether path/file metadata redaction is relaxed.
func IsDebugEnabled(ctx context.Context) bool {
	return slog.Default().Enabled(ctx, slog.LevelDebug)
}

// NewRequestID returns a server-generated UUIDv4-like correlation identifier.
func NewRequestID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("request-%d", time.Now().UnixNano())
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, logger)
}

func Logger(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}

// Error returns a redacted error attribute for non-DEBUG records. Use
// ErrorAttr when the emitting record is at DEBUG level so path/email content
// in the error message is preserved for diagnosis.
func Error(err error) slog.Attr {
	return ErrorAttr(err, false)
}

// ErrorAttr returns a redacted error attribute. When debug is true, URL
// credentials, query values, control characters and sensitive assignments are
// still removed, but file paths and email addresses are kept.
func ErrorAttr(err error, debug bool) slog.Attr {
	if err == nil {
		return slog.String("error", "")
	}
	if debug {
		return slog.String("error", redactCore(err.Error()))
	}
	return slog.String("error", Redact(err.Error()))
}

// ErrorKind classifies an error into a stable, machine-readable category. It
// never changes the API error_code contract; it only labels operational logs.
// Unknown errors fall back to "internal".
func ErrorKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "not_found"
	}
	if kind := errorKindFromTypedError(err); kind != "" {
		return kind
	}
	if kind := errorKindFromHTTP(err); kind != "" {
		return kind
	}
	if kind := errorKindFromMessage(err.Error()); kind != "" {
		return kind
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "network"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "network"
	}
	return "internal"
}

// errorKindFromTypedError lets packages expose a stable operational category
// without requiring observability to import their error sentinels.
func errorKindFromTypedError(err error) string {
	type errorKinder interface{ ErrorKind() string }
	var ek errorKinder
	if errors.As(err, &ek) {
		return ek.ErrorKind()
	}
	return ""
}

// httpStatusFromError extracts an HTTP status previously attached to the error
// via the standard errors.Join / fmt.Errorf conventions used across handlers.
func errorKindFromHTTP(err error) string {
	type httpStatuser interface{ HTTPStatus() int }
	var hs httpStatuser
	if errors.As(err, &hs) {
		return kindFromHTTPStatus(hs.HTTPStatus())
	}
	return ""
}

func kindFromHTTPStatus(status int) string {
	switch {
	case status >= 500:
		return "internal"
	case status == http.StatusUnauthorized, status == 407:
		return "authentication"
	case status == http.StatusForbidden:
		return "authorization"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusConflict:
		return "conflict"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status >= 400:
		return "validation"
	}
	return ""
}

// errorKindFromMessage inspects provider/DB error text for well-known patterns
// so external library errors (which embed status or provider codes) are labeled
// even when no typed sentinel is available.
func errorKindFromMessage(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "not found"), strings.Contains(lower, "no such"), strings.Contains(lower, "does not exist"):
		return "not_found"
	case strings.Contains(lower, "conflict"), strings.Contains(lower, "already exists"), strings.Contains(lower, "duplicate"):
		return "conflict"
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "too many requests"), strings.Contains(lower, "throttled"):
		return "rate_limited"
	case strings.Contains(lower, "unauthorized"), strings.Contains(lower, "unauthenticated"), strings.Contains(lower, "invalid token"), strings.Contains(lower, "forbidden"), strings.Contains(lower, "access denied"):
		return "authentication"
	case strings.Contains(lower, "deadline"), strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"):
		return "timeout"
	case strings.Contains(lower, "connection refused"), strings.Contains(lower, "no route"), strings.Contains(lower, "network is unreachable"), strings.Contains(lower, "dns"):
		return "network"
	case strings.Contains(lower, "integrity"), strings.Contains(lower, "checksum"), strings.Contains(lower, "hash mismatch"):
		return "integrity"
	case strings.Contains(lower, "database"), strings.Contains(lower, "sqlstate"), strings.Contains(lower, "pq:"), strings.Contains(lower, "driver:"):
		return "database"
	}
	return ""
}

// Redact removes control characters, URL credentials/query values, email
// addresses, file paths and known secret assignments before a value crosses
// the operational logging boundary. This is the non-DEBUG redaction: paths
// and emails are stripped because they are personal metadata.
func Redact(value string) string {
	value = redactCore(value)
	value = emailValue.ReplaceAllString(value, "[redacted-email]")
	value = pathValue.ReplaceAllString(value, "[redacted-path]")
	return value
}

type legacyWriter struct{ logger *slog.Logger }

func (w legacyWriter) Write(data []byte) (int, error) {
	// Legacy callers frequently interpolate paths, filenames, and provider
	// errors. Keep their redacted message observable so critical events
	// (lockouts, schema-migration failures, OAuth-daemon errors) remain
	// diagnosable during the incremental slog migration. The redaction boundary
	// strips credentials, URL query, control characters and (outside DEBUG)
	// personal metadata before the text is emitted.
	msg := strings.TrimSpace(string(data))
	if msg == "" {
		return len(data), nil
	}
	w.logger.Info("legacy_log", slog.String("component", "legacy"), slog.String("message", Redact(msg)))
	return len(data), nil
}

type redactingHandler struct{ next slog.Handler }

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}
func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name)}
}
func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &redactingHandler{next: h.next.WithAttrs(redactAttrs(attrs, false))}
}
func (h *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	debug := record.Level <= slog.LevelDebug
	clean := slog.NewRecord(record.Time, record.Level, redactMessage(record.Message, debug), record.PC)
	record.Attrs(func(attr slog.Attr) bool { clean.AddAttrs(redactAttr(attr, debug)); return true })
	return h.next.Handle(ctx, clean)
}

// redactAttrs redacts a batch of attributes. When debug is true, path and email
// values in string attributes are preserved (control chars, URLs, secrets and
// sensitive keys remain redacted on every level).
func redactAttrs(attrs []slog.Attr, debug bool) []slog.Attr {
	clean := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		clean[i] = redactAttr(attr, debug)
	}
	return clean
}

func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, sensitive := range sensitiveKeys {
		if strings.Contains(lower, sensitive) {
			return true
		}
	}
	return false
}

func redactAttr(attr slog.Attr, debug bool) slog.Attr {
	if isSensitiveKey(attr.Key) {
		return slog.String(attr.Key, "[redacted]")
	}
	if attr.Value.Kind() == slog.KindGroup {
		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(redactAttrs(attr.Value.Group(), debug)...)}
	}
	if attr.Value.Kind() == slog.KindString {
		if debug {
			return slog.String(attr.Key, redactCore(attr.Value.String()))
		}
		return slog.String(attr.Key, Redact(attr.Value.String()))
	}
	if attr.Value.Kind() == slog.KindAny {
		if err, ok := attr.Value.Any().(error); ok {
			return ErrorAttr(err, debug)
		}
	}
	return attr
}

var _ io.Writer = legacyWriter{}
