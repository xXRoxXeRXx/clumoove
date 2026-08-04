package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

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
			_, err := CreateInitialAdmin(
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
			t.Fatalf("CreateInitialAdmin returned unexpected error: %v", err)
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

	u, err := CreateUser(database, "testlastlogin@example.com", "hash", "Test User", "de")
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// Initially LastLoginAt should be nil
	fetched, err := GetUserByID(database, u.ID)
	if err != nil {
		t.Fatalf("GetUserByID failed: %v", err)
	}
	if fetched.LastLoginAt != nil {
		t.Errorf("expected LastLoginAt to be nil initially, got %v", fetched.LastLoginAt)
	}

	// Update last login
	ts, err := UpdateLastLoginAt(database, u.ID)
	if err != nil {
		t.Fatalf("UpdateLastLoginAt failed: %v", err)
	}
	if ts.IsZero() {
		t.Errorf("expected non-zero timestamp from UpdateLastLoginAt")
	}

	// Stale / non-existent ID should return error (sql.ErrNoRows)
	if _, err := UpdateLastLoginAt(database, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Errorf("expected UpdateLastLoginAt with non-existent ID to return error, got nil")
	}

	// Verify GetUserByID returns non-nil LastLoginAt
	fetchedAfter, err := GetUserByID(database, u.ID)
	if err != nil {
		t.Fatalf("GetUserByID after update failed: %v", err)
	}
	if fetchedAfter.LastLoginAt == nil {
		t.Fatal("expected LastLoginAt to be non-nil after UpdateLastLoginAt")
	}

	// Verify GetUserByEmail returns non-nil LastLoginAt
	byEmail, err := GetUserByEmail(context.Background(), database, u.Email)
	if err != nil {
		t.Fatalf("GetUserByEmail failed: %v", err)
	}
	if byEmail.LastLoginAt == nil {
		t.Errorf("expected GetUserByEmail to return non-nil LastLoginAt")
	}

	// Verify ListUsers includes LastLoginAt
	users, _, err := ListUsers(database, UserListParams{Query: "testlastlogin@example.com", Page: 1, Limit: 10})
	if err != nil {
		t.Fatalf("ListUsers failed: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("ListUsers count = %d, want 1", len(users))
	}
	if users[0].LastLoginAt == nil {
		t.Errorf("expected ListUsers item to have non-nil LastLoginAt")
	}
}
