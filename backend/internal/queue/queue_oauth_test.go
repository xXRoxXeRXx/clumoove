package queue

import (
	"context"
	"testing"
	"time"
)

func TestTryClaimOAuthLockLocalFallback(t *testing.T) {
	var q *Queue // nil Queue (no Redis)

	token, claimed, err := q.TryClaimOAuthLock(context.Background(), "sync", "job-1", "source", 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !claimed {
		t.Fatal("expected claimed = true for local fallback")
	}
	if token != "local-no-redis" {
		t.Fatalf("expected local-no-redis token, got %q", token)
	}

	err = q.ReleaseOAuthLock(context.Background(), "sync", "job-1", "source", token)
	if err != nil {
		t.Fatalf("unexpected release error: %v", err)
	}
}

func TestGenerateRandomLockToken(t *testing.T) {
	t1 := generateRandomLockToken()
	t2 := generateRandomLockToken()

	if t1 == "" || t2 == "" {
		t.Fatal("generated empty token")
	}
	if t1 == t2 {
		t.Fatalf("generated duplicate lock tokens: %s", t1)
	}
}

func TestRedisOAuthLockOwnership(t *testing.T) {
	redisURL := "redis://:dev_redis_secure_pass_999@localhost:6379"
	q, err := NewQueue(redisURL)
	if err != nil {
		t.Skipf("Redis not available; skipping Redis-backed lock test: %v", err)
	}

	ctx := context.Background()
	tokenA, claimedA, err := q.TryClaimOAuthLock(ctx, "migration", "mig-lock-test", "source", 10*time.Second)
	if err != nil || !claimedA {
		t.Fatalf("instance A failed to claim lock: claimed=%v, err=%v", claimedA, err)
	}

	// Instance B tries to claim the same lock -> must fail (claimed = false)
	_, claimedB, err := q.TryClaimOAuthLock(ctx, "migration", "mig-lock-test", "source", 10*time.Second)
	if err != nil {
		t.Fatalf("instance B error claiming lock: %v", err)
	}
	if claimedB {
		t.Fatal("instance B claimed lock while instance A held it")
	}

	// Instance B tries to release instance A's lock with a fake token -> must NOT delete lock
	err = q.ReleaseOAuthLock(ctx, "migration", "mig-lock-test", "source", "fake-token-B")
	if err != nil {
		t.Fatalf("release with wrong token error: %v", err)
	}

	// Lock should still be held by instance A
	_, claimedB2, err := q.TryClaimOAuthLock(ctx, "migration", "mig-lock-test", "source", 10*time.Second)
	if claimedB2 {
		t.Fatal("lock was deleted by wrong token release")
	}

	// Instance A releases with correct token -> lock released
	err = q.ReleaseOAuthLock(ctx, "migration", "mig-lock-test", "source", tokenA)
	if err != nil {
		t.Fatalf("release with correct token error: %v", err)
	}

	// Instance B can now claim lock
	tokenB3, claimedB3, err := q.TryClaimOAuthLock(ctx, "migration", "mig-lock-test", "source", 10*time.Second)
	if err != nil || !claimedB3 {
		t.Fatalf("instance B failed to claim lock after release: claimed=%v, err=%v", claimedB3, err)
	}
	_ = q.ReleaseOAuthLock(ctx, "migration", "mig-lock-test", "source", tokenB3)
}
