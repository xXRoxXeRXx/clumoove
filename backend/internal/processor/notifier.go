package processor

import (
	"context"
	"encoding/json"
	"log"
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
				log.Printf("[Notifier] reset cleanup failed: %v", err)
			}
			if err := db.DeleteExpiredEmailChangeTokens(p.db); err != nil {
				log.Printf("[Notifier] email-change cleanup failed: %v", err)
			}
		case <-throttle.C:
			p.cleanupThrottlers()
		}
	}
}

// RunCompletionNotifier remains as a compatibility alias for integrations.
func (p *Processor) RunCompletionNotifier(ctx context.Context) { p.RunNotifier(ctx) }

func (p *Processor) cleanupThrottlers() {
	p.throttlers.Range(func(key, value interface{}) bool {
		id := key.(string)
		mig, err := db.GetMigration(p.db, id)
		if err != nil || mig == nil {
			p.throttlers.Delete(id)
			return true
		}
		switch mig.Status {
		case "COMPLETED", "COMPLETED_WITH_ERRORS", "FAILED", "CANCELLED":
			p.throttlers.Delete(id)
		}
		return true
	})
}

func (p *Processor) sendPendingNotifications(ctx context.Context) {
	deliveries, err := db.ClaimNotificationDeliveries(p.db, 10)
	if err != nil {
		log.Printf("[Notifier] claim failed: %v", err)
		return
	}
	for _, d := range deliveries {
		plain, err := crypto.Decrypt(d.ConfigEncrypted, p.secretKey)
		if err != nil {
			_ = db.CompleteNotificationDelivery(p.db, d.ID, false, "NOTIFICATION_DECRYPT_FAILED")
			log.Printf("[Notifier] channel=%s delivery=%s failed", d.ChannelType, d.ID)
			continue
		}
		var cfg notify.Config
		if json.Unmarshal([]byte(plain), &cfg) != nil {
			_ = db.CompleteNotificationDelivery(p.db, d.ID, false, "NOTIFICATION_DECRYPT_FAILED")
			continue
		}
		err = notify.Send(ctx, d.ChannelType, cfg, d.Payload, d.RecipientEmail, d.Language)
		if err != nil {
			_ = db.CompleteNotificationDelivery(p.db, d.ID, false, "NOTIFICATION_SEND_FAILED")
			log.Printf("[Notifier] channel=%s delivery=%s send failed", d.ChannelType, d.ID)
			continue
		}
		_ = db.CompleteNotificationDelivery(p.db, d.ID, true, "")
	}
}
