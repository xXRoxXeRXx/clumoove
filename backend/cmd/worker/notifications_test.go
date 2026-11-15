package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/notify"
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
			err := withDecryptedNotificationSecret(ciphertext, key, crypto.DomainNotificationConfig, func(secret string) error {
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
	err := withDecryptedNotificationSecret("not-ciphertext", "test-notification-encryption-key", crypto.DomainNotificationConfig, func(secret string) error {
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

	err = withDecryptedNotificationSecret(ciphertext, key, crypto.DomainNotificationConfig, func(plain string) error {
		_, err := decodeNotificationConfig(plain)
		return err
	})
	if !errors.Is(err, errNotificationDecryptFailed) {
		t.Fatalf("error = %v, want errNotificationDecryptFailed", err)
	}
}

func TestNotifierDeliversAndClassifiesChannelOutcomes(t *testing.T) {
	const key = "test-notification-encryption-key"
	validCfgEnc, err := crypto.EncryptWithDomain(`{"url":"https://ntfy.example/topic"}`, key, crypto.DomainNotificationConfig)
	if err != nil {
		t.Fatalf("EncryptWithDomain() error = %v", err)
	}
	smtpPassEnc, err := crypto.EncryptWithDomain("smtp-secret", key, crypto.DomainSMTPPassword)
	if err != nil {
		t.Fatalf("EncryptWithDomain() error = %v", err)
	}

	type completion struct {
		id      string
		success bool
		errCode string
	}
	var completions []completion

	n := newNotifier(nil, key)
	n.getSMTPSettings = func() (*db.InstanceSMTPSettings, error) {
		return &db.InstanceSMTPSettings{
			SMTPHost:        "smtp.example",
			SMTPPort:        587,
			SMTPUsername:    "mailer",
			SMTPPasswordEnc: smtpPassEnc,
			SMTPFromEmail:   "noreply@example.com",
			SMTPEncryption:  "starttls",
		}, nil
	}
	n.completeDelivery = func(id string, success bool, errCode string) error {
		completions = append(completions, completion{id: id, success: success, errCode: errCode})
		return nil
	}

	var sentChannels []string
	n.send = func(_ context.Context, channelType string, cfg notify.Config, payload json.RawMessage, recipientEmail, language string) error {
		sentChannels = append(sentChannels, channelType)
		if channelType == "email" && cfg["smtp_password"] != "smtp-secret" {
			t.Fatalf("smtp_password = %v, want decrypted password", cfg["smtp_password"])
		}
		if channelType == "webhook_fail" {
			return errors.New("network failure")
		}
		return nil
	}

	n.sendNotificationDelivery(context.Background(), db.NotificationDelivery{
		ID:             "del-email",
		ChannelType:    "email",
		Payload:        json.RawMessage(`{"status":"COMPLETED"}`),
		RecipientEmail: "user@example.com",
		Language:       "en",
	})
	n.sendNotificationDelivery(context.Background(), db.NotificationDelivery{
		ID:              "del-ntfy",
		ChannelType:     "ntfy",
		ConfigEncrypted: validCfgEnc,
		Payload:         json.RawMessage(`{"status":"COMPLETED"}`),
	})
	n.sendNotificationDelivery(context.Background(), db.NotificationDelivery{
		ID:              "del-bad-decrypt",
		ChannelType:     "ntfy",
		ConfigEncrypted: "corrupt",
	})
	n.sendNotificationDelivery(context.Background(), db.NotificationDelivery{
		ID:              "del-send-err",
		ChannelType:     "webhook_fail",
		ConfigEncrypted: validCfgEnc,
	})

	want := []completion{
		{id: "del-email", success: true, errCode: ""},
		{id: "del-ntfy", success: true, errCode: ""},
		{id: "del-bad-decrypt", success: false, errCode: "NOTIFICATION_DECRYPT_FAILED"},
		{id: "del-send-err", success: false, errCode: "NOTIFICATION_SEND_FAILED"},
	}
	if len(completions) != len(want) {
		t.Fatalf("completions = %+v, want %+v", completions, want)
	}
	for i := range want {
		if completions[i] != want[i] {
			t.Fatalf("completion[%d] = %+v, want %+v", i, completions[i], want[i])
		}
	}
	if len(sentChannels) != 3 {
		t.Fatalf("sentChannels = %v, want 3 send calls", sentChannels)
	}
}

func TestNotifierRunReturnsContextErrOnCancellation(t *testing.T) {
	n := newNotifier(nil, "key")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- n.Run(ctx)
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("notifier.Run did not stop on context cancellation")
	}
}
