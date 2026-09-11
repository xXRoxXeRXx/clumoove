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
func tryInlineHashVerify(ctx context.Context, task *db.Task, targetClient storage.StorageProvider, resourceType, targetPath string) bool {
	verifyCtx, verifyCancel := context.WithTimeout(ctx, 15*time.Second)
	defer verifyCancel()

	targetHash, errHash := targetClient.GetFileHash(verifyCtx, resourceType, targetPath)
	if errHash != nil || targetHash == "" {
		return false
	}
	targetAlgo, cleanTarget := storage.ParseHashString(targetHash)
	srcHash := bestSourceHash(task)
	if task.WorkerHash.Valid {
		wAlgo, _ := storage.ParseHashString(task.WorkerHash.String)
		sAlgo, _ := storage.ParseHashString(srcHash)
		if wAlgo == targetAlgo && sAlgo != targetAlgo {
			srcHash = task.WorkerHash.String
		}
	}
	if srcHash == "" {
		return false
	}
	sourceAlgo, cleanSource := storage.ParseHashString(srcHash)
	if isComparableHash(sourceAlgo) && isComparableHash(targetAlgo) && sourceAlgo == targetAlgo {
		if cleanSource == cleanTarget {
			task.ChecksumVerified = true
			task.TargetHash = sql.NullString{String: targetHash, Valid: true}
			return true
		}
	}
	return false
}
