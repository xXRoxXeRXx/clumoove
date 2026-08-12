package processor

import (
	"fmt"
	"log/slog"
	"strings"
)

// processorLogf adapts legacy formatted messages to structured slog records.
// It sends every record through the application's JSON/redaction handler. New
// call sites should use slog directly with domain-specific attributes.
func processorLogf(format string, args ...any) {
	message := strings.TrimSpace(fmt.Sprintf(format, args...))
	level := slog.LevelInfo
	lower := strings.ToLower(message)
	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "cannot") || strings.Contains(lower, "warning") {
		level = slog.LevelError
	}
	slog.LogAttrs(nil, level, "processor_event",
		slog.String("component", "processor"),
		slog.String("message", message),
	)
}
