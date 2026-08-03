package db

import (
	"context"
	"database/sql"
	"errors"
)

// ClaimMigrationVerification grants one worker a fenced verification pass. A
// new generation invalidates every write from a prior, expired lease holder.
func ClaimMigrationVerification(db *sql.DB, ctx context.Context, migrationID string) (int, bool, error) {
	var generation int
	err := db.QueryRowContext(ctx, `
		UPDATE migrations
		SET verification_generation = verification_generation + 1,
		    verification_lease_until = NOW() + INTERVAL '2 minutes',
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
		  AND status = 'VERIFYING'
		  AND (verification_lease_until IS NULL OR verification_lease_until <= NOW())
		RETURNING verification_generation
	`, migrationID).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return generation, err == nil, err
}

// RenewMigrationVerificationLease keeps a claimed pass alive and confirms it
// is still entitled to commit verification results.
func RenewMigrationVerificationLease(db *sql.DB, ctx context.Context, migrationID string, generation int) (bool, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE migrations
		SET verification_lease_until = NOW() + INTERVAL '2 minutes', updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'VERIFYING' AND verification_generation = $2
		  AND verification_lease_until > NOW()
	`, migrationID, generation)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func ReleaseMigrationVerificationLease(db *sql.DB, ctx context.Context, migrationID string, generation int) error {
	_, err := db.ExecContext(ctx, `
		UPDATE migrations SET verification_lease_until = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND verification_generation = $2 AND status = 'VERIFYING'
	`, migrationID, generation)
	return err
}

func MarkMigrationTaskChecksumVerifiedWhileVerifying(db *sql.DB, ctx context.Context, taskID string, targetHash string, generation int) (bool, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE tasks AS t
		SET checksum_verified = TRUE,
		    target_hash = CASE WHEN $2 <> '' THEN $2 ELSE t.target_hash END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE t.id = $1 AND t.status = 'COMPLETED' AND t.checksum_verified = FALSE
		  AND EXISTS (
			SELECT 1 FROM migrations m
			WHERE m.id = t.migration_id AND m.status = 'VERIFYING'
			  AND m.verification_generation = $3 AND m.verification_lease_until > NOW()
		  )
	`, taskID, targetHash, generation)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func MarkMigrationTaskChecksumMismatchWhileVerifying(db *sql.DB, ctx context.Context, task *Task, generation int) (bool, error) {
	// The verifier populates ErrorMessage, TargetHash, and NextRetryAt before
	// calling this function. A zero NextRetryAt deliberately suppresses retry.
	res, err := db.ExecContext(ctx, `
		UPDATE tasks AS t
		SET status = 'FAILED', error_message = $2, next_retry_at = $3,
		    target_hash = $4, checksum_verified = FALSE, updated_at = CURRENT_TIMESTAMP
		WHERE t.id = $1 AND t.status = 'COMPLETED' AND t.checksum_verified = FALSE
		  AND EXISTS (
			SELECT 1 FROM migrations m
			WHERE m.id = t.migration_id AND m.status = 'VERIFYING'
			  AND m.verification_generation = $5 AND m.verification_lease_until > NOW()
		  )
	`, task.ID, task.ErrorMessage, task.NextRetryAt, task.TargetHash, generation)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
