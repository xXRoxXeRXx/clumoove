package storage

import (
	"context"
	"time"
)

// FileExister checks whether a target resource exists and reports its byte size.
type FileExister interface {
	FileExists(ctx context.Context, resourceType, path string) (bool, int64, error)
}

// FileHasher retrieves a server-computed hash string for a target resource.
type FileHasher interface {
	GetFileHash(ctx context.Context, resourceType, path string) (string, error)
}

// GetFileHashByResourceID queries the target hash using a persisted target
// resource ID when supported by the provider, otherwise by path.
func GetFileHashByResourceID(ctx context.Context, client FileHasher, resourceType, path, resourceID string) (string, error) {
	if resourceID != "" {
		if verifier, ok := client.(TargetResourceVerifier); ok {
			return verifier.GetFileHashByResourceID(ctx, resourceType, resourceID)
		}
	}
	return client.GetFileHash(ctx, resourceType, path)
}

// VerifyTargetSizeByResourceID queries target existence and size with bounded
// retries for transient provider failures, stopping immediately if ctx is cancelled.
func VerifyTargetSizeByResourceID(ctx context.Context, client FileExister, resourceType, path, resourceID string) (exists bool, size int64, err error) {
	verifier, hasResourceVerifier := client.(TargetResourceVerifier)
	for attempt := 0; attempt < 3; attempt++ {
		if resourceID != "" && hasResourceVerifier {
			exists, size, err = verifier.FileExistsByResourceID(ctx, resourceType, resourceID)
		} else {
			exists, size, err = client.FileExists(ctx, resourceType, path)
		}
		if err == nil {
			return exists, size, nil
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return exists, size, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return exists, size, err
}
