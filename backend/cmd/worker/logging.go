package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

func workerLogf(component, format string, args ...any) {
	message := strings.TrimSpace(fmt.Sprintf(format, args...))
	level := slog.LevelInfo
	lower := strings.ToLower(message)
	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "cannot") || strings.Contains(lower, "warning") {
		level = slog.LevelError
	}
	slog.LogAttrs(context.Background(), level, "worker_event",
		slog.String("component", component),
		slog.String("message", message),
	)
}
