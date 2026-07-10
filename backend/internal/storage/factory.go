package storage

import (
	"context"
	"fmt"
	"net/url"
)

func NewProvider(ctx context.Context, providerType, urlStr, username, password string) (StorageProvider, error) {
	// Sanitize URL credentials to prevent leakage in url.Error (Finding 2)
	if providerType == "nextcloud" || providerType == "webdav" {
		if parsed, err := url.Parse(urlStr); err == nil && parsed.User != nil {
			if username == "" {
				username = parsed.User.Username()
			}
			if password == "" {
				if pass, ok := parsed.User.Password(); ok {
					password = pass
				}
			}
			parsed.User = nil
			urlStr = parsed.String()
		}
	}

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
	case "smb":
		return NewSMBProvider(urlStr, username, password)
	default:
		return nil, fmt.Errorf("unsupported provider type: %q", providerType)
	}
}

