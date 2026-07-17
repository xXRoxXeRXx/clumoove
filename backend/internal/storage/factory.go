package storage

import (
	"context"
	"fmt"
	"net/url"
)

// ValidProviders is the canonical list of supported storage providers. It is
// the single source of truth shared by the NewProvider switch below and any
// request-time whitelist checks (e.g. main.go handleConnect), so adding a
// provider only requires updating the switch — not every call site.
var ValidProviders = []string{
	"nextcloud", "webdav", "dropbox", "google", "googlephotos", "smb", "s3", "sftp", "magentacloud",
}

// IsValidProvider reports whether p is a supported storage provider.
func IsValidProvider(p string) bool {
	for _, v := range ValidProviders {
		if v == p {
			return true
		}
	}
	return false
}

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

	// SSRF guard: reject egress to loopback / link-local (and private ranges
	// when MIGRATION_BLOCK_PRIVATE is set) for providers that connect to a
	// user-supplied host.
	if providerType == "nextcloud" || providerType == "webdav" ||
		providerType == "smb" || providerType == "sftp" {
		if err := validateEgressURL(urlStr); err != nil {
			return nil, err
		}
	}

	switch providerType {
	case "nextcloud":
		return NewNextcloudProvider(urlStr, username, password)
	case "magentacloud":
		// MagentaCLOUD has a fixed public WebDAV endpoint, so urlStr is ignored.
		return NewMagentacloudProvider(username, password)
	case "webdav":
		return NewWebDAVProvider(urlStr, username, password)
	case "dropbox":
		return NewDropboxProvider(password)
	case "google":
		// The OAuth token is passed in the password field for OAuth providers
		return NewGoogleProvider(ctx, password)
	case "googlephotos":
		// The OAuth token is passed in the password field for OAuth providers
		return NewGooglePhotosProvider(ctx, password)
	case "smb":
		return NewSMBProvider(urlStr, username, password)
	case "s3":
		return NewS3Provider(urlStr, username, password)
	case "sftp":
		return NewSFTPProvider(urlStr, username, password)
	default:
		return nil, fmt.Errorf("unsupported provider type: %q", providerType)
	}
}

