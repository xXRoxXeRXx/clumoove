package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"backend/internal/observability"
)

// logf keeps legacy handler diagnostics structured while they are gradually
// refined into event-specific log records. Request-scoped loggers carry the
// request ID added by requestLogMiddleware; error details are redacted by the
// observability package before emission.
func (s *APIServer) logf(r *http.Request, format string, args ...any) {
	s.logfContext(r.Context(), format, args...)
}

func (s *APIServer) logfContext(ctx context.Context, format string, args ...any) {
	err := fmt.Errorf(format, args...)
	observability.Logger(ctx).ErrorContext(ctx, "handler_failure", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
}

// infof is reserved for non-sensitive successful lifecycle and delivery
// events. Failures must continue to use logf so their details are redacted.
func (s *APIServer) infof(r *http.Request, format string, args ...any) {
	ctx := r.Context()
	observability.Logger(ctx).InfoContext(ctx, "handler_event", slog.String("message", fmt.Sprintf(format, args...)))
}

func (s *APIServer) warnf(r *http.Request, format string, args ...any) {
	ctx := r.Context()
	observability.Logger(ctx).WarnContext(ctx, "handler_warning", slog.String("message", fmt.Sprintf(format, args...)))
}

func (s *APIServer) logln(r *http.Request, args ...any) {
	s.loglnContext(r.Context(), args...)
}

func (s *APIServer) loglnContext(ctx context.Context, args ...any) {
	observability.Logger(ctx).InfoContext(ctx, "handler_event", slog.String("message", fmt.Sprint(args...)))
}
