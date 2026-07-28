package db

import (
	"encoding/json"
	"testing"
)

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
