package storage

import (
	"context"
	"fmt"
)

func NewProvider(ctx context.Context, providerType, urlStr, username, password string) (StorageProvider, error) {
	switch providerType {
	case "nextcloud":
		return NewNextcloudProvider(urlStr, username, password)
	case "webdav":
		return NewWebDAVProvider(urlStr, username, password)
	case "dropbox":
		return NewDropboxProvider(password)
	case "google":
		// The OAuth token is passed in the password field for OAuth providers
		return NewGoogleProvider(ctx, password)
	default:
		return nil, fmt.Errorf("unsupported provider type: %q", providerType)
	}
}
