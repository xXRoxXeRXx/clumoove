package processor

import (
	"context"
	"database/sql"
	"log"
	"net/mail"
	"os"
	"strconv"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/email"
)

func (p *Processor) RunCompletionNotifier(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	cleanupTicker := time.NewTicker(1 * time.Hour)
	defer cleanupTicker.Stop()

	throttleCleanupTicker := time.NewTicker(1 * time.Minute)
	defer throttleCleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sendPendingCompletionEmails(ctx)
		case <-cleanupTicker.C:
			if err := db.DeleteExpiredPasswordResetTokens(p.db); err != nil {
				log.Printf("[CompletionNotifier] Error cleaning up expired reset tokens: %v\n", err)
			}
			if err := db.DeleteExpiredEmailChangeTokens(p.db); err != nil {
				log.Printf("[CompletionNotifier] Error cleaning up expired email change tokens: %v\n", err)
			}
		case <-throttleCleanupTicker.C:
			p.cleanupThrottlers()
		}
	}
}

func (p *Processor) cleanupThrottlers() {
	p.throttlers.Range(func(key, value interface{}) bool {
		migrationID := key.(string)
		mig, err := db.GetMigration(p.db, migrationID)
		if err != nil || mig == nil {
			p.throttlers.Delete(migrationID)
			return true
		}
		switch mig.Status {
		case "COMPLETED", "COMPLETED_WITH_ERRORS", "FAILED", "CANCELLED":
			p.throttlers.Delete(migrationID)
		}
		return true
	})
}

func (p *Processor) sendPendingCompletionEmails(ctx context.Context) {
	for claimed := 0; claimed < 10; claimed++ {
		tx, notifs, err := db.LockPendingEmailNotifications(p.db, 1)
		if err != nil {
			log.Printf("[CompletionNotifier] Error claiming pending notification: %v\n", err)
			return
		}
		if len(notifs) == 0 {
			_ = tx.Rollback()
			break
		}
		n := notifs[0]
		if err := p.sendCompletionEmail(tx, n); err != nil {
			log.Printf("[CompletionNotifier] Transient failure for migration %s, will retry: %v\n", n.MigrationID, err)
		}
		if err := tx.Commit(); err != nil {
			log.Printf("[CompletionNotifier] Error committing email claim for migration %s: %v\n", n.MigrationID, err)
		}
	}
}

func (p *Processor) sendCompletionEmail(tx *sql.Tx, n db.PendingEmailNotification) error {
	settings, err := db.GetUserSMTPSettings(p.db, n.UserID)
	if err != nil {
		if err == sql.ErrNoRows {
			return db.MarkMigrationEmailSentTx(tx, n.MigrationID)
		}
		return err
	}

	if !settings.NotifyOnCompletion {
		return db.MarkMigrationEmailSentTx(tx, n.MigrationID)
	}

	password, err := crypto.Decrypt(settings.SMTPPasswordEnc, p.secretKey)
	if err != nil {
		log.Printf("[CompletionNotifier] Error decrypting SMTP password for user %s: %v\n", n.UserID, err)
		return db.MarkMigrationEmailSentTx(tx, n.MigrationID)
	}

	smtpCfg := email.SMTPConfig{
		Host:       settings.SMTPHost,
		Port:       strconv.Itoa(settings.SMTPPort),
		Username:   settings.SMTPUsername,
		Password:   password,
		FromEmail:  settings.SMTPFromEmail,
		FromName:   settings.SMTPFromName,
		Encryption: settings.SMTPEncryption,
	}

	if smtpCfg.Host == "" {
		smtpCfg.Host = os.Getenv("SMTP_HOST")
		smtpCfg.Port = os.Getenv("SMTP_PORT")
		smtpCfg.Username = os.Getenv("SMTP_USER")
		smtpCfg.Password = os.Getenv("SMTP_PASS")
		smtpCfg.FromEmail = os.Getenv("SMTP_FROM")
		smtpCfg.FromName = os.Getenv("SMTP_FROM_NAME")
		smtpCfg.Encryption = os.Getenv("SMTP_ENCRYPTION")
	}

	if smtpCfg.Host == "" || smtpCfg.FromEmail == "" {
		log.Printf("[CompletionNotifier] Skipping completion mail for migration %s: no SMTP server configured\n", n.MigrationID)
		return db.MarkMigrationEmailSentTx(tx, n.MigrationID)
	}

	user, err := db.GetUserByID(p.db, n.UserID)
	if err != nil {
		return err
	}

	if _, err := mail.ParseAddress(user.Email); err != nil {
		log.Printf("[CompletionNotifier] Invalid recipient address %q for migration %s: %v\n", user.Email, n.MigrationID, err)
		return db.MarkMigrationEmailSentTx(tx, n.MigrationID)
	}

	errMsg := ""
	if n.ErrorMessage.Valid {
		errMsg = n.ErrorMessage.String
	}

	htmlBody := email.BuildMigrationReportEmail(
		n.MigrationID, n.Status,
		n.TotalFiles, n.ProcessedFiles, n.FailedFiles, n.SkippedFiles,
		n.TotalBytes, n.ProcessedBytes, errMsg,
	)

	if err := email.SendMail(smtpCfg, user.Email, "Clumoove — Migrationsbericht", htmlBody); err != nil {
		return err
	}

	log.Printf("[CompletionNotifier] Sent completion email for migration %s to %s\n", n.MigrationID, user.Email)
	return db.MarkMigrationEmailSentTx(tx, n.MigrationID)
}
