package processor

import (
	"context"
	"database/sql"
	"time"

	"backend/internal/db"
	"backend/internal/storage"
)

// tryInlineHashVerify attempts an immediate cryptographic checksum verification
// directly after an upload using the existing connected target provider client.
// Returns true if verification succeeded and updates task.ChecksumVerified and task.TargetHash.
func tryInlineHashVerify(ctx context.Context, task *db.Task, targetClient storage.FileHasher, resourceType, targetPath string) bool {
	return tryInlineHashVerifyByResourceID(ctx, task, targetClient, resourceType, targetPath, "")
}

func tryInlineHashVerifyByResourceID(ctx context.Context, task *db.Task, targetClient storage.FileHasher, resourceType, targetPath, targetResourceID string) bool {
	verifyCtx, verifyCancel := context.WithTimeout(ctx, 15*time.Second)
	defer verifyCancel()

	targetHash, errHash := storage.GetFileHashByResourceID(verifyCtx, targetClient, resourceType, targetPath, targetResourceID)
	if errHash != nil || targetHash == "" {
		return false
	}
	if cmp := storage.CompareTargetHash(task.SourceHash.String, task.WorkerHash.String, targetHash); cmp.Matched {
		task.ChecksumVerified = true
		task.TargetHash = sql.NullString{String: targetHash, Valid: true}
		return true
	}
	return false
}
