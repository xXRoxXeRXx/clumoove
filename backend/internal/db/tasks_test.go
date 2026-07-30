package db

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestIndexingErrorMessage(t *testing.T) {
	tests := []struct {
		name  string
		input IndexingErrorInput
		want  string
	}{
		{name: "persists explicit message", input: IndexingErrorInput{ErrorMessage: "failed to inspect path"}, want: "failed to inspect path"},
		{name: "falls back to error", input: IndexingErrorInput{Err: errors.New("listing failed")}, want: "listing failed"},
		{name: "prefers explicit message", input: IndexingErrorInput{ErrorMessage: "safe message", Err: errors.New("raw error")}, want: "safe message"},
		{name: "sanitizes fallback error", input: IndexingErrorInput{Err: errors.New("failed: https://user:password@example.com")}, want: "failed: https://***:***@example.com"},
		{name: "empty input returns empty string", input: IndexingErrorInput{}, want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := indexingErrorMessage(test.input); got != test.want {
				t.Errorf("indexingErrorMessage() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDisplayTaskName(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		metadata string
		want     string
	}{
		{
			name:     "uses Immich original filename",
			filePath: "/All Assets/eaf7957e-0601-428a-895d-55a376194d5a",
			metadata: `{"custom_props":{"immich_filename":"2026-07-urlaub.jpg"}}`,
			want:     "2026-07-urlaub.jpg",
		},
		{
			name:     "uses picker name",
			filePath: "/picker/opaque-id",
			metadata: `{"name":"document.pdf"}`,
			want:     "document.pdf",
		},
		{
			name:     "falls back to path",
			filePath: "/files/document.pdf",
			metadata: `{}`,
			want:     "/files/document.pdf",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := displayTaskName(test.filePath, json.RawMessage(test.metadata))
			if got != test.want {
				t.Errorf("displayTaskName(%q, %s) = %q, want %q", test.filePath, test.metadata, got, test.want)
			}
		})
	}
}
