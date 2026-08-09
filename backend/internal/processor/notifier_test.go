package processor

import (
	"errors"
	"testing"

	"backend/internal/crypto"
)

func TestWithDecryptedNotificationSecretZeroesAfterUse(t *testing.T) {
	const key = "test-notification-encryption-key"
	ciphertext, err := crypto.Encrypt("notification-secret", key)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
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
			var retained *string
			err := withDecryptedNotificationSecret(ciphertext, key, func(secret *string) error {
				retained = secret
				if got := *secret; got != "notification-secret" {
					t.Fatalf("callback secret = %q, want decrypted secret", got)
				}
				return tt.useErr
			})
			if !errors.Is(err, tt.useErr) {
				t.Fatalf("error = %v, want %v", err, tt.useErr)
			}
			if retained == nil {
				t.Fatal("callback did not receive the decrypted secret")
			}
			if got := *retained; got != "" {
				t.Fatalf("decrypted secret was not zeroed after callback: %q", got)
			}
		})
	}
}

func TestWithDecryptedNotificationSecretDoesNotUseOnDecryptFailure(t *testing.T) {
	called := false
	err := withDecryptedNotificationSecret("not-ciphertext", "test-notification-encryption-key", func(secret *string) error {
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
	ciphertext, err := crypto.Encrypt(`{"url":`, key)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	var retained *string
	err = withDecryptedNotificationSecret(ciphertext, key, func(plain *string) error {
		retained = plain
		_, err := decodeNotificationConfig(*plain)
		return err
	})
	if !errors.Is(err, errNotificationDecryptFailed) {
		t.Fatalf("error = %v, want errNotificationDecryptFailed", err)
	}
	if retained == nil {
		t.Fatal("callback did not receive the decrypted configuration")
	}
	if got := *retained; got != "" {
		t.Fatalf("decrypted config was not zeroed after callback: %q", got)
	}
}
