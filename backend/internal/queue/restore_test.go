package queue

import (
	"context"
	"testing"
	"time"
)

func TestRestoreItemPayload(t *testing.T) {
	payload := RestoreItemPayload{
		RestoreItemID:        "item-1",
		RestoreRunID:         "run-1",
		SnapshotRelativePath: "documents/file.pdf",
		TargetPath:           "restored/documents/file.pdf",
		IsDir:                false,
		ClaimEpoch:           1,
	}

	if payload.RestoreItemID != "item-1" || payload.IsDir {
		t.Fatalf("unexpected payload state: %+v", payload)
	}
}

func TestListenRestoreEventsEmptyConn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := ListenRestoreEvents(ctx, "", func(ch, extra string) {})
	if err != nil {
		t.Fatalf("expected nil for empty connStr, got: %v", err)
	}
}
