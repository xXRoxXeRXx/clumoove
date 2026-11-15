package main

import "backend/internal/observability"

func workerLogf(component, format string, args ...any) {
	observability.LogLegacyf("worker_event", component, format, args...)
}
