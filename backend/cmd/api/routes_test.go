package main

import (
	"context"
	"testing"
)

func TestSetupRoutesDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("setupRoutes panicked: %v", r)
		}
	}()

	server := &APIServer{
		db:            nil,
		jwtSecret:     "01234567890123456789012345678901",
		encryptionKey: "01234567890123456789012345678901",
		backgroundCtx: context.Background(),
	}

	mux := server.setupRoutes()
	if mux == nil {
		t.Fatal("expected non-nil http.ServeMux")
	}

	// Verify that the handler wrapper chain builds correctly as well
	handler := server.requestLogMiddleware(server.securityHeadersMiddleware(corsMiddleware(mux)))
	if handler == nil {
		t.Fatal("expected non-nil wrapped http.Handler")
	}
}
