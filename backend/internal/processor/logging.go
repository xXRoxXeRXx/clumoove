package processor

import "backend/internal/observability"

// processorLogf adapts legacy formatted messages to structured slog records.
// It sends every record through the application's JSON/redaction handler. New
// call sites should use slog directly with domain-specific attributes.
func processorLogf(format string, args ...any) {
	observability.LogLegacyf("processor_event", "processor", format, args...)
}
