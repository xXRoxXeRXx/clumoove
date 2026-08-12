package sanitize

import (
	"context"
	"fmt"
	"path"
	"strings"

	"backend/internal/storage"
)

const maxRenameAttempts = 100

func ResolveCollision(ctx context.Context, client storage.StorageProvider, resourceType, dirPath, fileName string, targetProvider string) (string, error) {
	ext := ""
	base := fileName
	if idx := strings.LastIndex(fileName, "."); idx > 0 {
		ext = fileName[idx:]
		base = fileName[:idx]
	}

	listing, err := client.GetDirectoryListing(ctx, resourceType, dirPath)
	if err != nil {
		return "", fmt.Errorf("failed to list target directory for collision resolution: %w", err)
	}

	existing := make(map[string]struct{}, len(listing))
	for _, resource := range listing {
		name := resource.Name
		if name == "" {
			name = path.Base(resource.Path)
		}
		existing[name] = struct{}{}
	}

	for counter := 1; counter <= maxRenameAttempts; counter++ {
		candidate := collisionCandidate(base, ext, counter, targetProvider)
		candidate = SanitizeFilename(candidate, targetProvider).SanitizedName
		if _, exists := existing[candidate]; exists {
			continue
		}
		if IsCaseInsensitive(targetProvider) && hasCaseInsensitiveCollision(listing, candidate) {
			continue
		}

		return candidate, nil
	}

	return "", fmt.Errorf("failed to resolve collision after %d attempts", maxRenameAttempts)
}

func hasCaseInsensitiveCollision(listing []storage.CloudResource, candidate string) bool {
	for _, resource := range listing {
		name := resource.Name
		if name == "" {
			name = path.Base(resource.Path)
		}
		if strings.EqualFold(name, candidate) {
			return true
		}
	}
	return false
}

func collisionCandidate(base, ext string, counter int, targetProvider string) string {
	maxLen := getMaxFilenameLength(targetProvider)
	suffix := fmt.Sprintf("_%d%s", counter, ext)
	if filenameLength(suffix, targetProvider) >= maxLen {
		return truncateFilename(suffix, maxLen, targetProvider)
	}

	availableBase := maxLen - filenameLength(suffix, targetProvider)
	return truncateFilename(base, availableBase, targetProvider) + suffix
}
