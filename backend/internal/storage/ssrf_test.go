package storage

import (
	"context"
	"net"
	"net/http"
	"testing"
)

func TestValidateEgressURL(t *testing.T) {
	blockPrivateEgress = false
	defer func() { blockPrivateEgress = false }()

	// RFC1918/ULA are permitted by default (this tool migrates between
	// self-hosted/internal servers); loopback and link-local are always blocked.
	allowed := []string{
		"https://8.8.8.8/",               // public literal IP
		"https://10.0.0.5:8080/",         // RFC1918 permitted by default
		"https://192.168.1.10/nextcloud", // RFC1918 permitted by default
		"https://[fc00::1]/dav",          // ULA permitted by default
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

func TestSortIPsIPv4First(t *testing.T) {
	ips := []net.IP{
		net.ParseIP("2001:db8::1"),
		net.ParseIP("198.51.100.1"),
	}
	sortIPsIPv4First(ips)
	if ips[0].To4() == nil {
		t.Errorf("expected first IP to be IPv4, got %s", ips[0])
	}
}

func TestValidateEgressRedirectRejectsBlockedTarget(t *testing.T) {
	orig := blockPrivateEgress
	blockPrivateEgress = false
	defer func() { blockPrivateEgress = orig }()

	req, err := http.NewRequest(http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateEgressRedirect(req, nil); err == nil {
		t.Fatal("expected redirect to metadata address to be blocked")
	}
}

func TestEgressDialerRejectsBlockedLiteralDialAddress(t *testing.T) {
	orig := blockPrivateEgress
	blockPrivateEgress = false
	defer func() { blockPrivateEgress = orig }()

	_, err := egressDialer("8.8.8.8")(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil {
		t.Fatal("expected loopback dial address to be blocked")
	}
}

func TestSSRFProtectedClientsValidateRedirects(t *testing.T) {
	webdav, err := NewWebDAVProvider("https://8.8.8.8/dav", "user", "password")
	if err != nil {
		t.Fatal(err)
	}
	nextcloud, err := NewNextcloudProvider("https://8.8.8.8", "user", "password")
	if err != nil {
		t.Fatal(err)
	}
	immich, err := NewImmichProvider("https://8.8.8.8", "api-key")
	if err != nil {
		t.Fatal(err)
	}
	magentacloud, err := NewMagentacloudProvider("user", "password")
	if err != nil {
		t.Fatal(err)
	}
	s3Provider, err := NewS3Provider("s3://bucket?endpoint=https%3A%2F%2F8.8.8.8", "access-key", "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	s3Client, ok := s3Provider.client.Options().HTTPClient.(*http.Client)
	if !ok {
		t.Fatal("S3 provider does not use an *http.Client")
	}

	for name, client := range map[string]*http.Client{
		"webdav":       webdav.HTTPClient,
		"nextcloud":    nextcloud.HTTPClient,
		"immich":       immich.HTTPClient,
		"magentacloud": magentacloud.HTTPClient,
		"s3":           s3Client,
	} {
		if client.CheckRedirect == nil {
			t.Errorf("%s client does not validate redirect destinations", name)
		}
	}
}
