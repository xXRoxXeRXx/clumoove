package scheduler

import (
	"testing"
	"time"
)

func TestValidateCronExpression(t *testing.T) {
	valid := []string{
		"*/30 * * * *", // every 30 minutes
		"0 * * * *",    // hourly
		"0 2 * * *",    // daily at 02:00
		"*/15 * * * *", // every 15 minutes
		"0 0 * * 0",    // weekly on Sunday midnight
		"0 0 1 * *",    // monthly on the 1st
		"@daily",       // robfig/cron descriptor (parsed by standard parser? no)
	}
	// Note: @daily is NOT supported by cron.ParseStandard; only 5-field expressions.
	valid = valid[:len(valid)-1]

	for _, expr := range valid {
		if err := ValidateCronExpression(expr); err != nil {
			t.Errorf("expected %q to be valid, got error: %v", expr, err)
		}
	}

	invalid := []string{
		"",
		"not-a-cron",
		"*/30 * *",       // too few fields
		"*/30 * * * * *", // too many fields (6-field with seconds)
		"99 * * * *",     // minute out of range
		"* * * *",        // too few fields
	}
	for _, expr := range invalid {
		if err := ValidateCronExpression(expr); err == nil {
			t.Errorf("expected %q to be invalid, but it passed validation", expr)
		}
	}
}

func TestNextRunEvery30Minutes(t *testing.T) {
	expr := "*/30 * * * *"
	now := time.Date(2026, 7, 11, 10, 5, 0, 0, time.UTC)
	next, err := NextRunFrom(expr, now)
	if err != nil {
		t.Fatalf("NextRunFrom returned error: %v", err)
	}

	// From 10:05, the next :00/:30 boundary is 10:30 → 25 minutes later.
	want := time.Date(2026, 7, 11, 10, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("NextRunFrom(%q, 10:05) = %s, want %s", expr, next.Format(time.RFC3339), want.Format(time.RFC3339))
	}

	diff := next.Sub(now)
	if diff != 25*time.Minute {
		t.Errorf("expected next run 25 minutes later, got %s", diff)
	}
}

func TestNextRunHourly(t *testing.T) {
	expr := "0 * * * *"
	now := time.Date(2026, 7, 11, 10, 45, 0, 0, time.UTC)
	next, err := NextRunFrom(expr, now)
	if err != nil {
		t.Fatalf("NextRunFrom returned error: %v", err)
	}

	want := time.Date(2026, 7, 11, 11, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("NextRunFrom(%q, 10:45) = %s, want %s", expr, next.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestNextRunDailyAt2AM(t *testing.T) {
	expr := "0 2 * * *"
	// Just after 02:00 → next run is tomorrow 02:00
	now := time.Date(2026, 7, 11, 3, 0, 0, 0, time.UTC)
	next, err := NextRunFrom(expr, now)
	if err != nil {
		t.Fatalf("NextRunFrom returned error: %v", err)
	}

	want := time.Date(2026, 7, 12, 2, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("NextRunFrom(%q, 03:00) = %s, want %s", expr, next.Format(time.RFC3339), want.Format(time.RFC3339))
	}

	// Just before 02:00 → next run is today 02:00
	now = time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)
	next, err = NextRunFrom(expr, now)
	if err != nil {
		t.Fatalf("NextRunFrom returned error: %v", err)
	}
	want = time.Date(2026, 7, 11, 2, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("NextRunFrom(%q, 01:00) = %s, want %s", expr, next.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestNextRunIsStrictlyInTheFuture(t *testing.T) {
	expr := "*/30 * * * *"
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC) // exactly on a boundary
	next, err := NextRunFrom(expr, now)
	if err != nil {
		t.Fatalf("NextRunFrom returned error: %v", err)
	}
	// cron.Next returns the *next* occurrence strictly after `now`, so 10:30 not 10:00.
	want := time.Date(2026, 7, 11, 10, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("NextRunFrom on boundary = %s, want %s (must be strictly future)", next.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestIsJobActiveStatus(t *testing.T) {
	active := []string{"RUNNING", "INDEXING"}
	for _, s := range active {
		if !isJobActiveStatus(s) {
			t.Errorf("status %q should be considered active (overlap protection)", s)
		}
	}

	inactive := []string{
		"PENDING", "SCHEDULED", "COMPLETED", "FAILED",
		"PAUSED_CONNECTION_LOSS", "", "UNKNOWN",
	}
	for _, s := range inactive {
		if isJobActiveStatus(s) {
			t.Errorf("status %q should NOT be considered active", s)
		}
	}
}
