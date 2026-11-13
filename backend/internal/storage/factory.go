package storage

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"reflect"
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

// ValidProviders is the ordered list used by API/UI consumers, derived directly
// from the authoritative provider catalogue.
var (
	defaultProviderCatalogue = mustBuildProviderCatalogue(providerCatalogueEntries)
	ValidProviders           = defaultProviderCatalogue.validProviders()
)

type providerRegistration struct {
	Type                      string
	RequiresHost              bool
	RequiresHTTPS             bool
	RequiresEgressValidation  bool
	RejectsURLUserinfo        bool
	IsVirtual                 bool
	SupportedResourceTypes    map[string]bool
	SupportsAtomicRename      bool
	VerificationMode          VerificationMode
	ManagerCapabilities       ManagerCapabilities
	WindowsManagerBlocked     bool
	ManagerPathMutation       bool
	ManagerRootNativeID       string
	ManagerDownloadByNativeID bool
	ManagerSingleOp           func(locator, destination ManagerLocator, name string) bool
	Construct                 func(ctx context.Context, urlStr, username, password string) (StorageProvider, error)
}

type providerCatalogue struct {
	order   []string
	entries map[string]providerRegistration
}

func mustBuildProviderCatalogue(entries []providerRegistration) *providerCatalogue {
	cat, err := newProviderCatalogue(entries)
	if err != nil {
		panic(err)
	}
	return cat
}

func newProviderCatalogue(entries []providerRegistration) (*providerCatalogue, error) {
	order := make([]string, 0, len(entries))
	byType := make(map[string]providerRegistration, len(entries))
	for _, entry := range entries {
		if entry.Type == "" {
			return nil, fmt.Errorf("provider registration requires a non-empty type")
		}
		if _, exists := byType[entry.Type]; exists {
			return nil, fmt.Errorf("duplicate provider registration: %q", entry.Type)
		}
		if entry.Construct == nil {
			return nil, fmt.Errorf("provider %q registration requires a constructor", entry.Type)
		}
		if len(entry.SupportedResourceTypes) == 0 {
			return nil, fmt.Errorf("provider %q registration requires at least one supported resource type", entry.Type)
		}
		if entry.RequiresHTTPS && !entry.RequiresHost {
			return nil, fmt.Errorf("provider %q registration requires host when HTTPS is required", entry.Type)
		}
		if entry.RejectsURLUserinfo && !entry.RequiresEgressValidation {
			return nil, fmt.Errorf("provider %q registration requires egress validation when URL userinfo is rejected", entry.Type)
		}
		if entry.VerificationMode == "" {
			return nil, fmt.Errorf("provider %q registration requires a verification mode", entry.Type)
		}
		order = append(order, entry.Type)
		byType[entry.Type] = entry
	}
	return &providerCatalogue{order: order, entries: byType}, nil
}

func (c *providerCatalogue) validProviders() []string {
	out := make([]string, len(c.order))
	copy(out, c.order)
	return out
}

func (c *providerCatalogue) lookup(providerType string) (providerRegistration, bool) {
	entry, ok := c.entries[providerType]
	return entry, ok
}

func (c *providerCatalogue) isValidProvider(providerType string) bool {
	_, ok := c.entries[providerType]
	return ok
}

func (c *providerCatalogue) providerSupportsResourceType(providerType, resourceType string) bool {
	entry, ok := c.entries[providerType]
	return ok && entry.SupportedResourceTypes[resourceType]
}

func (c *providerCatalogue) isVirtualProvider(providerType string) bool {
	entry, ok := c.entries[providerType]
	return ok && entry.IsVirtual
}

func (c *providerCatalogue) validateProviderURL(providerType, urlStr string) error {
	entry, ok := c.entries[providerType]
	if !ok || !entry.RequiresHost {
		return nil
	}
	parsed, err := url.Parse(urlStr)
	if err != nil || parsed.Hostname() == "" {
		return fmt.Errorf("provider %q requires a valid URL with a host", providerType)
	}
	if entry.RequiresHTTPS && parsed.Scheme != "https" {
		return fmt.Errorf("provider %q requires an HTTPS URL", providerType)
	}
	return nil
}

func (c *providerCatalogue) supportsAtomicRename(providerType string) bool {
	entry, ok := c.lookup(providerType)
	return ok && entry.SupportsAtomicRename
}

func (c *providerCatalogue) verificationMode(providerType string) VerificationMode {
	entry, ok := c.lookup(providerType)
	if !ok {
		return VerificationNone
	}
	return entry.VerificationMode
}

type catalogueTraits struct {
	catalogue    *providerCatalogue
	providerType string
}

type catalogueTraitsBinder interface {
	bindCatalogueTraits(*providerCatalogue, string)
}

func (t *catalogueTraits) bindCatalogueTraits(c *providerCatalogue, providerType string) {
	if t == nil {
		return
	}
	t.catalogue = c
	t.providerType = providerType
}

func (t *catalogueTraits) resolveCatalogueProvider(fallback string) (*providerCatalogue, string) {
	c := defaultProviderCatalogue
	if t != nil && t.catalogue != nil {
		c = t.catalogue
	}
	if t != nil && t.providerType != "" {
		return c, t.providerType
	}
	return c, fallback
}

func (t *catalogueTraits) supportsAtomicRename(fallback string) bool {
	c, providerType := t.resolveCatalogueProvider(fallback)
	return c.supportsAtomicRename(providerType)
}

func (t *catalogueTraits) verificationMode(fallback string) VerificationMode {
	c, providerType := t.resolveCatalogueProvider(fallback)
	return c.verificationMode(providerType)
}

func (c *providerCatalogue) newProvider(ctx context.Context, providerType, urlStr, username, password string) (StorageProvider, error) {
	entry, exists := c.lookup(providerType)
	if !exists {
		return nil, fmt.Errorf("unsupported provider type: %q", providerType)
	}

	// URL userinfo is accepted only for host-based providers that use the
	// explicit username/password fields. Extract and remove it before any
	// downstream parser can include the URL in an error. FTPS deliberately
	// rejects URL userinfo as part of its strict connection URL contract.
	if entry.RequiresEgressValidation {
		if parsed, err := url.Parse(urlStr); err == nil && parsed.User != nil {
			if entry.RejectsURLUserinfo {
				return nil, fmt.Errorf("provider %q URL must not contain userinfo", providerType)
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

		// SSRF guard: reject egress to loopback / link-local (and private ranges
		// when MIGRATION_BLOCK_PRIVATE is set) for providers that connect to a
		// user-supplied host.
		if err := validateEgressURLContext(ctx, urlStr); err != nil {
			return nil, err
		}
	}

	provider, err := entry.Construct(ctx, urlStr, username, password)
	if err != nil {
		return nil, err
	}
	if binder, ok := provider.(catalogueTraitsBinder); ok {
		if v := reflect.ValueOf(provider); v.IsValid() && (v.Kind() != reflect.Ptr || !v.IsNil()) {
			binder.bindCatalogueTraits(c, providerType)
		}
	}
	return provider, nil
}

func (c *providerCatalogue) managerCapabilitiesFor(providerType string) ManagerCapabilities {
	entry, ok := c.entries[providerType]
	if !ok {
		return ManagerCapabilities{}
	}
	if entry.WindowsManagerBlocked && runtime.GOOS == "windows" {
		return ManagerCapabilities{}
	}
	return entry.ManagerCapabilities
}

func (c *providerCatalogue) singleOpClassifier(entry providerRegistration) func(ManagerLocator, ManagerLocator, string) bool {
	if entry.ManagerSingleOp != nil {
		return entry.ManagerSingleOp
	}
	return func(ManagerLocator, ManagerLocator, string) bool { return true }
}

func selectManagerMutator[T any](c *providerCatalogue, providerType string, provider StorageProvider, build func(providerRegistration) T) (T, bool) {
	if dedicated, ok := provider.(T); ok {
		return dedicated, true
	}
	entry, ok := c.lookup(providerType)
	if !ok || !entry.ManagerPathMutation || (entry.WindowsManagerBlocked && runtime.GOOS == "windows") {
		var zero T
		return zero, false
	}
	return build(entry), true
}

func (c *providerCatalogue) newManagerRenamer(providerType string, provider StorageProvider) (ManagerRenamer, bool) {
	return selectManagerMutator(c, providerType, provider, func(entry providerRegistration) ManagerRenamer {
		return newPathManagerMutatorWithClassifier(provider, c.singleOpClassifier(entry))
	})
}

func (c *providerCatalogue) newManagerMover(providerType string, provider StorageProvider) (ManagerMover, bool) {
	return selectManagerMutator(c, providerType, provider, func(entry providerRegistration) ManagerMover {
		return newPathManagerMutatorWithClassifier(provider, c.singleOpClassifier(entry))
	})
}

func (c *providerCatalogue) newManagerCopier(providerType string, provider StorageProvider) (ManagerCopier, bool) {
	return selectManagerMutator(c, providerType, provider, func(providerRegistration) ManagerCopier {
		return newPathManagerMutator(provider, false)
	})
}

func (c *providerCatalogue) newFileManager(providerType string, provider StorageProvider) (FileManager, error) {
	entry, ok := c.lookup(providerType)
	if provider == nil || !ok {
		return nil, ErrManagerUnsupported
	}
	return &fileManagerAdapter{
		catalogue:            c,
		providerType:         providerType,
		provider:             provider,
		capabilities:         c.managerCapabilitiesFor(providerType),
		pathSafe:             entry.ManagerPathMutation,
		supportsAtomicRename: entry.SupportsAtomicRename,
		rootNativeID:         entry.ManagerRootNativeID,
		downloadByNativeID:   entry.ManagerDownloadByNativeID,
		cleanupTimeout:       managerCleanupTimeout,
	}, nil
}

// IsValidProvider reports whether p is a supported storage provider.
func IsValidProvider(p string) bool {
	return defaultProviderCatalogue.isValidProvider(p)
}

var providerCatalogueEntries = []providerRegistration{
	{
		Type:                     "nextcloud",
		RequiresHost:             true,
		RequiresHTTPS:            true,
		RequiresEgressValidation: true,
		SupportedResourceTypes:   map[string]bool{"files": true, "calendars": true, "contacts": true},
		SupportsAtomicRename:     true,
		VerificationMode:         VerificationSizeOnly,
		ManagerCapabilities:      ManagerCapabilities{Browse: true, NativePagination: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, NativeCopy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: true, ConflictRename: true, Thumbnails: true},
		ManagerPathMutation:      true,
		Construct: func(_ context.Context, urlStr, username, password string) (StorageProvider, error) {
			return NewNextcloudProvider(urlStr, username, password)
		},
	},
	{
		Type:                     "opencloud",
		RequiresHost:             true,
		RequiresHTTPS:            true,
		RequiresEgressValidation: true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     true,
		VerificationMode:         VerificationSizeOnly,
		ManagerCapabilities:      ManagerCapabilities{Browse: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, NativeCopy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: true, ConflictRename: true, Thumbnails: false},
		ManagerPathMutation:      true,
		Construct: func(_ context.Context, urlStr, username, password string) (StorageProvider, error) {
			return NewOpenCloudProvider(urlStr, username, password)
		},
	},
	{
		Type:                     "webdav",
		RequiresHost:             true,
		RequiresHTTPS:            true,
		RequiresEgressValidation: true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     true,
		VerificationMode:         VerificationSizeOnly,
		ManagerCapabilities:      ManagerCapabilities{Browse: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, NativeCopy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: true, ConflictRename: true},
		ManagerPathMutation:      true,
		Construct: func(_ context.Context, urlStr, username, password string) (StorageProvider, error) {
			return NewWebDAVProvider(urlStr, username, password)
		},
	},
	{
		Type:                      "dropbox",
		SupportedResourceTypes:    map[string]bool{"files": true},
		SupportsAtomicRename:      true,
		VerificationMode:          VerificationCryptographicHash,
		ManagerCapabilities:       ManagerCapabilities{Browse: true, NativePagination: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, NativeCopy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: true, ConflictRename: true, Thumbnails: true},
		ManagerPathMutation:       true,
		ManagerDownloadByNativeID: true,
		Construct: func(_ context.Context, _, _, password string) (StorageProvider, error) {
			return NewDropboxProvider(password)
		},
	},
	{
		Type:                   "google",
		SupportedResourceTypes: map[string]bool{"files": true, "calendars": true, "contacts": true},
		SupportsAtomicRename:   true,
		VerificationMode:       VerificationCryptographicHash,
		ManagerCapabilities:    ManagerCapabilities{Browse: true, NativePagination: true, Download: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, NativeCopy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: true, ConflictRename: true, Thumbnails: true},
		ManagerRootNativeID:    "root",
		Construct: func(ctx context.Context, _, _, password string) (StorageProvider, error) {
			// The OAuth token is passed in the password field for OAuth providers
			return NewGoogleProvider(ctx, password)
		},
	},
	{
		Type:                   "onedrive",
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   true,
		VerificationMode:       VerificationCryptographicHash,
		ManagerCapabilities:    ManagerCapabilities{Browse: true, NativePagination: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, NativeCopy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: true, ConflictRename: true, Thumbnails: true},
		ManagerPathMutation:    true,
		Construct: func(_ context.Context, _, _, password string) (StorageProvider, error) {
			return NewOneDriveProvider(password)
		},
	},
	{
		Type:                   "hidrive",
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   true,
		VerificationMode:       VerificationCryptographicHash,
		ManagerCapabilities:    ManagerCapabilities{Browse: true, NativePagination: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, NativeCopy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: true, ConflictRename: true, Thumbnails: true},
		ManagerPathMutation:    true,
		Construct: func(_ context.Context, _, _, password string) (StorageProvider, error) {
			return NewHiDriveProvider(password)
		},
	},
	{
		Type:                     "smb",
		RequiresHost:             true,
		RequiresEgressValidation: true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     true,
		VerificationMode:         VerificationSizeOnly,
		ManagerCapabilities:      ManagerCapabilities{Browse: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: true, ConflictRename: true},
		ManagerPathMutation:      true,
		Construct: func(_ context.Context, urlStr, username, password string) (StorageProvider, error) {
			return NewSMBProvider(urlStr, username, password)
		},
	},
	{
		Type:                   "s3",
		RequiresHost:           true,
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   false,
		VerificationMode:       VerificationSizeOnly,
		ManagerCapabilities:    ManagerCapabilities{Browse: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, NativeCopy: true, DeleteFile: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: false, ConflictRename: true},
		ManagerPathMutation:    true,
		ManagerSingleOp:        func(ManagerLocator, ManagerLocator, string) bool { return false },
		Construct: func(_ context.Context, urlStr, username, password string) (StorageProvider, error) {
			return NewS3Provider(urlStr, username, password)
		},
	},
	{
		Type:                     "sftp",
		RequiresHost:             true,
		RequiresEgressValidation: true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     true,
		VerificationMode:         VerificationSizeOnly,
		ManagerCapabilities:      ManagerCapabilities{Browse: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: true, ConflictRename: true},
		ManagerPathMutation:      true,
		Construct: func(_ context.Context, urlStr, username, password string) (StorageProvider, error) {
			return NewSFTPProvider(urlStr, username, password)
		},
	},
	{
		Type:                     "ftp",
		RequiresHost:             true,
		RequiresEgressValidation: true,
		RejectsURLUserinfo:       true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     true,
		VerificationMode:         VerificationSizeOnly,
		ManagerCapabilities:      ManagerCapabilities{Browse: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: true, ConflictRename: true},
		ManagerPathMutation:      true,
		Construct: func(_ context.Context, urlStr, username, password string) (StorageProvider, error) {
			return NewFTPProvider(urlStr, username, password)
		},
	},
	{
		Type:                   "magentacloud",
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   true,
		VerificationMode:       VerificationSizeOnly,
		ManagerCapabilities:    ManagerCapabilities{Browse: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, NativeCopy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: true, ConflictRename: true, Thumbnails: true},
		ManagerPathMutation:    true,
		Construct: func(_ context.Context, _, username, password string) (StorageProvider, error) {
			// MagentaCLOUD has a fixed public WebDAV endpoint, so urlStr is ignored.
			return NewMagentacloudProvider(username, password)
		},
	},
	{
		Type:                   "koofr",
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   false,
		VerificationMode:       VerificationCryptographicHash,
		ManagerCapabilities:    ManagerCapabilities{Browse: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, NativeCopy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: false, ConflictRename: true, Thumbnails: true},
		ManagerPathMutation:    true,
		Construct: func(_ context.Context, _, username, password string) (StorageProvider, error) {
			// Koofr has a fixed public endpoint, so urlStr is ignored.
			return NewKoofrProvider(username, password)
		},
	},
	{
		Type:                   "local",
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   true,
		VerificationMode:       VerificationCryptographicHash,
		ManagerCapabilities:    ManagerCapabilities{Browse: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: true, ConflictRename: true},
		WindowsManagerBlocked:  true,
		ManagerPathMutation:    true,
		Construct: func(ctx context.Context, _, _, _ string) (StorageProvider, error) {
			// Local reads/writes files inside LOCAL_STORAGE_ROOT. It takes no URL,
			// username, or password and performs no network egress (SSRF guard skipped).
			return NewLocalProvider(localUserID(ctx))
		},
	},
	{
		Type:                     "immich",
		RequiresHost:             true,
		RequiresHTTPS:            true,
		RequiresEgressValidation: true,
		IsVirtual:                true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     false,
		VerificationMode:         VerificationCryptographicHash,
		ManagerCapabilities:      ManagerCapabilities{Browse: true, NativePagination: true, Download: true, DeleteFile: true, Thumbnails: true},
		Construct: func(_ context.Context, urlStr, _, password string) (StorageProvider, error) {
			return NewImmichProvider(urlStr, password)
		},
	},
	{
		Type:                     "seafile",
		RequiresHost:             true,
		RequiresHTTPS:            true,
		RequiresEgressValidation: true,
		SupportedResourceTypes:   map[string]bool{"files": true},
		SupportsAtomicRename:     false,
		VerificationMode:         VerificationSizeOnly,
		ManagerCapabilities:      ManagerCapabilities{Browse: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, NativeCopy: true, DeleteFile: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: false, ConflictRename: true, Thumbnails: true},
		ManagerPathMutation:      true,
		ManagerSingleOp: func(locator, destination ManagerLocator, name string) bool {
			// A renamed cross-directory Seafile item requires move then rename.
			return path.Base(locator.Path) == name || path.Dir(locator.Path) == path.Clean(destination.Path)
		},
		Construct: func(_ context.Context, urlStr, username, password string) (StorageProvider, error) {
			return NewSeafileProvider(urlStr, username, password)
		},
	},
	{
		Type:                   "mega",
		SupportedResourceTypes: map[string]bool{"files": true},
		SupportsAtomicRename:   true,
		VerificationMode:       VerificationSizeOnly,
		ManagerCapabilities:    ManagerCapabilities{Browse: true, Download: true, Archive: true, Upload: true, Mkdir: true, Rename: true, Move: true, Copy: true, DeleteFile: true, DeleteEmptyDirectory: true, DeleteRecursiveDirectory: true, ConflictSkip: true, ConflictOverwrite: true, ConflictOverwriteAtomic: true, ConflictRename: true},
		ManagerPathMutation:    true,
		ManagerSingleOp: func(locator, destination ManagerLocator, _ string) bool {
			return path.Dir(locator.Path) == path.Clean(destination.Path)
		},
		Construct: func(ctx context.Context, _, username, password string) (StorageProvider, error) {
			// A missing scoped session is normal for a first MEGA connection; the
			// provider authenticates and establishes one when Connect is called.
			session, _ := MegaSessionFromContext(ctx)
			return NewMegaProvider(username, password, session), nil
		},
	},
}

// NewManagerRenamer selects a provider's dedicated manager implementation
// before falling back to the path adapter for explicitly path-safe providers.
func NewManagerRenamer(providerType string, provider StorageProvider) (ManagerRenamer, bool) {
	return defaultProviderCatalogue.newManagerRenamer(providerType, provider)
}

// NewManagerMover selects a provider's dedicated manager implementation
// before falling back to the path adapter for explicitly path-safe providers.
func NewManagerMover(providerType string, provider StorageProvider) (ManagerMover, bool) {
	return defaultProviderCatalogue.newManagerMover(providerType, provider)
}

// NewManagerCopier selects a provider's dedicated native manager implementation
// before falling back to the exact-size streaming adapter for path-safe
// providers that have no portable server-side copy operation.
func NewManagerCopier(providerType string, provider StorageProvider) (ManagerCopier, bool) {
	return defaultProviderCatalogue.newManagerCopier(providerType, provider)
}

// ManagerCapabilitiesFor returns static capabilities after applying runtime
// platform restrictions. It returns false for unknown providers.
func ManagerCapabilitiesFor(providerType string) ManagerCapabilities {
	return defaultProviderCatalogue.managerCapabilitiesFor(providerType)
}

// ProviderSupportsResourceType reports whether providerType supports the given resourceType.
func ProviderSupportsResourceType(providerType, resourceType string) bool {
	return defaultProviderCatalogue.providerSupportsResourceType(providerType, resourceType)
}

// IsVirtualProvider reports whether providerType uses virtual item references rather than filesystem paths.
func IsVirtualProvider(providerType string) bool {
	return defaultProviderCatalogue.isVirtualProvider(providerType)
}

// ValidateProviderURL verifies that a provider which needs a host actually has a
// URL with a non-empty host. It is called at profile create/update and at
// migration start so a host-based provider with a blank URL is rejected up front
// with a clean error code instead of failing cryptically deep inside indexing
// (where the raw Go error would otherwise leak to the client). The SSRF egress
// policy itself is still enforced later inside NewProvider.
func ValidateProviderURL(providerType, urlStr string) error {
	return defaultProviderCatalogue.validateProviderURL(providerType, urlStr)
}

func NewProvider(ctx context.Context, providerType, urlStr, username, password string) (StorageProvider, error) {
	return defaultProviderCatalogue.newProvider(ctx, providerType, urlStr, username, password)
}
