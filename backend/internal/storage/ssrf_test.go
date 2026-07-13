package storage

import "testing"

func TestValidateEgressURL(t *testing.T) {
	blockPrivateEgress = false
	defer func() { blockPrivateEgress = false }()

	// RFC1918/ULA are permitted by default (this tool migrates between
	// self-hosted/internal servers); loopback and link-local are always blocked.
	allowed := []string{
		"https://8.8.8.8/",                // public literal IP
		"https://10.0.0.5:8080/",          // RFC1918 permitted by default
		"https://192.168.1.10/nextcloud",  // RFC1918 permitted by default
		"https://[fc00::1]/dav",           // ULA permitted by default
	}
	for _, u := range allowed {
		if err := validateEgressURL(u); err != nil {
			t.Errorf("expected %q to be allowed, got error: %v", u, err)
		}
	}

	blocked := []string{
		"http://127.0.0.1:9000/",
		"https://localhost/",
		"https://169.254.169.254/latest/meta-data/", // cloud metadata
		"https://[::1]/",
		"https://[fe80::1]/",
	}
	for _, u := range blocked {
		if err := validateEgressURL(u); err == nil {
			t.Errorf("expected %q to be blocked, but it was allowed", u)
		}
	}
}

func TestValidateEgressHostBlockPrivate(t *testing.T) {
	blockPrivateEgress = true
	defer func() { blockPrivateEgress = false }()

	if err := validateEgressHost("10.0.0.5"); err == nil {
		t.Errorf("expected private IP 10.0.0.5 to be blocked when MIGRATION_BLOCK_PRIVATE is set")
	}
	if err := validateEgressHost("8.8.8.8"); err != nil {
		t.Errorf("expected public IP 8.8.8.8 to be allowed, got: %v", err)
	}
}
