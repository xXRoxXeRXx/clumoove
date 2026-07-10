package storage

import (
	"errors"
	"os"
	"testing"
)

func TestIsSMBAuthError(t *testing.T) {
	tests := []struct {
		err      error
		expected bool
	}{
		{err: nil, expected: false},
		{err: os.ErrPermission, expected: true},
		{err: fmtError("The attempted logon is invalid. This is either due to a bad username or authentication information."), expected: true},
		{err: fmtError("bad username or password"), expected: true},
		{err: fmtError("permission denied"), expected: false}, // is NOT logon/bad username
		{err: fmtError("network timeout"), expected: false},
	}

	for i, tt := range tests {
		result := isSMBAuthError(tt.err)
		if result != tt.expected {
			t.Errorf("test %d failed: expected %v, got %v for err: %v", i, tt.expected, result, tt.err)
		}
	}
}

func fmtError(msg string) error {
	return errors.New(msg)
}

func TestNewSMBProviderValid(t *testing.T) {
	tests := []struct {
		name           string
		rawURL         string
		username       string
		password       string
		expectedHost   string
		expectedPort   string
		expectedShare  string
		expectedDomain string
	}{
		{
			name:           "Standard SMB URL",
			rawURL:         "smb://192.168.1.10/projekte",
			username:       "admin",
			password:       "secret",
			expectedHost:   "192.168.1.10",
			expectedPort:   "445",
			expectedShare:  "projekte",
			expectedDomain: "",
		},
		{
			name:           "SMB URL with custom port and domain",
			rawURL:         "smb://nas.local:1445/projekte?domain=WORKGROUP",
			username:       "user",
			password:       "pass",
			expectedHost:   "nas.local",
			expectedPort:   "1445",
			expectedShare:  "projekte",
			expectedDomain: "WORKGROUP",
		},
		{
			name:           "SMB URL with nested path segment (only first segment is share)",
			rawURL:         "smb://192.168.1.10/projekte/sub/dir?domain=MYDOMAIN",
			username:       "user",
			password:       "pass",
			expectedHost:   "192.168.1.10",
			expectedPort:   "445",
			expectedShare:  "projekte",
			expectedDomain: "MYDOMAIN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewSMBProvider(tt.rawURL, tt.username, tt.password)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if provider.Host != tt.expectedHost {
				t.Errorf("expected Host %q, got %q", tt.expectedHost, provider.Host)
			}
			if provider.Port != tt.expectedPort {
				t.Errorf("expected Port %q, got %q", tt.expectedPort, provider.Port)
			}
			if provider.Share != tt.expectedShare {
				t.Errorf("expected Share %q, got %q", tt.expectedShare, provider.Share)
			}
			if provider.Domain != tt.expectedDomain {
				t.Errorf("expected Domain %q, got %q", tt.expectedDomain, provider.Domain)
			}
			if provider.Username != tt.username {
				t.Errorf("expected Username %q, got %q", tt.username, provider.Username)
			}
			if provider.Password != tt.password {
				t.Errorf("expected Password %q, got %q", tt.password, provider.Password)
			}
		})
	}
}

func TestNewSMBProviderInvalid(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr string
	}{
		{
			name:    "Invalid scheme",
			rawURL:  "ftp://192.168.1.10/share",
			wantErr: "invalid scheme",
		},
		{
			name:    "Missing host",
			rawURL:  "smb:///share",
			wantErr: "missing host",
		},
		{
			name:    "Missing share",
			rawURL:  "smb://192.168.1.10",
			wantErr: "missing share name",
		},
		{
			name:    "Missing share (empty path)",
			rawURL:  "smb://192.168.1.10/",
			wantErr: "missing share name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSMBProvider(tt.rawURL, "user", "pass")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !containsSubstring(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (len(s) > 0 && (s[0:len(sub)] == sub || containsSubstring(s[1:], sub))))
}
