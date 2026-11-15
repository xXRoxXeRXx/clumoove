package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

type retryFailExister struct{}

func (*retryFailExister) FileExists(context.Context, string, string) (bool, int64, error) {
	return false, 0, errors.New("transient target error")
}

func TestVerifyTargetSizeByResourceIDStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	_, _, err := VerifyTargetSizeByResourceID(ctx, &retryFailExister{}, "files", "/file", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("VerifyTargetSizeByResourceID() error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("VerifyTargetSizeByResourceID() waited %v after cancellation", elapsed)
	}
}
