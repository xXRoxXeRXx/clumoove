package storage

import (
	"context"
	"fmt"
	"net/url"
)

type localUserContextKey struct{}

// WithLocalUserScope attaches a server-derived user ID to a provider context.
// It is intentionally separate from provider credentials and request paths.
func WithLocalUserScope(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, localUserContextKey{}, userID)
}

func localUserID(ctx context.Context) string {
	userID, _ := ctx.Value(localUserContextKey{}).(string)
	return userID
}

// ValidProviders is the canonical list of supported storage providers. It is
// the single source of truth shared by the NewProvider switch below and any
// request-time whitelist checks (e.g. main.go handleConnect), so adding a
// provider only requires updating the switch — not every call site.
var ValidProviders = []string{
	"nextcloud", "webdav", "dropbox", "google", "onedrive", "hidrive", "smb", "s3", "sftp", "magentacloud", "local", "immich",
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

// ProviderMetadata defines static capabilities and connection requirements for a storage provider.
type ProviderMetadata struct {
	Type                   string
	RequiresHost           bool
	IsVirtual              bool
	SupportedResourceTypes map[string]bool
}

var providerRegistry = map[string]ProviderMetadata{
	"nextcloud": {
		Type:                   "nextcloud",
		RequiresHost:           true,
		SupportedResourceTypes: map[string]bool{"files": true, "calendars": true, "contacts": true},
	},
	"google": {
		Type:                   "google",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true, "calendars": true, "contacts": true},
	},
	"onedrive": {
		Type:                   "onedrive",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true},
	},
	"webdav": {
		Type:                   "webdav",
		RequiresHost:           true,
		SupportedResourceTypes: map[string]bool{"files": true},
	},
	"dropbox": {
		Type:                   "dropbox",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true},
	},
	"hidrive": {
		Type:                   "hidrive",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true},
	},
	"smb": {
		Type:                   "smb",
		RequiresHost:           true,
		SupportedResourceTypes: map[string]bool{"files": true},
	},
	"s3": {
		Type:                   "s3",
		RequiresHost:           true,
		SupportedResourceTypes: map[string]bool{"files": true},
	},
	"sftp": {
		Type:                   "sftp",
		RequiresHost:           true,
		SupportedResourceTypes: map[string]bool{"files": true},
	},
	"magentacloud": {
		Type:                   "magentacloud",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true},
	},
	"local": {
		Type:                   "local",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true},
	},
	"immich": {
		Type:                   "immich",
		RequiresHost:           true,
		IsVirtual:              true,
		SupportedResourceTypes: map[string]bool{"files": true},
	},
}

// ProviderSupportsResourceType reports whether providerType supports the given resourceType.
func ProviderSupportsResourceType(providerType, resourceType string) bool {
	meta, exists := providerRegistry[providerType]
	if !exists {
		return false
	}
	return meta.SupportedResourceTypes[resourceType]
}

// IsVirtualProvider reports whether providerType uses virtual item references rather than filesystem paths.
func IsVirtualProvider(providerType string) bool {
	meta, exists := providerRegistry[providerType]
	return exists && meta.IsVirtual
}

// ValidateProviderURL verifies that a provider which needs a host actually has a
// URL with a non-empty host. It is called at profile create/update and at
// migration start so a host-based provider with a blank URL is rejected up front
// with a clean error code instead of failing cryptically deep inside indexing
// (where the raw Go error would otherwise leak to the client). The SSRF egress
// policy itself is still enforced later inside NewProvider.
func ValidateProviderURL(providerType, urlStr string) error {
	meta, exists := providerRegistry[providerType]
	if !exists || !meta.RequiresHost {
		return nil
	}
	parsed, err := url.Parse(urlStr)
	if err != nil || parsed.Hostname() == "" {
		return fmt.Errorf("provider %q requires a valid URL with a host", providerType)
	}
	return nil
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
		providerType == "smb" || providerType == "sftp" || providerType == "immich" {
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
	case "onedrive":
		return NewOneDriveProvider(password)
	case "hidrive":
		return NewHiDriveProvider(password)
	case "smb":
		return NewSMBProvider(urlStr, username, password)
	case "s3":
		return NewS3Provider(urlStr, username, password)
	case "local":
		// Local reads/writes files inside LOCAL_STORAGE_ROOT. It takes no URL,
		// username, or password and performs no network egress (SSRF guard skipped).
		return NewLocalProvider(localUserID(ctx))
	case "sftp":
		return NewSFTPProvider(urlStr, username, password)
	case "immich":
		return NewImmichProvider(urlStr, password)
	default:
		return nil, fmt.Errorf("unsupported provider type: %q", providerType)
	}
}
