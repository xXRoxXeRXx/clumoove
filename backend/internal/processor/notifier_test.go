package processor

import (
	"errors"
	"testing"

	"backend/internal/crypto"
)

func TestWithDecryptedNotificationSecretPassesPlaintextToCallback(t *testing.T) {
	const key = "test-notification-encryption-key"
	ciphertext, err := crypto.EncryptWithDomain("notification-secret", key, crypto.DomainNotificationConfig)
	if err != nil {
		t.Fatalf("EncryptWithDomain() error = %v", err)
	}

	tests := []struct {
		name   string
		useErr error
	}{
		{name: "send succeeds"},
		{name: "send fails", useErr: errors.New("send failed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := withDecryptedNotificationSecret(ciphertext, key, func(secret string) error {
				if secret != "notification-secret" {
					t.Fatalf("callback secret = %q, want decrypted secret", secret)
				}
				return tt.useErr
			})
			if !errors.Is(err, tt.useErr) {
				t.Fatalf("error = %v, want %v", err, tt.useErr)
			}
		})
	}
}

func TestWithDecryptedNotificationSecretDoesNotUseOnDecryptFailure(t *testing.T) {
	called := false
	err := withDecryptedNotificationSecret("not-ciphertext", "test-notification-encryption-key", func(secret string) error {
		called = true
		return nil
	})
	if !errors.Is(err, errNotificationDecryptFailed) {
		t.Fatalf("error = %v, want errNotificationDecryptFailed", err)
	}
	if called {
		t.Fatal("callback was invoked after decrypt failure")
	}
}

func TestWithDecryptedNotificationSecretClassifiesMalformedConfigAsDecryptFailure(t *testing.T) {
	const key = "test-notification-encryption-key"
	ciphertext, err := crypto.EncryptWithDomain(`{"url":`, key, crypto.DomainNotificationConfig)
	if err != nil {
		t.Fatalf("EncryptWithDomain() error = %v", err)
	}

	err = withDecryptedNotificationSecret(ciphertext, key, func(plain string) error {
		_, err := decodeNotificationConfig(plain)
		return err
	})
	if !errors.Is(err, errNotificationDecryptFailed) {
		t.Fatalf("error = %v, want errNotificationDecryptFailed", err)
	}
}
