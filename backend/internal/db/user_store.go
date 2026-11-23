package db

import (
	"context"
	"database/sql"
	"time"
)

// UserStore is the persistence seam for the users table: accounts, profile
// settings, the login and TOTP failure counters, two-factor enrolment and the
// administrative user management. It delegates to package-level functions
// without adding queries.
//
// Credential and lockout invariants stay in those functions: the failure
// counters lock an account atomically in one statement, TOTP enrolment and
// backup-code consumption are compare-and-swap writes, and SuspendUser removes
// the user's refresh tokens in the same transaction. Callers must not rebuild
// them from reads and writes.
//
// Nothing here checks who is calling. API callers resolve the authenticated
// user id from the validated JWT claims before any by-id call.
type UserStore interface {
	// Creation and lookup. A missing user is sql.ErrNoRows.
	CreateUser(email, passwordHash, displayName, language string) (*User, error)
	CreateUserWithRole(email, passwordHash, displayName, role string, mustChangePassword bool, language string) (*User, error)
	CreateInitialAdmin(ctx context.Context, email, passwordHash, displayName, language string) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserByIDContext(ctx context.Context, id string) (*User, error)
	GetUserAuthState(id string) (*UserAuthState, error)
	IsSetupRequired() (bool, error)

	// Login and TOTP failure counters. Increment reports true when the call
	// locked the account.
	IncrementLoginFailed(userID string, maxAttempts int, lockDuration time.Duration) (bool, error)
	ResetLoginFailed(userID string) error
	IncrementTOTPFailed(userID string, maxAttempts int, lockDuration time.Duration) (bool, error)
	ResetTOTPFailed(userID string) error
	UpdateLastLoginAt(id string) (time.Time, error)

	// Two-factor enrolment. Secret and enable are compare-and-swap writes that
	// report false when the stored state no longer matches.
	SetUserTOTPSecretContext(ctx context.Context, userID, encryptedSecret string) (bool, error)
	EnableUserTOTPContext(ctx context.Context, userID, expectedEncryptedSecret string, backupCodeHashes StringArray) (bool, error)
	DisableUserTOTP(userID string) error
	ConsumeTOTPBackupCode(ctx context.Context, userID, codeHash string) (bool, error)

	// Profile and credentials.
	UpdateUserDisplayName(id, name string) error
	UpdateUserLanguage(id, language string) error
	UpdateUserPassword(id, newHash string) error
	UpdateUserAvatar(id string, data []byte, mime string) error
	DeleteUserAvatar(id string) error

	// Administration.
	ListUsersContext(ctx context.Context, p UserListParams) ([]User, int, error)
	UpdateUserRole(id, role string) error
	SuspendUser(id string) ([]string, error)
	UpdateUserActive(id string, active bool) error
	DeleteUser(id string) error
	GetGlobalStats() (*GlobalStats, error)
}

type postgresUserStore struct{ db *sql.DB }

// NewUserStore returns the PostgreSQL-backed UserStore. It panics on a nil
// handle so a wiring mistake fails at startup instead of on the first query.
func NewUserStore(database *sql.DB) UserStore {
	if database == nil {
		panic("db: NewUserStore requires a database handle")
	}
	return postgresUserStore{db: database}
}

func (s postgresUserStore) CreateUser(email, passwordHash, displayName, language string) (*User, error) {
	return createUser(s.db, email, passwordHash, displayName, language)
}

func (s postgresUserStore) CreateUserWithRole(email, passwordHash, displayName, role string, mustChangePassword bool, language string) (*User, error) {
	return createUserWithRole(s.db, email, passwordHash, displayName, role, mustChangePassword, language)
}

func (s postgresUserStore) CreateInitialAdmin(ctx context.Context, email, passwordHash, displayName, language string) (*User, error) {
	return createInitialAdmin(ctx, s.db, email, passwordHash, displayName, language)
}

func (s postgresUserStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	return getUserByEmail(ctx, s.db, email)
}

func (s postgresUserStore) GetUserByIDContext(ctx context.Context, id string) (*User, error) {
	return getUserByIDContext(ctx, s.db, id)
}

func (s postgresUserStore) GetUserAuthState(id string) (*UserAuthState, error) {
	return getUserAuthState(s.db, id)
}

func (s postgresUserStore) IsSetupRequired() (bool, error) {
	return isSetupRequired(s.db)
}

func (s postgresUserStore) IncrementLoginFailed(userID string, maxAttempts int, lockDuration time.Duration) (bool, error) {
	return incrementLoginFailed(s.db, userID, maxAttempts, lockDuration)
}

func (s postgresUserStore) ResetLoginFailed(userID string) error {
	return resetLoginFailed(s.db, userID)
}

func (s postgresUserStore) IncrementTOTPFailed(userID string, maxAttempts int, lockDuration time.Duration) (bool, error) {
	return incrementTOTPFailed(s.db, userID, maxAttempts, lockDuration)
}

func (s postgresUserStore) ResetTOTPFailed(userID string) error {
	return resetTOTPFailed(s.db, userID)
}

func (s postgresUserStore) UpdateLastLoginAt(id string) (time.Time, error) {
	return updateLastLoginAt(s.db, id)
}

func (s postgresUserStore) SetUserTOTPSecretContext(ctx context.Context, userID, encryptedSecret string) (bool, error) {
	return setUserTOTPSecretContext(ctx, s.db, userID, encryptedSecret)
}

func (s postgresUserStore) EnableUserTOTPContext(ctx context.Context, userID, expectedEncryptedSecret string, backupCodeHashes StringArray) (bool, error) {
	return enableUserTOTPContext(ctx, s.db, userID, expectedEncryptedSecret, backupCodeHashes)
}

func (s postgresUserStore) DisableUserTOTP(userID string) error {
	return disableUserTOTP(s.db, userID)
}

func (s postgresUserStore) ConsumeTOTPBackupCode(ctx context.Context, userID, codeHash string) (bool, error) {
	return consumeTOTPBackupCode(ctx, s.db, userID, codeHash)
}

func (s postgresUserStore) UpdateUserDisplayName(id, name string) error {
	return updateUserDisplayName(s.db, id, name)
}

func (s postgresUserStore) UpdateUserLanguage(id, language string) error {
	return updateUserLanguage(s.db, id, language)
}

func (s postgresUserStore) UpdateUserPassword(id, newHash string) error {
	return updateUserPassword(s.db, id, newHash)
}

func (s postgresUserStore) UpdateUserAvatar(id string, data []byte, mime string) error {
	return updateUserAvatar(s.db, id, data, mime)
}

func (s postgresUserStore) DeleteUserAvatar(id string) error {
	return deleteUserAvatar(s.db, id)
}

func (s postgresUserStore) ListUsersContext(ctx context.Context, p UserListParams) ([]User, int, error) {
	return listUsersContext(ctx, s.db, p)
}

func (s postgresUserStore) UpdateUserRole(id, role string) error {
	return updateUserRole(s.db, id, role)
}

func (s postgresUserStore) SuspendUser(id string) ([]string, error) {
	return suspendUser(s.db, id)
}

func (s postgresUserStore) UpdateUserActive(id string, active bool) error {
	return updateUserActive(s.db, id, active)
}

func (s postgresUserStore) DeleteUser(id string) error {
	return deleteUser(s.db, id)
}

func (s postgresUserStore) GetGlobalStats() (*GlobalStats, error) {
	return getGlobalStats(s.db)
}
