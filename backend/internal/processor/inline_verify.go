package processor

import (
	"context"
	"database/sql"
	"time"

	"backend/internal/db"
	"backend/internal/storage"
)

type fileHasher interface {
	GetFileHash(ctx context.Context, resourceType, path string) (string, error)
}

func getFileHashByResourceID(ctx context.Context, client fileHasher, resourceType, path, resourceID string) (string, error) {
	if resourceID != "" {
		if verifier, ok := client.(storage.TargetResourceVerifier); ok {
			return verifier.GetFileHashByResourceID(ctx, resourceType, resourceID)
		}
	}
	return client.GetFileHash(ctx, resourceType, path)
}

// tryInlineHashVerify attempts an immediate cryptographic checksum verification
// directly after an upload using the existing connected target provider client.
// Returns true if verification succeeded and updates task.ChecksumVerified and task.TargetHash.
func tryInlineHashVerify(ctx context.Context, task *db.Task, targetClient fileHasher, resourceType, targetPath string) bool {
	return tryInlineHashVerifyByResourceID(ctx, task, targetClient, resourceType, targetPath, "")
}

func tryInlineHashVerifyByResourceID(ctx context.Context, task *db.Task, targetClient fileHasher, resourceType, targetPath, targetResourceID string) bool {
	verifyCtx, verifyCancel := context.WithTimeout(ctx, 15*time.Second)
	defer verifyCancel()

	targetHash, errHash := getFileHashByResourceID(verifyCtx, targetClient, resourceType, targetPath, targetResourceID)
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
