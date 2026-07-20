package storage

import (
	"testing"
)

func TestNewNextcloudProviderURLNormalization(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "https://nextcloud.example.com",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://nextcloud.example.com/",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://nextcloud.example.com/remote.php/dav",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://nextcloud.example.com/remote.php/dav/",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://nextcloud.example.com/remote.php/dav/files/user",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://nextcloud.example.com/remote.php/dav/files/user/",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://nextcloud.example.com/remote.php/dav/files/user/subfolder",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://example.com/nextcloud/remote.php/dav/files/user",
			expected: "https://example.com/nextcloud/remote.php/dav",
		},
		{
			input:    "https://example.com/remote.php/webdav/files/user",
			expected: "https://example.com/remote.php/dav",
		},
	}

	for _, tt := range tests {
		p, err := NewNextcloudProvider(tt.input, "user", "pass")
		if err != nil {
			t.Errorf("NewNextcloudProvider(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if p.BaseURL != tt.expected {
			t.Errorf("NewNextcloudProvider(%q).BaseURL = %q, want %q", tt.input, p.BaseURL, tt.expected)
		}
	}
}
