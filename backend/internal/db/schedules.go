package db

import (
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

func CreateSchedule(db *sql.DB, s *Schedule) (string, error) {
	query := `
		INSERT INTO schedules (
			user_id, task_type, task_id, cron_expression, run_at, next_run_at, is_active
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at, updated_at
	`
	err := db.QueryRow(
		query,
		s.UserID, s.TaskType, s.TaskID, s.CronExpression, s.RunAt, s.NextRunAt, s.IsActive,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)

	if err != nil {
		return "", err
	}
	return s.ID, nil
}

func GetSchedule(db *sql.DB, id string) (*Schedule, error) {
	query := `
		SELECT id, user_id, task_type, task_id, cron_expression, run_at, next_run_at,
		       is_active, created_at, updated_at
		FROM schedules WHERE id = $1
	`
	var s Schedule
	err := db.QueryRow(query, id).Scan(
		&s.ID, &s.UserID, &s.TaskType, &s.TaskID, &s.CronExpression, &s.RunAt, &s.NextRunAt,
		&s.IsActive, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func GetSchedulesForUser(db *sql.DB, userID string) ([]Schedule, error) {
	query := `
		SELECT id, user_id, task_type, task_id, cron_expression, run_at, next_run_at,
		       is_active, created_at, updated_at
		FROM schedules
		WHERE user_id = $1
		ORDER BY created_at DESC
	`
	rows, err := db.Query(query, userID)
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

func GetDueSchedules(db *sql.DB) ([]Schedule, error) {
	query := `
		SELECT id, user_id, task_type, task_id, cron_expression, run_at, next_run_at,
		       is_active, created_at, updated_at
		FROM schedules
		WHERE is_active = TRUE
		  AND next_run_at <= NOW()
		ORDER BY next_run_at ASC
	`
	rows, err := db.Query(query)
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

func UpdateNextRunAt(db *sql.DB, id string, nextRunAt time.Time) error {
	query := `
		UPDATE schedules
		SET next_run_at = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := db.Exec(query, nextRunAt, id)
	return err
}

func DeactivateSchedule(db *sql.DB, id string) error {
	query := `
		UPDATE schedules
		SET is_active = FALSE, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`
	_, err := db.Exec(query, id)
	return err
}

func DeleteSchedule(db *sql.DB, id string) error {
	query := `DELETE FROM schedules WHERE id = $1`
	_, err := db.Exec(query, id)
	return err
}

func DeleteSchedulesForTask(db *sql.DB, taskType string, taskID string) error {
	query := `DELETE FROM schedules WHERE task_type = $1 AND task_id = $2`
	_, err := db.Exec(query, taskType, taskID)
	return err
}

func VerifyScheduleOwnership(db *sql.DB, scheduleID, userID string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM schedules WHERE id = $1 AND user_id = $2)`
	var exists bool
	err := db.QueryRow(query, scheduleID, userID).Scan(&exists)
	return exists, err
}

func UpdateSchedule(db *sql.DB, s *Schedule) error {
	query := `
		UPDATE schedules
		SET cron_expression = $1, run_at = $2, next_run_at = $3, is_active = $4,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $5
	`
	_, err := db.Exec(query, s.CronExpression, s.RunAt, s.NextRunAt, s.IsActive, s.ID)
	return err
}
