package processor

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

// RunNotifier drains the durable per-channel outbox. Network work happens
// after the database claim, so a slow or failed channel never blocks others.
func (p *Processor) RunNotifier(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	throttle := time.NewTicker(time.Minute)
	defer throttle.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sendPendingNotifications(ctx)
		case <-cleanup.C:
			if err := db.DeleteExpiredPasswordResetTokens(p.db); err != nil {
				processorLogf("[Notifier] reset cleanup failed: %v", err)
			}
			if err := db.DeleteExpiredEmailChangeTokens(p.db); err != nil {
				processorLogf("[Notifier] email-change cleanup failed: %v", err)
			}
		case <-throttle.C:
			p.cleanupThrottlers()
		}
	}
}

// RunCompletionNotifier remains as a compatibility alias for integrations.
func (p *Processor) RunCompletionNotifier(ctx context.Context) { p.RunNotifier(ctx) }

// shouldEvictThrottler decides whether a throttler entry may be dropped. The
// throttlers map is shared by migrations and sync jobs, so a migration miss must
// fall back to a sync-job lookup before the entry is considered orphaned. A
// transient lookup error must never evict live throttler state.
// isTerminalStatus reports whether an entity lifecycle status is terminal and
// therefore its throttler entry may be evicted. Sync jobs reuse the migration
// vocabulary for safety, so a future sync terminal state cannot silently leak a
// throttler that only the migration branch would recognize.
func isTerminalStatus(status string) bool {
	switch status {
	case "COMPLETED", "COMPLETED_WITH_ERRORS", "FAILED", "CANCELLED":
		return true
	}
	return false
}

// shouldEvictThrottler decides whether a throttler entry may be dropped. The
// throttlers map is shared by migrations and sync jobs, so a migration miss must
// fall back to a sync-job lookup before the entry is considered orphaned. A
// transient lookup error must never evict live throttler state.
func shouldEvictThrottler(migStatus string, migErr error, lookupJob func() (string, error)) bool {
	if migErr == nil {
		return isTerminalStatus(migStatus)
	}
	if !errors.Is(migErr, sql.ErrNoRows) {
		return false
	}
	jobStatus, jobErr := lookupJob()
	if jobErr == nil {
		return isTerminalStatus(jobStatus)
	}
	// Neither a migration nor a sync job: the owning entity was deleted.
	return errors.Is(jobErr, sql.ErrNoRows)
}

func (p *Processor) cleanupThrottlers() {
	p.throttlers.Range(func(key, value interface{}) bool {
		id := key.(string)
		mig, err := db.GetMigration(p.db, id)
		if shouldEvictThrottler(migStatusOrEmpty(mig, err), err, func() (string, error) {
			job, jerr := db.GetSyncJob(p.db, id)
			if jerr != nil {
				return "", jerr
			}
			return job.Status, nil
		}) {
			p.throttlers.Delete(id)
		}
		return true
	})
}

// migStatusOrEmpty returns the migration status, or the empty string when the
// migration was not found or another error occurred. Both cases collapse to ""
// here; callers must inspect err independently to tell them apart (see
// shouldEvictThrottler, which keys off errors.Is(migErr, sql.ErrNoRows)).
func migStatusOrEmpty(mig *db.Migration, err error) string {
	if err != nil || mig == nil {
		return ""
	}
	return mig.Status
}

func (p *Processor) sendPendingNotifications(ctx context.Context) {
	deliveries, err := db.ClaimNotificationDeliveries(p.db, 10)
	if err != nil {
		processorLogf("[Notifier] claim failed: %v", err)
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
				p.sendNotificationDelivery(ctx, d)
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
func (p *Processor) sendNotificationDelivery(ctx context.Context, d db.NotificationDelivery) {
	if d.ChannelType == "email" {
		settings, err := db.GetInstanceSMTPSettings(p.db)
		if err != nil {
			_ = db.CompleteNotificationDelivery(p.db, d.ID, false, "SMTP_NOT_CONFIGURED")
			return
		}
		if err := withDecryptedNotificationSecret(settings.SMTPPasswordEnc, p.secretKey, func(password string) error {
			cfg := notify.Config{
				"smtp_host":       settings.SMTPHost,
				"smtp_port":       settings.SMTPPort,
				"smtp_username":   settings.SMTPUsername,
				"smtp_password":   password,
				"smtp_from_email": settings.SMTPFromEmail,
				"smtp_from_name":  settings.SMTPFromName,
				"smtp_encryption": settings.SMTPEncryption,
			}
			return notify.Send(ctx, d.ChannelType, cfg, d.Payload, d.RecipientEmail, d.Language)
		}); err != nil {
			if errors.Is(err, errNotificationDecryptFailed) {
				_ = db.CompleteNotificationDelivery(p.db, d.ID, false, "SMTP_DECRYPT_FAILED")
			} else {
				_ = db.CompleteNotificationDelivery(p.db, d.ID, false, "NOTIFICATION_SEND_FAILED")
			}
			return
		}
		_ = db.CompleteNotificationDelivery(p.db, d.ID, true, "")
		return
	}
	if err := withDecryptedNotificationSecret(d.ConfigEncrypted, p.secretKey, func(plain string) error {
		cfg, err := decodeNotificationConfig(plain)
		if err != nil {
			return err
		}
		return notify.Send(ctx, d.ChannelType, cfg, d.Payload, d.RecipientEmail, d.Language)
	}); err != nil {
		if errors.Is(err, errNotificationDecryptFailed) {
			_ = db.CompleteNotificationDelivery(p.db, d.ID, false, "NOTIFICATION_DECRYPT_FAILED")
			// A decryptable but malformed configuration is security-relevant: it
			// cannot be delivered and may indicate corrupted persisted state.
			processorLogf("[Notifier] channel=%s delivery=%s failed", d.ChannelType, d.ID)
			return
		}
		_ = db.CompleteNotificationDelivery(p.db, d.ID, false, "NOTIFICATION_SEND_FAILED")
		processorLogf("[Notifier] channel=%s delivery=%s send failed", d.ChannelType, d.ID)
		return
	}
	_ = db.CompleteNotificationDelivery(p.db, d.ID, true, "")
}

var errNotificationDecryptFailed = errors.New("notification secret decryption failed")

// withDecryptedNotificationSecret passes plaintext to a synchronous callback.
// The temporary GCM plaintext buffer is cleared before it is converted to the
// string required by the notification integration. The callback must not
// retain the string or a value derived from it beyond its invocation.
func withDecryptedNotificationSecret(encrypted, secretKey string, use func(string) error) error {
	secret, err := crypto.DecryptWithDomain(encrypted, secretKey, crypto.DomainNotificationConfig)
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
