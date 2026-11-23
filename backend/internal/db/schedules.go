package db

import (
	"context"
	"database/sql"
	"time"
)

type Schedule struct {
	ID             string         `json:"id"`
	UserID         string         `json:"user_id"`
	TaskType       string         `json:"task_type"` // migration, sync, backup
	TaskID         string         `json:"task_id"`
	CronExpression sql.NullString `json:"cron_expression"`
	RunAt          sql.NullTime   `json:"run_at"`
	NextRunAt      sql.NullTime   `json:"next_run_at"`
	IsActive       bool           `json:"is_active"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

const createScheduleQuery = `
		INSERT INTO schedules (
			user_id, task_type, task_id, cron_expression, run_at, next_run_at, is_active
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at, updated_at
	`

func insertSchedule(ctx context.Context, database queryExecerContext, s *Schedule) error {
	return database.QueryRowContext(ctx,
		createScheduleQuery,
		s.UserID, s.TaskType, s.TaskID, s.CronExpression, s.RunAt, s.NextRunAt, s.IsActive,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
}

// getScheduleContext retrieves a schedule while honoring caller cancellation.
func getScheduleContext(ctx context.Context, db *sql.DB, id string) (*Schedule, error) {
	query := `
		SELECT id, user_id, task_type, task_id, cron_expression, run_at, next_run_at,
		       is_active, created_at, updated_at
		FROM schedules WHERE id = $1
	`
	var s Schedule
	err := db.QueryRowContext(ctx, query, id).Scan(
		&s.ID, &s.UserID, &s.TaskType, &s.TaskID, &s.CronExpression, &s.RunAt, &s.NextRunAt,
		&s.IsActive, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// getSchedulesForUserContext lists schedules while honoring caller cancellation.
func getSchedulesForUserContext(ctx context.Context, db *sql.DB, userID string) ([]Schedule, error) {
	query := `
		SELECT id, user_id, task_type, task_id, cron_expression, run_at, next_run_at,
		       is_active, created_at, updated_at
		FROM schedules
		WHERE user_id = $1
		ORDER BY created_at DESC
	`
	rows, err := db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []Schedule
	for rows.Next() {
		var s Schedule
		err := rows.Scan(
			&s.ID, &s.UserID, &s.TaskType, &s.TaskID, &s.CronExpression, &s.RunAt, &s.NextRunAt,
			&s.IsActive, &s.CreatedAt, &s.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return schedules, nil
}

// getDueSchedulesContext lists active schedules that are ready to run while
// honoring caller cancellation.
func getDueSchedulesContext(ctx context.Context, db *sql.DB) ([]Schedule, error) {
	query := `
		SELECT id, user_id, task_type, task_id, cron_expression, run_at, next_run_at,
		       is_active, created_at, updated_at
		FROM schedules
		WHERE is_active = TRUE
		  AND next_run_at <= NOW()
		ORDER BY next_run_at ASC
	`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []Schedule
	for rows.Next() {
		var s Schedule
		err := rows.Scan(
			&s.ID, &s.UserID, &s.TaskType, &s.TaskID, &s.CronExpression, &s.RunAt, &s.NextRunAt,
			&s.IsActive, &s.CreatedAt, &s.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return schedules, nil
}

// updateNextRunAtContext updates a schedule while honoring caller cancellation.
func updateNextRunAtContext(ctx context.Context, db *sql.DB, id string, nextRunAt time.Time) error {
	query := `
		UPDATE schedules
		SET next_run_at = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := db.ExecContext(ctx, query, nextRunAt, id)
	return err
}

// updateNextRunAtIfUnchangedContext advances a schedule only when its next run
// is still the value observed by the caller. This prevents a stale scheduler
// read from overwriting a schedule explicitly rescheduled by a user.
func updateNextRunAtIfUnchangedContext(ctx context.Context, db *sql.DB, id string, expectedNextRunAt, nextRunAt time.Time) (bool, error) {
	result, err := db.ExecContext(ctx, `
		UPDATE schedules
		SET next_run_at = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2 AND next_run_at IS NOT DISTINCT FROM $3
	`, nextRunAt, id, expectedNextRunAt)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// deactivateScheduleContext deactivates a schedule while honoring caller cancellation.
func deactivateScheduleContext(ctx context.Context, db *sql.DB, id string) error {
	query := `
		UPDATE schedules
		SET is_active = FALSE, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`
	_, err := db.ExecContext(ctx, query, id)
	return err
}

func deleteSchedule(db *sql.DB, id string) error {
	query := `DELETE FROM schedules WHERE id = $1`
	_, err := db.Exec(query, id)
	return err
}

func deleteSchedulesForTask(db *sql.DB, taskType string, taskID string) error {
	query := `DELETE FROM schedules WHERE task_type = $1 AND task_id = $2`
	_, err := db.Exec(query, taskType, taskID)
	return err
}

// verifyScheduleOwnershipContext verifies ownership while honoring caller cancellation.
func verifyScheduleOwnershipContext(ctx context.Context, db *sql.DB, scheduleID, userID string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM schedules WHERE id = $1 AND user_id = $2)`
	var exists bool
	err := db.QueryRowContext(ctx, query, scheduleID, userID).Scan(&exists)
	return exists, err
}
