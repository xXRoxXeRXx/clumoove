package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	initialAdminSetupLockID   int64 = 84736292
	activeAdminMutationLockID int64 = 84736293
)

var (
	ErrSetupAlreadyCompleted = errors.New("initial setup already completed")
	// ErrLastActiveAdmin is returned when a governance mutation would remove
	// the installation's final active administrator.
	ErrLastActiveAdmin = errors.New("cannot remove the last active administrator")
)

type User struct {
	ID                  string       `json:"id"`
	Email               string       `json:"email"`
	PasswordHash        string       `json:"-"`
	DisplayName         string       `json:"display_name"`
	Language            string       `json:"language"`
	Role                string       `json:"role"`
	Active              bool         `json:"active"`
	MustChangePassword  bool         `json:"must_change_password"`
	Avatar              []byte       `json:"-"`
	AvatarMime          string       `json:"-"`
	CreatedAt           time.Time    `json:"created_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
	LastLoginAt         *time.Time   `json:"last_login_at"`
	TotpEnabled         bool         `json:"totp_enabled"`
	TotpSecretEnc       string       `json:"-"`
	TotpBackupCodes     StringArray  `json:"-"`
	TotpFailedAttempts  int          `json:"-"`
	TotpLockedUntil     sql.NullTime `json:"-"`
	LoginFailedAttempts int          `json:"-"`
	LoginLockedUntil    sql.NullTime `json:"-"`
}

// UserAuthState contains only the mutable account attributes required to
// authorize an already-authenticated request. Keep this separate from User so
// request authentication never loads credential material into memory.
type UserAuthState struct {
	Role               string
	Active             bool
	MustChangePassword bool
}

type UserListParams struct {
	Page   int
	Limit  int
	Role   string
	Active *bool
	Query  string
}

type GlobalStats struct {
	TotalUsers         int            `json:"total_users"`
	ActiveUsers        int            `json:"active_users"`
	MigrationsByStatus map[string]int `json:"migrations_by_status"`
	SyncsByStatus      map[string]int `json:"syncs_by_status"`
	TasksByStatus      map[string]int `json:"tasks_by_status"`
}

func CreateUser(db *sql.DB, email, passwordHash, displayName, language string) (*User, error) {
	if language != "de" && language != "en" {
		language = "en"
	}
	query := `
		INSERT INTO users (email, password_hash, display_name, language)
		VALUES ($1, $2, $3, $4)
		RETURNING id, role, active, must_change_password, language, created_at, updated_at
	`
	var u User
	u.Email = email
	u.DisplayName = displayName
	err := db.QueryRow(query, email, passwordHash, displayName, language).
		Scan(&u.ID, &u.Role, &u.Active, &u.MustChangePassword, &u.Language, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func CreateUserWithRole(database *sql.DB, email, passwordHash, displayName, role string, mustChangePassword bool, language string) (*User, error) {
	if !ValidRoles[role] {
		role = "USER"
	}
	if language != "de" && language != "en" {
		language = "en"
	}
	query := `
		INSERT INTO users (email, password_hash, display_name, role, active, must_change_password, language)
		VALUES ($1, $2, $3, $4, TRUE, $5, $6)
		RETURNING id, role, active, must_change_password, language, created_at, updated_at
	`
	var u User
	u.Email = email
	u.DisplayName = displayName
	err := database.QueryRow(query, email, passwordHash, displayName, role, mustChangePassword, language).
		Scan(&u.ID, &u.Role, &u.Active, &u.MustChangePassword, &u.Language, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateInitialAdmin atomically claims an empty installation for its first
// administrator. The advisory lock also serializes the otherwise unlocked
// empty-table case, where SELECT ... FOR UPDATE cannot protect a missing row.
func CreateInitialAdmin(ctx context.Context, database *sql.DB, email, passwordHash, displayName, language string) (*User, error) {
	if language != "de" && language != "en" {
		language = "en"
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, initialAdminSetupLockID); err != nil {
		return nil, err
	}

	var setupRequired bool
	if err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM users)`).Scan(&setupRequired); err != nil {
		return nil, err
	}
	if !setupRequired {
		return nil, ErrSetupAlreadyCompleted
	}

	const query = `
		INSERT INTO users (email, password_hash, display_name, role, active, must_change_password, language)
		VALUES ($1, $2, $3, 'ADMIN', TRUE, FALSE, $4)
		RETURNING id, role, active, must_change_password, language, created_at, updated_at
	`
	var u User
	u.Email = email
	u.DisplayName = displayName
	if err := tx.QueryRowContext(ctx, query, email, passwordHash, displayName, language).
		Scan(&u.ID, &u.Role, &u.Active, &u.MustChangePassword, &u.Language, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &u, nil
}

func GetUserByEmail(ctx context.Context, db *sql.DB, email string) (*User, error) {
	query := `
		SELECT id, email, password_hash, display_name, language, role, active, must_change_password, avatar, avatar_mime, created_at, updated_at,
		       totp_enabled, totp_secret_enc, totp_backup_codes, totp_failed_attempts, totp_locked_until,
		       login_failed_attempts, login_locked_until, last_login_at
		FROM users WHERE email = $1
	`
	var u User
	var mime sql.NullString
	var totpSecret sql.NullString
	var lastLogin sql.NullTime
	err := db.QueryRowContext(ctx, query, email).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Language, &u.Role, &u.Active, &u.MustChangePassword, &u.Avatar, &mime, &u.CreatedAt, &u.UpdatedAt,
		&u.TotpEnabled, &totpSecret, &u.TotpBackupCodes, &u.TotpFailedAttempts, &u.TotpLockedUntil,
		&u.LoginFailedAttempts, &u.LoginLockedUntil, &lastLogin)
	if err != nil {
		return nil, err
	}
	if mime.Valid {
		u.AvatarMime = mime.String
	}
	if totpSecret.Valid {
		u.TotpSecretEnc = totpSecret.String
	}
	if lastLogin.Valid {
		u.LastLoginAt = &lastLogin.Time
	}
	return &u, nil
}

func GetUserByID(db *sql.DB, id string) (*User, error) {
	return GetUserByIDContext(context.Background(), db, id)
}

// GetUserByIDContext retrieves a user while honoring caller cancellation.
func GetUserByIDContext(ctx context.Context, db *sql.DB, id string) (*User, error) {
	query := `
		SELECT id, email, password_hash, display_name, language, role, active, must_change_password, avatar, avatar_mime, created_at, updated_at,
		       totp_enabled, totp_secret_enc, totp_backup_codes, totp_failed_attempts, totp_locked_until,
		       login_failed_attempts, login_locked_until, last_login_at
		FROM users WHERE id = $1
	`
	var u User
	var mime sql.NullString
	var totpSecret sql.NullString
	var lastLogin sql.NullTime
	err := db.QueryRowContext(ctx, query, id).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Language, &u.Role, &u.Active, &u.MustChangePassword, &u.Avatar, &mime, &u.CreatedAt, &u.UpdatedAt,
		&u.TotpEnabled, &totpSecret, &u.TotpBackupCodes, &u.TotpFailedAttempts, &u.TotpLockedUntil,
		&u.LoginFailedAttempts, &u.LoginLockedUntil, &lastLogin)
	if err != nil {
		return nil, err
	}
	if mime.Valid {
		u.AvatarMime = mime.String
	}
	if totpSecret.Valid {
		u.TotpSecretEnc = totpSecret.String
	}
	if lastLogin.Valid {
		u.LastLoginAt = &lastLogin.Time
	}
	return &u, nil
}

// GetUserByIDTx is the transaction-scoped counterpart of GetUserByID.
func GetUserByIDTx(tx *sql.Tx, id string) (*User, error) {
	query := `
		SELECT id, email, password_hash, display_name, language, role, active, must_change_password, avatar, avatar_mime, created_at, updated_at,
		       totp_enabled, totp_secret_enc, totp_backup_codes, totp_failed_attempts, totp_locked_until,
		       login_failed_attempts, login_locked_until, last_login_at
		FROM users WHERE id = $1
	`
	var u User
	var mime sql.NullString
	var totpSecret sql.NullString
	var lastLogin sql.NullTime
	err := tx.QueryRow(query, id).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Language, &u.Role, &u.Active, &u.MustChangePassword, &u.Avatar, &mime, &u.CreatedAt, &u.UpdatedAt,
		&u.TotpEnabled, &totpSecret, &u.TotpBackupCodes, &u.TotpFailedAttempts, &u.TotpLockedUntil,
		&u.LoginFailedAttempts, &u.LoginLockedUntil, &lastLogin)
	if err != nil {
		return nil, err
	}
	if mime.Valid {
		u.AvatarMime = mime.String
	}
	if totpSecret.Valid {
		u.TotpSecretEnc = totpSecret.String
	}
	if lastLogin.Valid {
		u.LastLoginAt = &lastLogin.Time
	}
	return &u, nil
}

// GetUserAuthState fetches the current authorization state for an account.
func GetUserAuthState(database *sql.DB, id string) (*UserAuthState, error) {
	var state UserAuthState
	err := database.QueryRow(`
		SELECT role, active, must_change_password
		FROM users WHERE id = $1
	`, id).Scan(&state.Role, &state.Active, &state.MustChangePassword)
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func ListUsers(database *sql.DB, p UserListParams) ([]User, int, error) {
	return ListUsersContext(context.Background(), database, p)
}

// ListUsersContext lists users while honoring caller cancellation.
func ListUsersContext(ctx context.Context, database *sql.DB, p UserListParams) ([]User, int, error) {
	where := "TRUE"
	args := []interface{}{}
	idx := 1
	if p.Role != "" {
		where += fmt.Sprintf(" AND role = $%d", idx)
		args = append(args, p.Role)
		idx++
	}
	if p.Active != nil {
		where += fmt.Sprintf(" AND active = $%d", idx)
		args = append(args, *p.Active)
		idx++
	}
	if p.Query != "" {
		where += fmt.Sprintf(" AND (email ILIKE $%d OR display_name ILIKE $%d)", idx, idx+1)
		like := "%" + p.Query + "%"
		args = append(args, like, like)
		idx += 2
	}

	var total int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (p.Page - 1) * p.Limit
	if offset < 0 {
		offset = 0
	}
	listArgs := append(append([]interface{}{}, args...), p.Limit, offset)
	query := `
		SELECT id, email, display_name, role, active, must_change_password, totp_enabled, created_at, updated_at, last_login_at
		FROM users WHERE ` + where + `
		ORDER BY created_at DESC
		LIMIT $` + fmt.Sprintf("%d", idx) + ` OFFSET $` + fmt.Sprintf("%d", idx+1)
	rows, err := database.QueryContext(ctx, query, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		var u User
		var lastLogin sql.NullTime
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.Active, &u.MustChangePassword, &u.TotpEnabled, &u.CreatedAt, &u.UpdatedAt, &lastLogin); err != nil {
			return nil, 0, err
		}
		if lastLogin.Valid {
			u.LastLoginAt = &lastLogin.Time
		}
		users = append(users, u)
	}
	return users, total, nil
}

func UpdateLastLoginAt(database *sql.DB, id string) (time.Time, error) {
	var ts time.Time
	err := database.QueryRow(`UPDATE users SET last_login_at = CURRENT_TIMESTAMP WHERE id = $1 RETURNING last_login_at`, id).Scan(&ts)
	return ts, err
}

func UpdateUserDisplayName(db *sql.DB, id, name string) error {
	_, err := db.Exec(`UPDATE users SET display_name = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, name, id)
	return err
}

func UpdateUserLanguage(database *sql.DB, id, language string) error {
	if language != "de" && language != "en" {
		return fmt.Errorf("unsupported language %q", language)
	}
	_, err := database.Exec(`UPDATE users SET language = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, language, id)
	return err
}

func UpdateUserPassword(db *sql.DB, id, newHash string) error {
	_, err := db.Exec(`UPDATE users SET password_hash = $1, must_change_password = FALSE, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, newHash, id)
	return err
}

func UpdateUserRole(database *sql.DB, id, role string) error {
	if !ValidRoles[role] {
		return fmt.Errorf("invalid role %q", role)
	}

	tx, err := beginActiveAdminMutation(database)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if role != "ADMIN" {
		if err := ensureNotLastActiveAdmin(tx, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE users SET role = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, role, id); err != nil {
		return err
	}
	return tx.Commit()
}

// beginActiveAdminMutation serializes every operation that can remove an
// active administrator. The advisory lock also covers the empty result set
// case, which row locks alone cannot protect. READ COMMITTED keeps the count
// current after acquiring that lock without introducing serializable aborts
// from unrelated account updates.
func beginActiveAdminMutation(database *sql.DB) (*sql.Tx, error) {
	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1)`, activeAdminMutationLockID); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// ensureNotLastActiveAdmin re-counts while the transaction-scoped governance
// lock is held, immediately before a mutation that can remove an admin.
func ensureNotLastActiveAdmin(tx *sql.Tx, id string) error {
	var role string
	var active bool
	if err := tx.QueryRow(`SELECT role, active FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&role, &active); err != nil {
		// User mutations historically treat a target that disappears after route
		// handling as a no-op.
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if role != "ADMIN" || !active {
		return nil
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'ADMIN' AND active = TRUE`).Scan(&count); err != nil {
		return err
	}
	if count <= 1 {
		return ErrLastActiveAdmin
	}
	return nil
}

// SuspendUser deactivates an account and abandons every active sync pass. It
// returns the affected jobs so the API can cancel their in-flight coordinators
// and worker streams after this transaction commits.
func SuspendUser(database *sql.DB, id string) ([]string, error) {
	tx, err := beginActiveAdminMutation(database)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := ensureNotLastActiveAdmin(tx, id); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(`UPDATE users SET active = FALSE, updated_at = CURRENT_TIMESTAMP WHERE id = $1`, id); err != nil {
		return nil, err
	}

	// Suspending an account must terminate every renewable session while this
	// transaction still holds the user row lock. This also prevents a refresh
	// request from surviving the suspension by rotating its token concurrently.
	if _, err := tx.Exec(`DELETE FROM refresh_tokens WHERE user_id = $1`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE migrations SET status = 'PAUSED', updated_at = CURRENT_TIMESTAMP WHERE user_id = $1 AND status IN ('RUNNING', 'INDEXING')`,
		id,
	); err != nil {
		return nil, err
	}

	syncJobIDs, err := func() ([]string, error) {
		rows, err := tx.Query(`
			UPDATE sync_jobs
			SET status = 'PAUSED', updated_at = CURRENT_TIMESTAMP
			-- PAUSED_CONNECTION_LOSS is intentional: account suspension is final
			-- until an administrator reactivates the account, so recovery must not
			-- automatically resume this job.
			WHERE user_id = $1 AND status IN ('INDEXING', 'RUNNING', 'VERIFYING', 'PAUSED_CONNECTION_LOSS')
			RETURNING id
		`, id)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		var ids []string
		for rows.Next() {
			var syncJobID string
			if err := rows.Scan(&syncJobID); err != nil {
				return nil, err
			}
			ids = append(ids, syncJobID)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return ids, nil
	}()
	if err != nil {
		return nil, err
	}
	for _, syncJobID := range syncJobIDs {
		if _, err := tx.Exec(`
			UPDATE tasks
			SET status = 'CANCELLED', worker_hash = NULL, next_retry_at = NULL, updated_at = CURRENT_TIMESTAMP
			WHERE sync_job_id = $1
			  AND (status IN ('PENDING', 'RUNNING') OR (status = 'FAILED' AND next_retry_at IS NOT NULL))
		`, syncJobID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`UPDATE schedules SET is_active = FALSE, updated_at = CURRENT_TIMESTAMP WHERE user_id = $1`, id); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return syncJobIDs, nil
}

// UpdateUserActive is the reactivation path (active=true). For suspension
// (active=false), callers MUST use SuspendUser directly so they can publish
// cancellation events for in-flight sync coordinators and worker streams.
func UpdateUserActive(database *sql.DB, id string, active bool) error {
	if !active {
		return fmt.Errorf("use SuspendUser to deactivate an account")
	}
	_, err := database.Exec(`UPDATE users SET active = TRUE, updated_at = CURRENT_TIMESTAMP WHERE id = $1`, id)
	if err != nil {
		return err
	}
	_, err = database.Exec(`UPDATE schedules SET is_active = TRUE, updated_at = CURRENT_TIMESTAMP WHERE user_id = $1`, id)
	return err
}

func DeleteUser(database *sql.DB, id string) error {
	tx, err := beginActiveAdminMutation(database)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureNotLastActiveAdmin(tx, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM users WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func CountActiveAdmins(database *sql.DB) (int, error) {
	var n int
	err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'ADMIN' AND active = TRUE`).Scan(&n)
	return n, err
}

// IsSetupRequired checks if there are no users in the database, requiring initial admin setup.
func IsSetupRequired(database *sql.DB) (bool, error) {
	var setupRequired bool
	err := database.QueryRow(`SELECT NOT EXISTS (SELECT 1 FROM users)`).Scan(&setupRequired)
	if err != nil {
		return false, err
	}
	return setupRequired, nil
}

func GetGlobalStats(database *sql.DB) (*GlobalStats, error) {
	stats := &GlobalStats{
		MigrationsByStatus: map[string]int{},
		SyncsByStatus:      map[string]int{},
		TasksByStatus:      map[string]int{},
	}
	rows, err := database.Query(`
		SELECT 'users' AS category, 'total' AS status, COUNT(*) FROM users
		UNION ALL
		SELECT 'users', 'active', COUNT(*) FROM users WHERE active = TRUE
		UNION ALL
		SELECT 'migrations', status, COUNT(*) FROM migrations GROUP BY status
		UNION ALL
		SELECT 'syncs', status, COUNT(*) FROM sync_jobs GROUP BY status
		UNION ALL
		SELECT 'tasks', status, COUNT(*) FROM tasks GROUP BY status
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var category, status string
		var n int
		if err := rows.Scan(&category, &status, &n); err != nil {
			return nil, err
		}
		switch category {
		case "users":
			if status == "total" {
				stats.TotalUsers = n
			} else {
				stats.ActiveUsers = n
			}
		case "migrations":
			stats.MigrationsByStatus[status] = n
		case "syncs":
			stats.SyncsByStatus[status] = n
		case "tasks":
			stats.TasksByStatus[status] = n
		}
	}
	return stats, rows.Err()
}

func SetUserTOTPSecret(database *sql.DB, userID, encryptedSecret string) error {
	query := `
		UPDATE users
		SET totp_secret_enc = $1,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := database.Exec(query, encryptedSecret, userID)
	return err
}

func EnableUserTOTP(database *sql.DB, userID string, backupCodeHashes StringArray) error {
	query := `
		UPDATE users
		SET totp_enabled = TRUE,
		    totp_backup_codes = $1,
		    totp_failed_attempts = 0,
		    totp_locked_until = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := database.Exec(query, backupCodeHashes, userID)
	return err
}

func DisableUserTOTP(database *sql.DB, userID string) error {
	query := `
		UPDATE users
		SET totp_enabled = FALSE,
		    totp_secret_enc = NULL,
		    totp_backup_codes = NULL,
		    totp_failed_attempts = 0,
		    totp_locked_until = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`
	_, err := database.Exec(query, userID)
	return err
}

// ConsumeTOTPBackupCode removes a verified backup-code hash only if it is still
// present for an enabled user, and atomically clears the TOTP failure state.
// PostgreSQL rechecks the predicate after waiting on a concurrent update, so a
// recovery code can be consumed only once. Callers rely on a successful result
// to skip ResetTOTPFailed.
func ConsumeTOTPBackupCode(ctx context.Context, database *sql.DB, userID, codeHash string) (bool, error) {
	// The JSONB - operator removes all exact duplicates. Bcrypt hashes are unique
	// per generated backup code, so this consumes one code in practice.
	query := `
		UPDATE users
		SET totp_backup_codes = totp_backup_codes - $2,
		    totp_failed_attempts = 0,
		    totp_locked_until = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
		  AND totp_enabled = TRUE
		  AND totp_backup_codes ? $2
	`
	result, err := database.ExecContext(ctx, query, userID, codeHash)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

func IncrementTOTPFailed(database *sql.DB, userID string, maxAttempts int, lockDuration time.Duration) (bool, error) {
	lockUntil := time.Now().Add(lockDuration)
	query := `
		UPDATE users
		SET totp_failed_attempts = CASE
		        WHEN totp_failed_attempts + 1 >= $2 THEN 0
		        ELSE totp_failed_attempts + 1
		    END,
		    totp_locked_until = CASE
		        WHEN totp_failed_attempts + 1 >= $2 THEN $3
		        ELSE totp_locked_until
		    END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
		RETURNING (totp_locked_until IS NOT NULL AND totp_locked_until > CURRENT_TIMESTAMP)
	`
	var locked bool
	if err := database.QueryRow(query, userID, maxAttempts, lockUntil).Scan(&locked); err != nil {
		return false, err
	}
	return locked, nil
}

func ResetTOTPFailed(database *sql.DB, userID string) error {
	query := `
		UPDATE users
		SET totp_failed_attempts = 0,
		    totp_locked_until = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`
	_, err := database.Exec(query, userID)
	return err
}

func IncrementLoginFailed(database *sql.DB, userID string, maxAttempts int, lockDuration time.Duration) (bool, error) {
	lockUntil := time.Now().Add(lockDuration)
	query := `
		UPDATE users
		SET login_failed_attempts = CASE
		        WHEN login_failed_attempts + 1 >= $2 THEN 0
		        ELSE login_failed_attempts + 1
		    END,
		    login_locked_until = CASE
		        WHEN login_failed_attempts + 1 >= $2 THEN $3
		        ELSE login_locked_until
		    END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
		RETURNING (login_locked_until IS NOT NULL AND login_locked_until > CURRENT_TIMESTAMP)
	`
	var locked bool
	if err := database.QueryRow(query, userID, maxAttempts, lockUntil).Scan(&locked); err != nil {
		return false, err
	}
	return locked, nil
}

func ResetLoginFailed(database *sql.DB, userID string) error {
	query := `
		UPDATE users
		SET login_failed_attempts = 0,
		    login_locked_until = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`
	_, err := database.Exec(query, userID)
	return err
}

func UpdateUserAvatar(db *sql.DB, id string, data []byte, mime string) error {
	_, err := db.Exec(`UPDATE users SET avatar = $1, avatar_mime = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $3`, data, mime, id)
	return err
}

func DeleteUserAvatar(db *sql.DB, id string) error {
	_, err := db.Exec(`UPDATE users SET avatar = NULL, avatar_mime = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = $1`, id)
	return err
}
