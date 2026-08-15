package storage

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestExactSizeReader(t *testing.T) {
	tests := []struct {
		name    string
		content string
		size    int64
		wantErr bool
	}{
		{name: "exact body", content: "hello", size: 5},
		{name: "short body", content: "hell", size: 5, wantErr: true},
		{name: "trailing body", content: "hello!", size: 5, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewExactSizeReader(strings.NewReader(tt.content), tt.size)
			got, copyErr := io.ReadAll(reader)
			if copyErr != nil && !errors.Is(copyErr, ErrUploadSizeMismatch) {
				t.Fatalf("ReadAll() error = %v", copyErr)
			}
			if string(got) != tt.content[:min(len(tt.content), int(tt.size))] {
				t.Fatalf("ReadAll() = %q", got)
			}
			err := reader.Verify()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Verify() error = %v, wantErr %t", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrUploadSizeMismatch) {
				t.Fatalf("Verify() error = %v, want ErrUploadSizeMismatch", err)
			}
		})
	}
}
