package storage

import (
	"errors"
	"io"
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

func TestIsTransientMegaConnectError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "incomplete response", err: errors.New("unexpected end of JSON input"), want: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: true},
		{name: "authentication error", err: errors.New("access denied"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientMegaConnectError(tt.err); got != tt.want {
				t.Fatalf("isTransientMegaConnectError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
