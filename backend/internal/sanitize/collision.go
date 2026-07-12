package sanitize

import (
	"context"
	"strings"

	"backend/internal/storage"
)

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
