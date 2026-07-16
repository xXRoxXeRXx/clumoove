package storage

import (
	"context"
	"testing"
)

func TestIsValidProvider(t *testing.T) {
	for _, p := range ValidProviders {
		if !IsValidProvider(p) {
			t.Errorf("IsValidProvider(%q) = false, want true", p)
		}
	}
	invalid := []string{"", "ftp", "local", "NEXTCLOUD", "Dropbox", "s3 "}
	for _, p := range invalid {
		if IsValidProvider(p) {
			t.Errorf("IsValidProvider(%q) = true, want false", p)
		}
	}
}

func TestNewProviderRejectsUnsupported(t *testing.T) {
	_, err := NewProvider(context.Background(), "unsupported-type", "https://example.com", "u", "p")
	if err == nil {
		t.Errorf("expected error for unsupported provider type")
	}
}

func TestNewProviderSanitizesCredentialsInURL(t *testing.T) {
	// nextcloud/webdav pull creds out of the URL userinfo and strip them so they
	// don't leak into url.Error later. The provider should be constructed without error.
	cases := []struct {
		typ string
		url string
	}{
		{"nextcloud", "https://user:pass@10.0.0.5/remote.php/dav"},
		{"webdav", "https://user:pass@192.168.1.10/dav"},
	}
	for _, c := range cases {
		p, err := NewProvider(context.Background(), c.typ, c.url, "", "")
		if err != nil {
			t.Errorf("%s: NewProvider failed: %v", c.typ, err)
			continue
		}
		if p == nil {
			t.Errorf("%s: expected non-nil provider", c.typ)
		}
	}
}

func TestNewProviderSSRFBlockedByDefault(t *testing.T) {
	// Loopback must always be blocked regardless of MIGRATION_BLOCK_PRIVATE.
	blockPrivateEgress = false
	defer func() { blockPrivateEgress = false }()

	cases := []struct {
		typ string
		url string
	}{
		{"nextcloud", "https://127.0.0.1/remote.php/dav"},
		{"webdav", "https://localhost/dav"},
		{"smb", "smb://169.254.169.254/share"},
		{"sftp", "sftp://[::1]/"},
	}
	for _, c := range cases {
		if _, err := NewProvider(context.Background(), c.typ, c.url, "u", "p"); err == nil {
			t.Errorf("%s with %q: expected SSRF block, got nil error", c.typ, c.url)
		}
	}
}

func TestNewProviderOAuthProviders(t *testing.T) {
	// dropbox/google take the token in the password field; no egress validation.
	if p, err := NewProvider(context.Background(), "dropbox", "", "u", "oauth-token"); err != nil || p == nil {
		t.Errorf("dropbox: got p=%v err=%v", p, err)
	}
	if p, err := NewProvider(context.Background(), "google", "", "u", "oauth-token"); err != nil || p == nil {
		t.Errorf("google: got p=%v err=%v", p, err)
	}
	// magentacloud ignores url.
	if p, err := NewProvider(context.Background(), "magentacloud", "", "u", "p"); err != nil || p == nil {
		t.Errorf("magentacloud: got p=%v err=%v", p, err)
	}
}
