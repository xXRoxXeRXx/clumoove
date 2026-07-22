package storage

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"google.golang.org/api/googleapi"
)

func TestNewGoogleProviderRequiresToken(t *testing.T) {
	_, err := NewGoogleProvider(context.Background(), "")
	if err == nil {
		t.Error("expected error when token is empty, got nil")
	}
}

func TestNewGoogleProviderValidToken(t *testing.T) {
	p, err := NewGoogleProvider(context.Background(), "mock-oauth-token")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil GoogleProvider")
	}
	if !p.SupportsAtomicRename() {
		t.Error("expected SupportsAtomicRename() = true")
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestIsGoogleAuthError(t *testing.T) {
	gAuthErr := &googleapi.Error{Code: http.StatusUnauthorized, Message: "Invalid Credentials"}
	if !isGoogleAuthError(gAuthErr) {
		t.Errorf("expected isGoogleAuthError(gAuthErr) = true")
	}

	gForbiddenErr := &googleapi.Error{Code: http.StatusForbidden, Message: "Access Denied"}
	if !isGoogleAuthError(gForbiddenErr) {
		t.Errorf("expected isGoogleAuthError(gForbiddenErr) = true")
	}

	rawAuthErr := errors.New("oauth2: cannot fetch token: 401 Unauthorized")
	if !isGoogleAuthError(rawAuthErr) {
		t.Errorf("expected isGoogleAuthError(rawAuthErr) = true")
	}

	gNotFoundErr := &googleapi.Error{Code: http.StatusNotFound, Message: "Not Found"}
	if isGoogleAuthError(gNotFoundErr) {
		t.Errorf("expected isGoogleAuthError(gNotFoundErr) = false")
	}
}

func TestGoogleProviderNonFilesFileExists(t *testing.T) {
	p, err := NewGoogleProvider(context.Background(), "mock-token")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	ctx := context.Background()
	_, _, err = p.FileExists(ctx, "invalid_type", "/test")
	if err == nil {
		t.Error("FileExists: expected error for invalid resourceType, got nil")
	}
}

func TestGoogleDocsExtension(t *testing.T) {
	mime, ext := googleDocsExtension("application/vnd.google-apps.document")
	if ext != ".docx" || mime == "" {
		t.Errorf("expected .docx, got mime=%s ext=%s", mime, ext)
	}
	mime, ext = googleDocsExtension("application/vnd.google-apps.spreadsheet")
	if ext != ".xlsx" || mime == "" {
		t.Errorf("expected .xlsx, got mime=%s ext=%s", mime, ext)
	}
	mime, ext = googleDocsExtension("unknown/mime")
	if ext != "" || mime != "" {
		t.Errorf("expected empty for unknown mime, got mime=%s ext=%s", mime, ext)
	}
}
