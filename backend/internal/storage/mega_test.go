package storage

import (
	"errors"
	"testing"
)

func TestCleanMegaPath(t *testing.T) {
	tests := []struct {
		name, input, want string
		wantErr           error
	}{
		{"root", "/", "/", nil},
		{"normalizes dot", "a/../b", "/b", nil},
		{"clamps traversal", "../../etc/passwd", "/etc/passwd", nil},
		{"rejects backslash", `a\b`, "", ErrPathEscapesRoot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, err := cleanMegaPath(tt.input)
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Fatalf("cleanMegaPath(%q) = (%q, %v), want (%q, %v)", tt.input, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestMegaCapabilities(t *testing.T) {
	p := NewMegaProvider("user@example.com", "password", MegaSession{})
	if p.VerificationMode() != VerificationSizeOnly || !p.SupportsAtomicRename() {
		t.Fatal("unexpected MEGA capabilities")
	}
}
