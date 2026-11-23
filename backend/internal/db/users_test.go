package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"backend/internal/totp2fa"
)

type activeAdminMutation struct {
	name  string
	apply func(*sql.DB, string) error
}

func suspendActiveAdmin(database *sql.DB, id string) error {
	_, err := suspendUser(database, id)
	return err
}

func demoteActiveAdmin(database *sql.DB, id string) error {
	return updateUserRole(database, id, "USER")
}

func TestCreateInitialAdmin_ConcurrentRequestsCreateOnlyOneAdmin(t *testing.T) {
	database := setupTestDB(t)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := createInitialAdmin(
				context.Background(),
				database,
				fmt.Sprintf("admin-%d@example.test", i),
				"password-hash",
				"Initial Admin",
				"en",
			)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var created, rejected int
	for err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrSetupAlreadyCompleted):
			rejected++
		default:
			t.Fatalf("createInitialAdmin returned unexpected error: %v", err)
		}
	}
	if created != 1 || rejected != 1 {
		t.Fatalf("results = %d created, %d rejected; want 1 each", created, rejected)
	}

	var adminCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'ADMIN'`).Scan(&adminCount); err != nil {
		t.Fatalf("count initial admins: %v", err)
	}
	if adminCount != 1 {
		t.Errorf("initial admin count = %d, want 1", adminCount)
	}
}

func TestUpdateLastLoginAt_And_ListUsers(t *testing.T) {
	database := setupTestDB(t)

	u, err := createUser(database, "testlastlogin@example.com", "hash", "Test User", "de")
	if err != nil {
		t.Fatalf("createUser failed: %v", err)
	}

	// Initially LastLoginAt should be nil
	fetched, err := getUserByID(database, u.ID)
	if err != nil {
		t.Fatalf("getUserByID failed: %v", err)
	}
	if fetched.LastLoginAt != nil {
		t.Errorf("expected LastLoginAt to be nil initially, got %v", fetched.LastLoginAt)
	}

	// Update last login
	ts, err := updateLastLoginAt(database, u.ID)
	if err != nil {
		t.Fatalf("updateLastLoginAt failed: %v", err)
	}
	if ts.IsZero() {
		t.Errorf("expected non-zero timestamp from updateLastLoginAt")
	}

	// Stale / non-existent ID should return error (sql.ErrNoRows)
	if _, err := updateLastLoginAt(database, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Errorf("expected updateLastLoginAt with non-existent ID to return error, got nil")
	}

	// Verify getUserByID returns non-nil LastLoginAt
	fetchedAfter, err := getUserByID(database, u.ID)
	if err != nil {
		t.Fatalf("getUserByID after update failed: %v", err)
	}
	if fetchedAfter.LastLoginAt == nil {
		t.Fatal("expected LastLoginAt to be non-nil after updateLastLoginAt")
	}

	// Verify getUserByEmail returns non-nil LastLoginAt
	byEmail, err := getUserByEmail(context.Background(), database, u.Email)
	if err != nil {
		t.Fatalf("getUserByEmail failed: %v", err)
	}
	if byEmail.LastLoginAt == nil {
		t.Errorf("expected getUserByEmail to return non-nil LastLoginAt")
	}

	// Verify listUsers includes LastLoginAt
	users, _, err := listUsers(database, UserListParams{Query: "testlastlogin@example.com", Page: 1, Limit: 10})
	if err != nil {
		t.Fatalf("listUsers failed: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("listUsers count = %d, want 1", len(users))
	}
	if users[0].LastLoginAt == nil {
		t.Errorf("expected listUsers item to have non-nil LastLoginAt")
	}
}

func TestActiveAdminGovernanceMutationsAreConcurrentSafe(t *testing.T) {
	mutations := []activeAdminMutation{
		{
			name:  "suspend",
			apply: suspendActiveAdmin,
		},
		{
			name:  "delete",
			apply: deleteUser,
		},
		{
			name:  "demote",
			apply: demoteActiveAdmin,
		},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			database := setupTestDB(t)
			adminA, err := createUserWithRole(database, "admin-a@example.test", "hash", "Admin A", "ADMIN", false, "en")
			if err != nil {
				t.Fatalf("create admin A: %v", err)
			}
			adminB, err := createUserWithRole(database, "admin-b@example.test", "hash", "Admin B", "ADMIN", false, "en")
			if err != nil {
				t.Fatalf("create admin B: %v", err)
			}

			start := make(chan struct{})
			errs := make(chan error, 2)
			var wg sync.WaitGroup
			for _, id := range []string{adminA.ID, adminB.ID} {
				wg.Add(1)
				go func(id string) {
					defer wg.Done()
					<-start
					errs <- mutation.apply(database, id)
				}(id)
			}
			close(start)
			wg.Wait()
			close(errs)

			var succeeded, rejected int
			for err := range errs {
				switch {
				case err == nil:
					succeeded++
				case errors.Is(err, ErrLastActiveAdmin):
					rejected++
				default:
					t.Fatalf("mutation returned unexpected error: %v", err)
				}
			}
			if succeeded != 1 || rejected != 1 {
				t.Fatalf("results = %d succeeded, %d rejected; want 1 each", succeeded, rejected)
			}
			count, err := countActiveAdmins(database)
			if err != nil {
				t.Fatalf("count active admins: %v", err)
			}
			if count < 1 {
				t.Fatal("governance mutation removed every active administrator")
			}
		})
	}
}

func TestConsumeTOTPBackupCode_ConcurrentRequestsConsumeOnlyOnce(t *testing.T) {
	database := setupTestDB(t)
	user, err := createUser(database, "totp-recovery@example.test", "hash", "Recovery User", "en")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	code := "ABCDEFGH23"
	hash, err := totp2fa.HashBackupCode(code)
	if err != nil {
		t.Fatalf("hash backup code: %v", err)
	}
	if _, err := database.Exec(`
		UPDATE users
		SET totp_enabled = TRUE,
		    totp_backup_codes = $1,
		    totp_failed_attempts = 3
		WHERE id = $2
	`, StringArray{hash}, user.ID); err != nil {
		t.Fatalf("configure TOTP user: %v", err)
	}

	type consumeResult struct {
		consumed bool
		err      error
	}

	start := make(chan struct{})
	results := make(chan consumeResult, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			consumed, err := consumeTOTPBackupCode(context.Background(), database, user.ID, hash)
			results <- consumeResult{consumed: consumed, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var consumed int
	for result := range results {
		if result.err != nil {
			t.Fatalf("consume backup code: %v", result.err)
		}
		if result.consumed {
			consumed++
		}
	}
	if consumed != 1 {
		t.Fatalf("backup-code consumption successes = %d, want 1", consumed)
	}

	updated, err := getUserByID(database, user.ID)
	if err != nil {
		t.Fatalf("load updated user: %v", err)
	}
	if len(updated.TotpBackupCodes) != 0 {
		t.Fatalf("backup codes remaining = %d, want 0", len(updated.TotpBackupCodes))
	}
	if updated.TotpFailedAttempts != 0 || updated.TotpLockedUntil.Valid {
		t.Fatalf("TOTP failure state not reset: attempts=%d locked=%v", updated.TotpFailedAttempts, updated.TotpLockedUntil.Valid)
	}
}

func TestSetUserTOTPSecret_RejectsWhenAlreadyEnabled(t *testing.T) {
	database := setupTestDB(t)
	user, err := createUser(database, "totp-setup-guard@example.test", "hash", "Guard User", "en")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	secretA := "encrypted-secret-A"
	secretB := "encrypted-secret-B"

	ok, err := setUserTOTPSecret(database, user.ID, secretA)
	if err != nil {
		t.Fatalf("set initial secret: %v", err)
	}
	if !ok {
		t.Fatal("expected set initial secret to succeed")
	}

	ok, err = enableUserTOTP(database, user.ID, secretA, StringArray{"hash1"})
	if err != nil {
		t.Fatalf("enable TOTP: %v", err)
	}
	if !ok {
		t.Fatal("expected enable TOTP to succeed")
	}

	// Attempting to set a new secret while TOTP is already enabled must fail
	ok, err = setUserTOTPSecret(database, user.ID, secretB)
	if err != nil {
		t.Fatalf("set secret on enabled account: %v", err)
	}
	if ok {
		t.Fatal("expected setUserTOTPSecret to be rejected when TOTP is already enabled")
	}

	updated, err := getUserByID(database, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if updated.TotpSecretEnc != secretA {
		t.Fatalf("secret overwritten: got %q, want %q", updated.TotpSecretEnc, secretA)
	}
}

func TestEnableUserTOTP_RejectsMismatchedSecretOrAlreadyEnabled(t *testing.T) {
	database := setupTestDB(t)
	user, err := createUser(database, "totp-enable-guard@example.test", "hash", "Enable User", "en")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	secretA := "encrypted-secret-A"
	secretB := "encrypted-secret-B"

	ok, err := setUserTOTPSecret(database, user.ID, secretA)
	if err != nil || !ok {
		t.Fatalf("set initial secret: ok=%v, err=%v", ok, err)
	}

	// Attempt to enable with mismatched secret B
	ok, err = enableUserTOTP(database, user.ID, secretB, StringArray{"hash1"})
	if err != nil {
		t.Fatalf("enable with mismatched secret: %v", err)
	}
	if ok {
		t.Fatal("expected enableUserTOTP to be rejected with mismatched secret")
	}

	u, err := getUserByID(database, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if u.TotpEnabled {
		t.Fatal("user should not have TOTP enabled")
	}

	// Enable with correct secret A
	ok, err = enableUserTOTP(database, user.ID, secretA, StringArray{"hash1"})
	if err != nil || !ok {
		t.Fatalf("enable with valid secret: ok=%v, err=%v", ok, err)
	}

	u, err = getUserByID(database, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !u.TotpEnabled {
		t.Fatal("user should have TOTP enabled")
	}

	// Attempt to enable again when already enabled
	ok, err = enableUserTOTP(database, user.ID, secretA, StringArray{"hash2"})
	if err != nil {
		t.Fatalf("enable when already enabled: %v", err)
	}
	if ok {
		t.Fatal("expected enableUserTOTP to be rejected when already enabled")
	}
}

