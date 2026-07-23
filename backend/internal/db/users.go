package db

import (
	"database/sql"
	"fmt"
	"time"
)

type User struct {
	ID                  string       `json:"id"`
	Email               string       `json:"email"`
	PasswordHash        string       `json:"-"`
	DisplayName         string       `json:"display_name"`
	Role                string       `json:"role"`
	Active              bool         `json:"active"`
	MustChangePassword  bool         `json:"must_change_password"`
	Avatar              []byte       `json:"-"`
	AvatarMime          string       `json:"-"`
	CreatedAt           time.Time    `json:"created_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
	TotpEnabled         bool         `json:"totp_enabled"`
	TotpSecretEnc       string       `json:"-"`
	TotpBackupCodes     StringArray  `json:"-"`
	TotpFailedAttempts  int          `json:"-"`
	TotpLockedUntil     sql.NullTime `json:"-"`
	LoginFailedAttempts int          `json:"-"`
	LoginLockedUntil    sql.NullTime `json:"-"`
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

func CreateUser(db *sql.DB, email, passwordHash, displayName string) (*User, error) {
	query := `
		INSERT INTO users (email, password_hash, display_name)
		VALUES ($1, $2, $3)
		RETURNING id, role, active, must_change_password, created_at, updated_at
	`
	var u User
	u.Email = email
	u.DisplayName = displayName
	err := db.QueryRow(query, email, passwordHash, displayName).
		Scan(&u.ID, &u.Role, &u.Active, &u.MustChangePassword, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func CreateUserWithRole(database *sql.DB, email, passwordHash, displayName, role string, mustChangePassword bool) (*User, error) {
	if !ValidRoles[role] {
		role = "USER"
	}
	query := `
		INSERT INTO users (email, password_hash, display_name, role, active, must_change_password)
		VALUES ($1, $2, $3, $4, TRUE, $5)
		RETURNING id, role, active, must_change_password, created_at, updated_at
	`
	var u User
	u.Email = email
	u.DisplayName = displayName
	err := database.QueryRow(query, email, passwordHash, displayName, role, mustChangePassword).
		Scan(&u.ID, &u.Role, &u.Active, &u.MustChangePassword, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func GetUserByEmail(db *sql.DB, email string) (*User, error) {
	query := `
		SELECT id, email, password_hash, display_name, role, active, must_change_password, avatar, avatar_mime, created_at, updated_at,
		       totp_enabled, totp_secret_enc, totp_backup_codes, totp_failed_attempts, totp_locked_until,
		       login_failed_attempts, login_locked_until
		FROM users WHERE email = $1
	`
	var u User
	var mime sql.NullString
	var totpSecret sql.NullString
	err := db.QueryRow(query, email).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Role, &u.Active, &u.MustChangePassword, &u.Avatar, &mime, &u.CreatedAt, &u.UpdatedAt,
		&u.TotpEnabled, &totpSecret, &u.TotpBackupCodes, &u.TotpFailedAttempts, &u.TotpLockedUntil,
		&u.LoginFailedAttempts, &u.LoginLockedUntil)
	if err != nil {
		return nil, err
	}
	if mime.Valid {
		u.AvatarMime = mime.String
	}
	if totpSecret.Valid {
		u.TotpSecretEnc = totpSecret.String
	}
	return &u, nil
}

func GetUserByID(db *sql.DB, id string) (*User, error) {
	query := `
		SELECT id, email, password_hash, display_name, role, active, must_change_password, avatar, avatar_mime, created_at, updated_at,
		       totp_enabled, totp_secret_enc, totp_backup_codes, totp_failed_attempts, totp_locked_until,
		       login_failed_attempts, login_locked_until
		FROM users WHERE id = $1
	`
	var u User
	var mime sql.NullString
	var totpSecret sql.NullString
	err := db.QueryRow(query, id).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Role, &u.Active, &u.MustChangePassword, &u.Avatar, &mime, &u.CreatedAt, &u.UpdatedAt,
		&u.TotpEnabled, &totpSecret, &u.TotpBackupCodes, &u.TotpFailedAttempts, &u.TotpLockedUntil,
		&u.LoginFailedAttempts, &u.LoginLockedUntil)
	if err != nil {
		return nil, err
	}
	if mime.Valid {
		u.AvatarMime = mime.String
	}
	if totpSecret.Valid {
		u.TotpSecretEnc = totpSecret.String
	}
	return &u, nil
}

func ListUsers(database *sql.DB, p UserListParams) ([]User, int, error) {
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
	if err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (p.Page - 1) * p.Limit
	if offset < 0 {
		offset = 0
	}
	listArgs := append(append([]interface{}{}, args...), p.Limit, offset)
	query := `
		SELECT id, email, display_name, role, active, must_change_password, totp_enabled, created_at, updated_at
		FROM users WHERE ` + where + `
		ORDER BY created_at DESC
		LIMIT $` + fmt.Sprintf("%d", idx) + ` OFFSET $` + fmt.Sprintf("%d", idx+1)
	rows, err := database.Query(query, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.Active, &u.MustChangePassword, &u.TotpEnabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, nil
}

func UpdateUserDisplayName(db *sql.DB, id, name string) error {
	_, err := db.Exec(`UPDATE users SET display_name = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, name, id)
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
	_, err := database.Exec(`UPDATE users SET role = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, role, id)
	return err
}

func UpdateUserActive(database *sql.DB, id string, active bool) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE users SET active = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, active, id); err != nil {
		return err
	}

	if !active {
		if _, err := tx.Exec(
			`UPDATE migrations SET status = 'PAUSED', updated_at = CURRENT_TIMESTAMP WHERE user_id = $1 AND status IN ('RUNNING', 'INDEXING')`,
			id,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE schedules SET is_active = FALSE, updated_at = CURRENT_TIMESTAMP WHERE user_id = $1`, id); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`UPDATE schedules SET is_active = TRUE, updated_at = CURRENT_TIMESTAMP WHERE user_id = $1`, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func DeleteUser(database *sql.DB, id string) error {
	_, err := database.Exec(`DELETE FROM users WHERE id = $1`, id)
	return err
}

func CountActiveAdmins(database *sql.DB) (int, error) {
	var n int
	err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'ADMIN' AND active = TRUE`).Scan(&n)
	return n, err
}

// IsSetupRequired checks if there are no users in the database, requiring initial admin setup.
func IsSetupRequired(database *sql.DB) (bool, error) {
	var count int
	err := database.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

func GetGlobalStats(database *sql.DB) (*GlobalStats, error) {
	stats := &GlobalStats{
		MigrationsByStatus: map[string]int{},
		SyncsByStatus:      map[string]int{},
		TasksByStatus:      map[string]int{},
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&stats.TotalUsers); err != nil {
		return nil, err
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE active = TRUE`).Scan(&stats.ActiveUsers); err != nil {
		return nil, err
	}
	rows, err := database.Query(`SELECT status, COUNT(*) FROM migrations GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return nil, err
		}
		stats.MigrationsByStatus[status] = n
	}
	rows.Close()

	rows, err = database.Query(`SELECT status, COUNT(*) FROM sync_jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return nil, err
		}
		stats.SyncsByStatus[status] = n
	}
	rows.Close()

	rows, err = database.Query(`SELECT status, COUNT(*) FROM tasks GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return nil, err
		}
		stats.TasksByStatus[status] = n
	}
	rows.Close()
	return stats, nil
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

func ReplaceUsedBackupCode(database *sql.DB, userID string, remainingHashes StringArray) error {
	query := `
		UPDATE users
		SET totp_backup_codes = $1,
		    totp_failed_attempts = 0,
		    totp_locked_until = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := database.Exec(query, remainingHashes, userID)
	return err
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
