package queue

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/lib/pq"

	"backend/internal/db"
	"backend/internal/observability"
)

// RestoreItemPayload represents an in-flight claimed restore item.
type RestoreItemPayload struct {
	RestoreItemID        string `json:"restore_item_id"`
	RestoreRunID         string `json:"restore_run_id"`
	SnapshotRelativePath string `json:"snapshot_relative_path"`
	TargetPath           string `json:"target_path"`
	IsDir                bool   `json:"is_dir"`
	ClaimEpoch           int64  `json:"claim_epoch"`
}

// DequeueRestoreItem claims the next runnable item for a restore run while
// respecting the per-job thread cap and enforcing parent-first directory ordering.
func DequeueRestoreItem(ctx context.Context, database *sql.DB, workerID string) (*db.RestoreItem, error) {
	return db.ClaimNextRestoreItemForWorkerContext(ctx, database, workerID)
}

// HeartbeatRestoreItem extends the claim deadline for an active restore item.
func HeartbeatRestoreItem(ctx context.Context, database *sql.DB, itemID, runID, workerID string, claimEpoch int64) error {
	return db.HeartbeatRestoreItemContext(ctx, database, itemID, runID, workerID, claimEpoch)
}

// RecoverStaleRestoreItems resets expired running restore items back to PENDING.
func RecoverStaleRestoreItems(ctx context.Context, database *sql.DB) (int64, error) {
	return db.RecoverStaleRestoreItemsContext(ctx, database)
}

// ListenRestoreEvents connects a PostgreSQL listener to restore notification channels.
func ListenRestoreEvents(ctx context.Context, connStr string, onEvent func(channel, payload string)) error {
	if connStr == "" {
		return nil
	}
	minReconn := 10 * time.Second
	maxReconn := 1 * time.Minute
	listener := pq.NewListener(connStr, minReconn, maxReconn, func(ev pq.ListenerEventType, err error) {
		if err != nil {
			slog.WarnContext(ctx, "restore_pq_listener_event", slog.String("component", "queue"), observability.Error(err))
		}
	})
	defer listener.Close()

	if err := listener.Listen("restore_item_available"); err != nil {
		return fmt.Errorf("listen restore_item_available: %w", err)
	}
	if err := listener.Listen("restore_run_available"); err != nil {
		return fmt.Errorf("listen restore_run_available: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case n := <-listener.Notify:
			if n == nil {
				continue
			}
			if onEvent != nil {
				onEvent(n.Channel, n.Extra)
			}
		case <-time.After(time.Minute):
			go func() {
				_ = listener.Ping()
			}()
		}
	}
}
