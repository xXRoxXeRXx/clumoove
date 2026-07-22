package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type RefreshToken struct {
	TokenHash string    `json:"token_hash"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

type UserSMTPSettings struct {
	UserID             string    `json:"user_id"`
	SMTPHost           string    `json:"smtp_host"`
	SMTPPort           int       `json:"smtp_port"`
	SMTPUsername       string    `json:"smtp_username"`
	SMTPPasswordEnc    string    `json:"-"`
	SMTPFromEmail      string    `json:"smtp_from_email"`
	SMTPFromName       string    `json:"smtp_from_name"`
	SMTPEncryption     string    `json:"smtp_encryption"`
	NotifyOnCompletion bool      `json:"notify_on_completion"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type PasswordResetToken struct {
	TokenHash string    `json:"token_hash"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
	CreatedAt time.Time `json:"created_at"`
}

type EmailChangeToken struct {
	TokenHash string    `json:"token_hash"`
	UserID    string    `json:"user_id"`
	NewEmail  string    `json:"new_email"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
	CreatedAt time.Time `json:"created_at"`
}

var ErrEmailTaken = errors.New("email already taken")

func GetSetting(db *sql.DB, key string) (string, error) {
	var val string
	query := `SELECT value FROM settings WHERE key = $1`
	err := db.QueryRow(query, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

func SetSetting(db *sql.DB, key, value string) error {
	query := `
		INSERT INTO settings (key, value, updated_at)
		VALUES ($1, $2, CURRENT_TIMESTAMP)
		ON CONFLICT (key) DO UPDATE
		SET value = EXCLUDED.value, updated_at = CURRENT_TIMESTAMP
	`
	_, err := db.Exec(query, key, value)
	return err
}

func StoreRefreshToken(db *sql.DB, tokenHash string, userID string, expiresAt time.Time) error {
	query := `
		INSERT INTO refresh_tokens (token_hash, user_id, expires_at)
		VALUES ($1, $2, $3)
	`
	_, err := db.Exec(query, tokenHash, userID, expiresAt)
	return err
}

func DeleteRefreshToken(db *sql.DB, tokenHash string) error {
	query := `DELETE FROM refresh_tokens WHERE token_hash = $1`
	_, err := db.Exec(query, tokenHash)
	return err
}

func DeleteAllRefreshTokensForUser(db *sql.DB, userID string) error {
	query := `DELETE FROM refresh_tokens WHERE user_id = $1`
	_, err := db.Exec(query, userID)
	return err
}

func GetUserIDByRefreshToken(db *sql.DB, tokenHash string) (string, error) {
	query := `
		SELECT user_id FROM refresh_tokens 
		WHERE token_hash = $1 AND expires_at > $2
	`
	var userID string
	err := db.QueryRow(query, tokenHash, time.Now()).Scan(&userID)
	if err != nil {
		return "", err
	}
	return userID, nil
}

func GetUserSMTPSettings(db *sql.DB, userID string) (*UserSMTPSettings, error) {
	query := `
		SELECT user_id, smtp_host, smtp_port, smtp_username, smtp_password_encrypted,
		       smtp_from_email, smtp_from_name, smtp_encryption, notify_on_completion, updated_at
		FROM user_smtp_settings WHERE user_id = $1
	`
	var s UserSMTPSettings
	err := db.QueryRow(query, userID).Scan(
		&s.UserID, &s.SMTPHost, &s.SMTPPort, &s.SMTPUsername, &s.SMTPPasswordEnc,
		&s.SMTPFromEmail, &s.SMTPFromName, &s.SMTPEncryption, &s.NotifyOnCompletion, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func UpsertUserSMTPSettings(db *sql.DB, s *UserSMTPSettings) error {
	query := `
		INSERT INTO user_smtp_settings (
			user_id, smtp_host, smtp_port, smtp_username, smtp_password_encrypted,
			smtp_from_email, smtp_from_name, smtp_encryption, notify_on_completion
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (user_id) DO UPDATE SET
			smtp_host = EXCLUDED.smtp_host,
			smtp_port = EXCLUDED.smtp_port,
			smtp_username = EXCLUDED.smtp_username,
			smtp_password_encrypted = EXCLUDED.smtp_password_encrypted,
			smtp_from_email = EXCLUDED.smtp_from_email,
			smtp_from_name = EXCLUDED.smtp_from_name,
			smtp_encryption = EXCLUDED.smtp_encryption,
			notify_on_completion = EXCLUDED.notify_on_completion,
			updated_at = CURRENT_TIMESTAMP
	`
	_, err := db.Exec(query,
		s.UserID, s.SMTPHost, s.SMTPPort, s.SMTPUsername, s.SMTPPasswordEnc,
		s.SMTPFromEmail, s.SMTPFromName, s.SMTPEncryption, s.NotifyOnCompletion,
	)
	return err
}

func CreatePasswordResetToken(db *sql.DB, tokenHash, userID string, expiresAt time.Time) error {
	query := `
		INSERT INTO password_reset_tokens (token_hash, user_id, expires_at)
		VALUES ($1, $2, $3)
	`
	_, err := db.Exec(query, tokenHash, userID, expiresAt)
	return err
}

func GetPasswordResetToken(db *sql.DB, tokenHash string) (*PasswordResetToken, error) {
	query := `
		SELECT token_hash, user_id, expires_at, used, created_at
		FROM password_reset_tokens WHERE token_hash = $1
	`
	var t PasswordResetToken
	err := db.QueryRow(query, tokenHash).Scan(&t.TokenHash, &t.UserID, &t.ExpiresAt, &t.Used, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func ClaimPasswordResetToken(db *sql.DB, ctx context.Context, tokenHash, newPasswordHash string) (string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var userID string
	err = tx.QueryRow(`
		UPDATE password_reset_tokens
		SET used = TRUE
		WHERE token_hash = $1 AND used = FALSE AND expires_at > NOW()
		RETURNING user_id
	`, tokenHash).Scan(&userID)
	if err != nil {
		return "", err
	}

	if _, err := tx.Exec(`UPDATE users SET password_hash = $1 WHERE id = $2`, newPasswordHash, userID); err != nil {
		return "", err
	}

	if _, err := tx.Exec(`DELETE FROM refresh_tokens WHERE user_id = $1`, userID); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return userID, nil
}

func DeleteExpiredPasswordResetTokens(db *sql.DB) error {
	query := `DELETE FROM password_reset_tokens WHERE expires_at < NOW() OR used = TRUE`
	_, err := db.Exec(query)
	return err
}

func CreateEmailChangeToken(db *sql.DB, tokenHash, userID, newEmail string, expiresAt time.Time) error {
	if _, err := db.Exec(`DELETE FROM email_change_tokens WHERE user_id = $1`, userID); err != nil {
		return err
	}
	query := `
		INSERT INTO email_change_tokens (token_hash, user_id, new_email, expires_at)
		VALUES ($1, $2, $3, $4)
	`
	_, err := db.Exec(query, tokenHash, userID, newEmail, expiresAt)
	return err
}

func ClaimEmailChangeToken(db *sql.DB, ctx context.Context, tokenHash string) (userID, newEmail string, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()

	var uid, newMail string
	err = tx.QueryRow(`
		SELECT user_id, new_email
		FROM email_change_tokens
		WHERE token_hash = $1 AND used = FALSE AND expires_at > NOW()
	`, tokenHash).Scan(&uid, &newMail)
	if err != nil {
		return "", "", err
	}

	var taken string
	err = tx.QueryRow(`SELECT id FROM users WHERE email = $1 AND id <> $2 LIMIT 1`, newMail, uid).Scan(&taken)
	if err == nil {
		return "", "", ErrEmailTaken
	}
	if err != sql.ErrNoRows {
		return "", "", err
	}

	if _, err := tx.Exec(`
		UPDATE email_change_tokens
		SET used = TRUE
		WHERE token_hash = $1
	`, tokenHash); err != nil {
		return "", "", err
	}

	if _, err := tx.Exec(`UPDATE users SET email = $1 WHERE id = $2`, newMail, uid); err != nil {
		return "", "", err
	}

	if _, err := tx.Exec(`DELETE FROM refresh_tokens WHERE user_id = $1`, uid); err != nil {
		return "", "", err
	}

	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return uid, newMail, nil
}

func DeleteExpiredEmailChangeTokens(db *sql.DB) error {
	query := `DELETE FROM email_change_tokens WHERE expires_at < NOW() OR used = TRUE`
	_, err := db.Exec(query)
	return err
}
