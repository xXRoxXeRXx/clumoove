package sanitize

import (
	"context"
	"strings"

	"backend/internal/storage"
)

// CheckCaseCollision reports whether the target directory contains a file
// whose name differs from fileName only in casing. Existing directories are
// intentionally skipped: creating a directory that differs only by case is
// idempotent on case-insensitive targets.
func CheckCaseCollision(ctx context.Context, client storage.StorageProvider, resourceType, dirPath, fileName string) (string, error) {
	listing, err := client.GetDirectoryListing(ctx, resourceType, dirPath)
	if err != nil {
		return "", err
	}

	for _, res := range listing {
		if res.IsDir {
			continue
		}
		if strings.EqualFold(res.Name, fileName) && res.Name != fileName {
			return res.Name, nil
		}
	}
	return "", nil
}
