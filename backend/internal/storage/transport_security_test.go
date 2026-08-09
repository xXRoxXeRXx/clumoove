package storage

import "testing"

func TestUserConfiguredHTTPProvidersRejectPlaintextHTTP(t *testing.T) {
	tests := []struct {
		name string
		new  func() error
	}{
		{"nextcloud", func() error { _, err := NewNextcloudProvider("http://nextcloud.example.test", "user", "pass"); return err }},
		{"opencloud", func() error { _, err := NewOpenCloudProvider("http://opencloud.example.test", "user", "pass"); return err }},
		{"webdav", func() error { _, err := NewWebDAVProvider("http://webdav.example.test/dav", "user", "pass"); return err }},
		{"immich", func() error { _, err := NewImmichProvider("http://immich.example.test", "api-key"); return err }},
		{"seafile", func() error { _, err := NewSeafileProvider("http://seafile.example.test", "user", "pass"); return err }},
		{"s3 custom endpoint", func() error { _, err := NewS3Provider("s3://bucket?endpoint=http%3A%2F%2Fs3.example.test", "access-key", "secret-key"); return err }},
		{"s3 legacy insecure option", func() error { _, err := NewS3Provider("s3://bucket?endpoint=https%3A%2F%2Fs3.example.test&insecure=true", "access-key", "secret-key"); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.new(); err == nil {
				t.Fatal("expected plaintext HTTP or legacy insecure option to be rejected")
			}
		})
	}
}

func TestValidateProviderURLRequiresHTTPS(t *testing.T) {
	for _, provider := range []string{"nextcloud", "opencloud", "webdav", "immich", "seafile"} {
		if err := ValidateProviderURL(provider, "http://example.test"); err == nil {
			t.Errorf("ValidateProviderURL(%q, HTTP URL) unexpectedly succeeded", provider)
		}
	}
}
