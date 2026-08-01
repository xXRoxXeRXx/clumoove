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
