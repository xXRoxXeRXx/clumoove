package storage

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const testSFTPHostKeyFingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestNewSFTPProviderValid(t *testing.T) {
	p, err := NewSFTPProvider("sftp://example.com:2222/?host_key="+testSFTPHostKeyFingerprint, "user", "pass")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil SFTPProvider")
	}
	if p.Host != "example.com" || p.Port != "2222" {
		t.Errorf("expected host example.com port 2222, got host %s port %s", p.Host, p.Port)
	}
	if !p.SupportsAtomicRename() {
		t.Error("expected SupportsAtomicRename() = true")
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestNewSFTPProviderDefaultPort(t *testing.T) {
	p, err := NewSFTPProvider("sftp://10.0.0.1/?host_key="+testSFTPHostKeyFingerprint, "user", "pass")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p.Port != "22" {
		t.Errorf("expected default port 22, got %s", p.Port)
	}
}

func TestNewSFTPProviderPrivateKey(t *testing.T) {
	mockKey := "-----BEGIN OPENSSH PRIVATE KEY-----\nmock\n-----END OPENSSH PRIVATE KEY-----"
	p, err := NewSFTPProvider("sftp://example.com/?host_key="+testSFTPHostKeyFingerprint, "user", mockKey)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p.PrivateKey != mockKey {
		t.Errorf("expected PrivateKey to be populated")
	}
	if p.Password != "" {
		t.Errorf("expected Password to be emptied when PrivateKey is provided")
	}
}

func TestNewSFTPProviderRequiresValidHostKeyFingerprint(t *testing.T) {
	for _, rawURL := range []string{
		"sftp://example.com/",
		"sftp://example.com/?host_key=SHA256:not-base64",
		"sftp://example.com/?host_key=SHA256:abc",
		"sftp://example.com/?host_key=MD5:aa:bb",
	} {
		if _, err := NewSFTPProvider(rawURL, "user", "pass"); err == nil {
			t.Errorf("NewSFTPProvider(%q) succeeded without a valid fingerprint", rawURL)
		}
	}
}

func TestParseSFTPHostKeyFingerprint(t *testing.T) {
	want := sha256.Sum256([]byte("host key"))
	fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(want[:])
	got, err := parseSFTPHostKeyFingerprint(fingerprint)
	if err != nil {
		t.Fatalf("parseSFTPHostKeyFingerprint returned error: %v", err)
	}
	if got != want {
		t.Errorf("parsed fingerprint = %x, want %x", got, want)
	}
}

func TestSFTPProviderVerifyHostKey(t *testing.T) {
	trustedKey := mustSFTPPublicKey(t, "trusted host key")
	fingerprint := ssh.FingerprintSHA256(trustedKey)
	p, err := NewSFTPProvider("sftp://example.com/?host_key="+url.QueryEscape(fingerprint), "user", "pass")
	if err != nil {
		t.Fatalf("NewSFTPProvider returned error: %v", err)
	}

	if err := p.verifyHostKey("example.com:22", nil, trustedKey); err != nil {
		t.Fatalf("verifyHostKey rejected the configured host key: %v", err)
	}
	if err := p.verifyHostKey("example.com:22", nil, mustSFTPPublicKey(t, "untrusted host key")); err == nil {
		t.Fatal("verifyHostKey accepted an unconfigured host key")
	}
}

func mustSFTPPublicKey(t *testing.T, key string) ssh.PublicKey {
	t.Helper()
	seed := sha256.Sum256([]byte(key))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey, err := ssh.NewPublicKey(privateKey.Public())
	if err != nil {
		t.Fatalf("ssh.NewPublicKey returned error: %v", err)
	}
	return publicKey
}

func TestIsSFTPAuthError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password]"), true},
		{errors.New("permission denied"), true},
		{errors.New("sftp connect: authentication failed"), true},
		{errors.New("file does not exist"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isSFTPAuthError(c.err); got != c.want {
			t.Errorf("isSFTPAuthError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestSFTPProviderNonFilesRejected(t *testing.T) {
	p, err := NewSFTPProvider("sftp://example.com/?host_key="+testSFTPHostKeyFingerprint, "user", "pass")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	ctx := context.Background()
	invalidTypes := []string{"calendars", "contacts", "invalid"}

	for _, resourceType := range invalidTypes {
		if _, err := p.GetDirectoryListing(ctx, resourceType, "/"); err == nil {
			t.Errorf("GetDirectoryListing: expected error for resourceType %q, got nil", resourceType)
		}
		if _, err := p.InspectResource(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("InspectResource: expected error for resourceType %q, got nil", resourceType)
		}
		if _, err := p.StreamDownload(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("StreamDownload: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.StreamUpload(ctx, resourceType, "/test.txt", nil, 0); err == nil {
			t.Errorf("StreamUpload: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.StreamUploadChunked(ctx, resourceType, "/test.txt", nil, 0, nil); err == nil {
			t.Errorf("StreamUploadChunked: expected error for resourceType %q, got nil", resourceType)
		}
		if _, _, err := p.FileExists(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("FileExists: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.DeleteFile(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("DeleteFile: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.RenameFile(ctx, resourceType, "/old.txt", "/new.txt"); err == nil {
			t.Errorf("RenameFile: expected error for resourceType %q, got nil", resourceType)
		}
		if _, err := p.GetFileHash(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("GetFileHash: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.CreateParentDirectories(ctx, resourceType, "/dir/test.txt"); err == nil {
			t.Errorf("CreateParentDirectories: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.CreateDirectory(ctx, resourceType, "/dir"); err == nil {
			t.Errorf("CreateDirectory: expected error for resourceType %q, got nil", resourceType)
		}
	}
}

func TestSFTPProviderCancelledContextDoesNotWaitForSession(t *testing.T) {
	p, err := NewSFTPProvider("sftp://example.com/?host_key="+testSFTPHostKeyFingerprint, "user", "pass")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	if err := p.lock(context.Background()); err != nil {
		t.Fatalf("failed to acquire session lock: %v", err)
	}
	defer p.unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.lock(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("lock() error = %v, want context.Canceled", err)
	}
}

func TestSFTPConnectReturnsCancelledContext(t *testing.T) {
	p, err := NewSFTPProvider("sftp://example.com/?host_key="+testSFTPHostKeyFingerprint, "user", "pass")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ok, err := p.Connect(ctx)
	if ok {
		t.Fatal("Connect succeeded with a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect() error = %v, want context.Canceled", err)
	}
}

func TestSFTPHandshakeDeadlineUsesSoonerContextDeadline(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Second))
	defer cancel()

	if got, want := sftpHandshakeDeadline(ctx, now), now.Add(time.Second); !got.Equal(want) {
		t.Fatalf("sftpHandshakeDeadline() = %v, want %v", got, want)
	}
}

func TestSFTPHandshakeDeadlineCapsConnectionSetup(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(2*sftpConnectTimeout))
	defer cancel()

	if got, want := sftpHandshakeDeadline(ctx, now), now.Add(sftpConnectTimeout); !got.Equal(want) {
		t.Fatalf("sftpHandshakeDeadline() = %v, want %v", got, want)
	}
}

func TestSFTPHandshakeDeadlineUsesConnectionTimeoutWithoutContextDeadline(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)

	if got, want := sftpHandshakeDeadline(context.Background(), now), now.Add(sftpConnectTimeout); !got.Equal(want) {
		t.Fatalf("sftpHandshakeDeadline() = %v, want %v", got, want)
	}
}
