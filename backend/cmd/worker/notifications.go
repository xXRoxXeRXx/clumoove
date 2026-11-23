package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/notify"
)

// notifier drains the durable per-channel outbox and cleans expired account
// verification tokens. Network work runs after the database claim, so a slow
// or failed channel never blocks others.
type notifier struct {
	db               *sql.DB
	tokens           db.AuthTokenStore
	secretKey        string
	send             func(ctx context.Context, channelType string, cfg notify.Config, payload json.RawMessage, recipientEmail, language string) error
	getSMTPSettings  func() (*db.InstanceSMTPSettings, error)
	completeDelivery func(id string, success bool, errCode string) error
}

func newNotifier(database *sql.DB, secretKey string) *notifier {
	var tokens db.AuthTokenStore
	if database != nil {
		tokens = db.NewAuthTokenStore(database)
	}
	return &notifier{
		db:        database,
		tokens:    tokens,
		secretKey: secretKey,
		send:      notify.Send,
		getSMTPSettings: func() (*db.InstanceSMTPSettings, error) {
			return db.NewSettingsStore(database).GetInstanceSMTPSettings()
		},
		completeDelivery: func(id string, success bool, errCode string) error {
			return db.CompleteNotificationDelivery(database, id, success, errCode)
		},
	}
}

func (n *notifier) Run(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			n.sendPendingNotifications(ctx)
		case <-cleanup.C:
			if err := n.tokens.DeleteExpiredPasswordResetTokens(); err != nil {
				workerLogf("notifier", "[Notifier] reset cleanup failed: %v", err)
			}
			if err := n.tokens.DeleteExpiredEmailChangeTokens(); err != nil {
				workerLogf("notifier", "[Notifier] email-change cleanup failed: %v", err)
			}
		}
	}
}

func (n *notifier) sendPendingNotifications(ctx context.Context) {
	deliveries, err := db.ClaimNotificationDeliveries(n.db, 10)
	if err != nil {
		workerLogf("notifier", "[Notifier] claim failed: %v", err)
		return
	}
	const notificationWorkers = 3
	jobs := make(chan db.NotificationDelivery)
	var wg sync.WaitGroup
	for i := 0; i < notificationWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range jobs {
				n.sendNotificationDelivery(ctx, d)
			}
		}()
	}
	for _, d := range deliveries {
		jobs <- d
	}
	close(jobs)
	wg.Wait()
}

// sendNotificationDelivery handles one independently claimed delivery. The
// caller runs a bounded worker pool so a slow channel cannot block the batch.
func (n *notifier) sendNotificationDelivery(ctx context.Context, d db.NotificationDelivery) {
	if d.ChannelType == "email" {
		settings, err := n.getSMTPSettings()
		if err != nil {
			_ = n.completeDelivery(d.ID, false, "SMTP_NOT_CONFIGURED")
			return
		}
		if err := withDecryptedNotificationSecret(settings.SMTPPasswordEnc, n.secretKey, crypto.DomainSMTPPassword, func(password string) error {
			cfg := notify.Config{
				"smtp_host":       settings.SMTPHost,
				"smtp_port":       settings.SMTPPort,
				"smtp_username":   settings.SMTPUsername,
				"smtp_password":   password,
				"smtp_from_email": settings.SMTPFromEmail,
				"smtp_from_name":  settings.SMTPFromName,
				"smtp_encryption": settings.SMTPEncryption,
			}
			return n.send(ctx, d.ChannelType, cfg, d.Payload, d.RecipientEmail, d.Language)
		}); err != nil {
			if errors.Is(err, errNotificationDecryptFailed) {
				_ = n.completeDelivery(d.ID, false, "SMTP_DECRYPT_FAILED")
			} else {
				_ = n.completeDelivery(d.ID, false, "NOTIFICATION_SEND_FAILED")
			}
			return
		}
		_ = n.completeDelivery(d.ID, true, "")
		return
	}
	if err := withDecryptedNotificationSecret(d.ConfigEncrypted, n.secretKey, crypto.DomainNotificationConfig, func(plain string) error {
		cfg, err := decodeNotificationConfig(plain)
		if err != nil {
			return err
		}
		return n.send(ctx, d.ChannelType, cfg, d.Payload, d.RecipientEmail, d.Language)
	}); err != nil {
		if errors.Is(err, errNotificationDecryptFailed) {
			_ = n.completeDelivery(d.ID, false, "NOTIFICATION_DECRYPT_FAILED")
			// A decryptable but malformed configuration is security-relevant: it
			// cannot be delivered and may indicate corrupted persisted state.
			workerLogf("notifier", "[Notifier] channel=%s delivery=%s failed", d.ChannelType, d.ID)
			return
		}
		_ = n.completeDelivery(d.ID, false, "NOTIFICATION_SEND_FAILED")
		workerLogf("notifier", "[Notifier] channel=%s delivery=%s send failed", d.ChannelType, d.ID)
		return
	}
	_ = n.completeDelivery(d.ID, true, "")
}

var errNotificationDecryptFailed = errors.New("notification secret decryption failed")

// withDecryptedNotificationSecret passes plaintext to a synchronous callback.
// The temporary GCM plaintext buffer is cleared before it is converted to the
// string required by the notification integration. The callback must not
// retain the string or a value derived from it beyond its invocation.
func withDecryptedNotificationSecret(encrypted, secretKey, domain string, use func(string) error) error {
	secret, err := crypto.DecryptWithDomain(encrypted, secretKey, domain)
	if err != nil {
		return errNotificationDecryptFailed
	}
	return use(secret)
}

func decodeNotificationConfig(plain string) (notify.Config, error) {
	var cfg notify.Config
	if err := json.Unmarshal([]byte(plain), &cfg); err != nil {
		return nil, errNotificationDecryptFailed
	}
	return cfg, nil
}
