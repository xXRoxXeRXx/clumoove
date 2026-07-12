package sanitize

import (
	"context"
	"fmt"
	"strings"

	"backend/internal/storage"
)

func ResolveCollision(ctx context.Context, client storage.StorageProvider, resourceType, dirPath, fileName string, targetProvider string) (string, error) {
	ext := ""
	base := fileName
	if idx := strings.LastIndex(fileName, "."); idx > 0 {
		ext = fileName[idx:]
		base = fileName[:idx]
	}

	for counter := 1; counter <= 100; counter++ {
		candidate := fmt.Sprintf("%s_%d%s", base, counter, ext)
		candidatePath := dirPath + "/" + candidate

		exists, _, err := client.FileExists(ctx, resourceType, candidatePath)
		if err != nil {
			return "", fmt.Errorf("failed to check existence of collision candidate: %w", err)
		}
		if exists {
			continue
		}

		if IsCaseInsensitive(targetProvider) {
			collision, err := CheckCaseCollision(ctx, client, resourceType, dirPath, candidate)
			if err != nil {
				return "", fmt.Errorf("failed to check case collision for collision candidate: %w", err)
			}
			if collision != "" {
				continue
			}
		}

		return candidate, nil
	}

	return "", fmt.Errorf("failed to resolve collision after 100 attempts")
}
