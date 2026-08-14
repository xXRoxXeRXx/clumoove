package storage

import (
	"context"
	"fmt"
	"net/url"
	"runtime"
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

// ValidProviders is the ordered list used by API/UI consumers. Keep it in
// parity with providerRegistry, which is the source used for validation.
var ValidProviders = []string{
	"nextcloud", "opencloud", "webdav", "dropbox", "google", "onedrive", "hidrive", "smb", "s3", "sftp", "ftp", "magentacloud", "koofr", "local", "immich", "seafile", "mega",
}

// IsValidProvider reports whether p is a supported storage provider.
func IsValidProvider(p string) bool {
	_, ok := providerRegistry[p]
	return ok
}

// ProviderMetadata defines static capabilities and connection requirements for a storage provider.
type ProviderMetadata struct {
	Type                     string
	RequiresHost             bool
	RequiresHTTPS            bool
	RequiresEgressValidation bool
	IsVirtual                bool
	SupportedResourceTypes   map[string]bool
	SupportsAtomicRename     bool
	VerificationMode         VerificationMode
}

var providerRegistry = map[string]ProviderMetadata{
	"nextcloud": {
		Type:                     "nextcloud",
		RequiresHost:             true,
		RequiresHTTPS:            true,
		RequiresEgressValidation: true,
		SupportedResourceTypes:   map[string]bool{"files": true, "calendars": true, "contacts": true},
		SupportsAtomicRename:     true, VerificationMode: VerificationSizeOnly,
	},
	"opencloud": {
		Type:                     "opencloud",
		RequiresHost:             true,
		RequiresHTTPS:            true,
		RequiresEgressValidation: true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     true, VerificationMode: VerificationSizeOnly,
	},
	"google": {
		Type:                   "google",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true, "calendars": true, "contacts": true},
		SupportsAtomicRename:   true, VerificationMode: VerificationCryptographicHash,
	},
	"onedrive": {
		Type:                   "onedrive",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   true, VerificationMode: VerificationCryptographicHash,
	},
	"webdav": {
		Type:                     "webdav",
		RequiresHost:             true,
		RequiresHTTPS:            true,
		RequiresEgressValidation: true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     true, VerificationMode: VerificationSizeOnly,
	},
	"dropbox": {
		Type:                   "dropbox",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   true, VerificationMode: VerificationCryptographicHash,
	},
	"hidrive": {
		Type:                   "hidrive",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   true, VerificationMode: VerificationCryptographicHash,
	},
	"smb": {
		Type:                     "smb",
		RequiresHost:             true,
		RequiresEgressValidation: true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     true, VerificationMode: VerificationSizeOnly,
	},
	"s3": {
		Type:                   "s3",
		RequiresHost:           true,
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   false, VerificationMode: VerificationSizeOnly,
	},
	"sftp": {
		Type:                     "sftp",
		RequiresHost:             true,
		RequiresEgressValidation: true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     true, VerificationMode: VerificationSizeOnly,
	},
	"ftp": {
		Type:                     "ftp",
		RequiresHost:             true,
		RequiresEgressValidation: true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     true, VerificationMode: VerificationSizeOnly,
	},
	"magentacloud": {
		Type:                   "magentacloud",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   true, VerificationMode: VerificationSizeOnly,
	},
	"koofr": {
		Type:                   "koofr",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   false, VerificationMode: VerificationCryptographicHash,
	},
	"local": {
		Type:                   "local",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   true, VerificationMode: VerificationCryptographicHash,
	},
	"immich": {
		Type:                     "immich",
		RequiresHost:             true,
		RequiresHTTPS:            true,
		RequiresEgressValidation: true,
		IsVirtual:                true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     false, VerificationMode: VerificationCryptographicHash,
	},
	"seafile": {
		Type:                     "seafile",
		RequiresHost:             true,
		RequiresHTTPS:            true,
		RequiresEgressValidation: true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     false, VerificationMode: VerificationSizeOnly,
	},
	"mega": {
		Type:                   "mega",
		RequiresHost:           false,
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   true, VerificationMode: VerificationSizeOnly,
	},
}

// managerCapabilityRegistry is deliberately independent from the storage
// interface. The first file-manager release exposes only read behavior that is
// already covered by provider contracts; mutations remain false until each
// provider has manager-specific tests and semantics.
var managerCapabilityRegistry = map[string]ManagerCapabilities{
	// Google is the first provider with dedicated, ID-based manager contracts
	// and native cursor paging. Other providers remain disabled until their
	// manager-specific implementations and tests exist; migration primitives
	// are deliberately not a manager capability signal.
	"nextcloud":    {},
	"opencloud":    {},
	"webdav":       {},
	"dropbox":      {},
	"google":       {Browse: true, NativePagination: true, Download: true},
	"onedrive":     {},
	"hidrive":      {},
	"smb":          {},
	"s3":           {},
	"sftp":         {},
	"ftp":          {},
	"magentacloud": {},
	"koofr":        {},
	"local":        {},
	"immich":       {},
	"seafile":      {},
	"mega":         {},
}

// ManagerCapabilitiesFor returns static capabilities after applying runtime
// platform restrictions. It returns false for unknown providers.
func ManagerCapabilitiesFor(providerType string) ManagerCapabilities {
	capabilities, ok := managerCapabilityRegistry[providerType]
	if !ok {
		return ManagerCapabilities{}
	}
	if providerType == "local" && runtime.GOOS == "windows" {
		return ManagerCapabilities{}
	}
	return capabilities
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
	if meta.RequiresHTTPS && parsed.Scheme != "https" {
		return fmt.Errorf("provider %q requires an HTTPS URL", providerType)
	}
	return nil
}

func NewProvider(ctx context.Context, providerType, urlStr, username, password string) (StorageProvider, error) {
	// URL userinfo is accepted only for host-based providers that use the
	// explicit username/password fields. Extract and remove it before any
	// downstream parser can include the URL in an error. FTPS deliberately
	// rejects URL userinfo as part of its strict connection URL contract.
	if meta, exists := providerRegistry[providerType]; exists && meta.RequiresEgressValidation {
		if parsed, err := url.Parse(urlStr); err == nil && parsed.User != nil {
			if providerType == "ftp" {
				return nil, fmt.Errorf("FTPS URL must not contain userinfo")
			}
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
	if meta, exists := providerRegistry[providerType]; exists && meta.RequiresEgressValidation {
		if err := validateEgressURLContext(ctx, urlStr); err != nil {
			return nil, err
		}
	}

	switch providerType {
	case "nextcloud":
		return NewNextcloudProvider(urlStr, username, password)
	case "opencloud":
		return NewOpenCloudProvider(urlStr, username, password)
	case "magentacloud":
		// MagentaCLOUD has a fixed public WebDAV endpoint, so urlStr is ignored.
		return NewMagentacloudProvider(username, password)
	case "koofr":
		// Koofr has a fixed public endpoint, so urlStr is ignored.
		return NewKoofrProvider(username, password)
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
	case "ftp":
		return NewFTPProvider(urlStr, username, password)
	case "immich":
		return NewImmichProvider(urlStr, password)
	case "seafile":
		return NewSeafileProvider(urlStr, username, password)
	case "mega":
		// A missing scoped session is normal for a first MEGA connection; the
		// provider authenticates and establishes one when Connect is called.
		session, _ := MegaSessionFromContext(ctx)
		return NewMegaProvider(username, password, session), nil
	default:
		return nil, fmt.Errorf("unsupported provider type: %q", providerType)
	}
}
