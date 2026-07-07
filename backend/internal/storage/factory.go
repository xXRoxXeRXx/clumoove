package storage

import (
	"fmt"
)

func NewProvider(providerType, urlStr, username, password string) (StorageProvider, error) {
	switch providerType {
	case "nextcloud":
		return NewNextcloudProvider(urlStr, username, password)
	case "dropbox":
		return NewDropboxProvider(password)
	default:
		return nil, fmt.Errorf("unsupported provider type: %q", providerType)
	}
}
