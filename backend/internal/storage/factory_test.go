package storage

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestIsValidProvider(t *testing.T) {
	for _, p := range ValidProviders {
		if !IsValidProvider(p) {
			t.Errorf("IsValidProvider(%q) = false, want true", p)
		}
	}
	validProviderSet := make(map[string]struct{}, len(ValidProviders))
	for _, providerType := range ValidProviders {
		validProviderSet[providerType] = struct{}{}
	}
	for providerType := range defaultProviderCatalogue.entries {
		if _, ok := validProviderSet[providerType]; !ok {
			t.Errorf("catalogue entry %q is missing from ValidProviders", providerType)
		}
	}
	invalid := []string{"", "NEXTCLOUD", "Dropbox", "s3 "}
	for _, p := range invalid {
		if IsValidProvider(p) {
			t.Errorf("IsValidProvider(%q) = true, want false", p)
		}
	}
}

func TestProviderSupportsResourceType(t *testing.T) {
	if !ProviderSupportsResourceType("nextcloud", "calendars") || !ProviderSupportsResourceType("nextcloud", "contacts") {
		t.Errorf("expected nextcloud to support calendars and contacts")
	}
	if !ProviderSupportsResourceType("google", "calendars") || !ProviderSupportsResourceType("google", "contacts") {
		t.Errorf("expected google to support calendars and contacts")
	}
	for _, p := range []string{"local", "webdav", "s3", "sftp", "ftp", "smb", "dropbox", "onedrive", "hidrive", "immich", "magentacloud", "koofr"} {
		if !ProviderSupportsResourceType(p, "files") {
			t.Errorf("expected %s to support files", p)
		}
		if ProviderSupportsResourceType(p, "calendars") || ProviderSupportsResourceType(p, "contacts") {
			t.Errorf("expected %s NOT to support calendars or contacts", p)
		}
	}
}

func TestNewProviderRejectsUnsupported(t *testing.T) {
	_, err := NewProvider(context.Background(), "unsupported-type", "https://example.com", "u", "p")
	if err == nil {
		t.Errorf("expected error for unsupported provider type")
	}
}

func TestNewProviderSanitizesCredentialsInURL(t *testing.T) {
	// Host-based providers pull credentials out of URL userinfo and strip them
	// before downstream parsing. FTPS is intentionally excluded because its URL
	// contract rejects userinfo.
	cases := []struct {
		typ string
		url string
	}{
		{"nextcloud", "https://user:pass@10.0.0.5/remote.php/dav"},
		{"opencloud", "https://user:pass@10.0.0.5/remote.php/dav"},
		{"webdav", "https://user:pass@192.168.1.10/dav"},
		{"smb", "smb://user:pass@10.0.0.5/share"},
		{"sftp", "sftp://user:pass@10.0.0.5/?host_key=SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"immich", "https://user:pass@10.0.0.5"},
		{"seafile", "https://user:pass@10.0.0.5"},
	}
	for _, c := range cases {
		p, err := NewProvider(context.Background(), c.typ, c.url, "", "")
		if err != nil {
			t.Errorf("%s: NewProvider failed: %v", c.typ, err)
			continue
		}
		if p == nil {
			t.Errorf("%s: expected non-nil provider", c.typ)
		}
	}
}

func TestNewProviderDoesNotExposeURLCredentialsInErrors(t *testing.T) {
	const secret = "super-secret-password"
	// The invalid percent escape forces url.Parse to fail while preserving the
	// userinfo-shaped input that must never be included in the returned error.
	malformedURL := "sftp://user:" + secret + "%@example.com"
	for _, providerType := range []string{"nextcloud", "sftp", "smb"} {
		_, err := NewProvider(context.Background(), providerType, malformedURL, "", "")
		if err == nil {
			t.Errorf("%s: expected malformed URL error", providerType)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error leaked URL credentials: %v", providerType, err)
		}
	}
	for _, constructor := range []func(string, string, string) error{
		func(rawURL, username, password string) error {
			_, err := NewSFTPProvider(rawURL, username, password)
			return err
		},
		func(rawURL, username, password string) error {
			_, err := NewSMBProvider(rawURL, username, password)
			return err
		},
	} {
		err := constructor(malformedURL, "", "")
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("direct constructor leaked URL credentials: %v", err)
		}
	}
	_, err := NewProvider(context.Background(), "ftp", "ftps://user:"+secret+"@10.0.0.5", "", "")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("FTPS userinfo error leaked URL credentials: %v", err)
	}
}

func TestNewProviderSSRFBlockedByDefault(t *testing.T) {
	// Loopback must always be blocked regardless of MIGRATION_BLOCK_PRIVATE.
	blockPrivateEgress.Store(false)
	defer blockPrivateEgress.Store(false)

	cases := []struct {
		typ string
		url string
	}{
		{"nextcloud", "https://127.0.0.1/remote.php/dav"},
		{"webdav", "https://localhost/dav"},
		{"smb", "smb://169.254.169.254/share"},
		{"sftp", "sftp://[::1]/"},
		{"ftp", "ftps://127.0.0.1/"},
	}
	for _, c := range cases {
		if _, err := NewProvider(context.Background(), c.typ, c.url, "u", "p"); err == nil {
			t.Errorf("%s with %q: expected SSRF block, got nil error", c.typ, c.url)
		}
	}
}

func TestPublicFactorySSRFGuardsEveryRegisteredEgressProvider(t *testing.T) {
	// Keep this table deliberately keyed by the registry: adding a host-based
	// provider without a public-factory SSRF test fails loudly.
	blockedURLs := map[string]string{
		"nextcloud": "https://127.0.0.1/remote.php/dav",
		"opencloud": "https://127.0.0.1",
		"webdav":    "https://127.0.0.1/dav",
		"smb":       "smb://127.0.0.1/share",
		"sftp":      "sftp://127.0.0.1/?host_key=SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"ftp":       "ftps://127.0.0.1",
		"immich":    "https://127.0.0.1",
		"seafile":   "https://127.0.0.1",
	}
	guarded := 0
	for providerType, meta := range defaultProviderCatalogue.entries {
		if !meta.RequiresEgressValidation {
			continue
		}
		guarded++
		rawURL, ok := blockedURLs[providerType]
		if !ok {
			t.Errorf("%s requires egress validation but has no public factory test", providerType)
			continue
		}
		if _, err := NewProvider(context.Background(), providerType, rawURL, "user", "secret"); err == nil {
			t.Errorf("NewProvider(%q) allowed a loopback endpoint", providerType)
		}
	}
	if guarded != len(blockedURLs) {
		t.Fatalf("egress-validated catalogue providers = %d, test inputs = %d", guarded, len(blockedURLs))
	}
}

func TestProviderRegistryCapabilitiesMatchRuntime(t *testing.T) {
	runtimeProviders := map[string]StorageProvider{
		"nextcloud":    &NextcloudProvider{davProvider: &davProvider{}},
		"opencloud":    &OpenCloudProvider{davProvider: &davProvider{}},
		"webdav":       &WebDAVProvider{},
		"dropbox":      &DropboxProvider{},
		"google":       &GoogleProvider{},
		"onedrive":     &OneDriveProvider{},
		"hidrive":      &HiDriveProvider{},
		"smb":          &SMBProvider{},
		"s3":           &S3Provider{},
		"sftp":         &SFTPProvider{},
		"ftp":          &FTPProvider{},
		"magentacloud": &MagentacloudProvider{davProvider: &davProvider{}},
		"koofr":        &KoofrProvider{},
		"local":        &LocalProvider{},
		"immich":       &ImmichProvider{},
		"seafile":      &SeafileProvider{},
		"mega":         &MegaProvider{},
	}
	for providerType, meta := range defaultProviderCatalogue.entries {
		provider, ok := runtimeProviders[providerType]
		if !ok {
			t.Errorf("%s is registered without a runtime capability assertion", providerType)
			continue
		}
		if got := provider.VerificationMode(); got != meta.VerificationMode {
			t.Errorf("%s VerificationMode() = %q, catalogue = %q", providerType, got, meta.VerificationMode)
		}
		if got := provider.SupportsAtomicRename(); got != meta.SupportsAtomicRename {
			t.Errorf("%s SupportsAtomicRename() = %t, catalogue = %t", providerType, got, meta.SupportsAtomicRename)
		}
	}
	if len(runtimeProviders) != len(defaultProviderCatalogue.entries) {
		t.Fatalf("runtime capability assertions = %d, catalogue providers = %d", len(runtimeProviders), len(defaultProviderCatalogue.entries))
	}
}

func TestManagerCapabilityRegistryMatchesProviders(t *testing.T) {
	for _, providerType := range ValidProviders {
		entry, ok := defaultProviderCatalogue.lookup(providerType)
		if !ok {
			t.Errorf("%s is missing from provider catalogue", providerType)
			continue
		}
		if entry.ManagerCapabilities == (ManagerCapabilities{}) {
			t.Errorf("%s is missing manager capabilities", providerType)
		}
	}
}

func TestManagerReadCapabilitiesCoverEveryFilesProvider(t *testing.T) {
	for _, providerType := range ValidProviders {
		if providerType == "local" && runtime.GOOS == "windows" {
			continue
		}
		capabilities := ManagerCapabilitiesFor(providerType)
		if !capabilities.Browse || !capabilities.Download {
			t.Errorf("%s read capabilities = %#v, want browse and download", providerType, capabilities)
		}
	}
}

func TestFilesystemManagerDirectoryDeleteCapabilities(t *testing.T) {
	for _, providerType := range []string{"smb", "sftp", "ftp", "local"} {
		if providerType == "local" && runtime.GOOS == "windows" {
			continue
		}
		capabilities := ManagerCapabilitiesFor(providerType)
		if !capabilities.DeleteEmptyDirectory || !capabilities.DeleteRecursiveDirectory {
			t.Fatalf("%s directory delete capabilities = %#v, want both enabled", providerType, capabilities)
		}
	}
	if capabilities := ManagerCapabilitiesFor("s3"); capabilities.DeleteEmptyDirectory || capabilities.DeleteRecursiveDirectory {
		t.Fatalf("s3 directory delete capabilities = %#v, want both disabled", capabilities)
	}
}

func TestGoogleManagerUploadCapabilities(t *testing.T) {
	capabilities := ManagerCapabilitiesFor("google")
	if !capabilities.Upload || !capabilities.Rename || !capabilities.Move || !capabilities.ConflictSkip || !capabilities.ConflictOverwrite || !capabilities.ConflictOverwriteAtomic || !capabilities.ConflictRename {
		t.Fatalf("Google manager capabilities = %#v, want tested upload and mutation support", capabilities)
	}
}

func TestNextcloudManagerCapabilities(t *testing.T) {
	capabilities := ManagerCapabilitiesFor("nextcloud")
	if !capabilities.Browse || !capabilities.NativePagination || !capabilities.Download || !capabilities.Upload || !capabilities.Mkdir || !capabilities.Thumbnails {
		t.Fatalf("Nextcloud manager capabilities = %#v, want full browse, pagination, mutations, and thumbnails", capabilities)
	}
}

func TestDropboxManagerCapabilities(t *testing.T) {
	capabilities := ManagerCapabilitiesFor("dropbox")
	if !capabilities.Browse || !capabilities.NativePagination || !capabilities.Download || !capabilities.Upload || !capabilities.Mkdir || !capabilities.Thumbnails {
		t.Fatalf("Dropbox manager capabilities = %#v, want full browse, pagination, mutations, and thumbnails", capabilities)
	}
}

func TestHiDriveManagerCapabilities(t *testing.T) {
	capabilities := ManagerCapabilitiesFor("hidrive")
	if !capabilities.Browse || !capabilities.NativePagination || !capabilities.Download || !capabilities.Upload || !capabilities.Mkdir || !capabilities.Thumbnails {
		t.Fatalf("HiDrive manager capabilities = %#v, want full browse, pagination, mutations, and thumbnails", capabilities)
	}
}

func TestMagentacloudManagerCapabilities(t *testing.T) {
	capabilities := ManagerCapabilitiesFor("magentacloud")
	if !capabilities.Browse || !capabilities.Download || !capabilities.Upload || !capabilities.Mkdir || !capabilities.ConflictSkip || !capabilities.ConflictOverwrite || !capabilities.ConflictOverwriteAtomic || !capabilities.ConflictRename || !capabilities.Thumbnails {
		t.Fatalf("MagentaCLOUD manager capabilities = %#v, want browse, download, mutations, and thumbnails", capabilities)
	}
}

func TestOpenCloudManagerCapabilities(t *testing.T) {
	capabilities := ManagerCapabilitiesFor("opencloud")
	if !capabilities.Browse || !capabilities.Download || !capabilities.Upload || !capabilities.Mkdir || !capabilities.ConflictSkip || !capabilities.ConflictOverwrite || !capabilities.ConflictOverwriteAtomic || !capabilities.ConflictRename {
		t.Fatalf("OpenCloud manager capabilities = %#v, want browse, download, and mutations", capabilities)
	}
}

func TestKoofrManagerCapabilities(t *testing.T) {
	capabilities := ManagerCapabilitiesFor("koofr")
	if !capabilities.Browse || !capabilities.Download || !capabilities.Upload || !capabilities.Mkdir || !capabilities.ConflictSkip || !capabilities.ConflictOverwrite || !capabilities.ConflictRename || !capabilities.Thumbnails {
		t.Fatalf("Koofr manager capabilities = %#v, want browse, download, upload, mkdir, conflict options, and thumbnails", capabilities)
	}
}

func TestSeafileManagerCapabilities(t *testing.T) {
	capabilities := ManagerCapabilitiesFor("seafile")
	if !capabilities.Browse || !capabilities.Download || !capabilities.Upload || !capabilities.Mkdir || !capabilities.Thumbnails {
		t.Fatalf("Seafile manager capabilities = %#v, want browse, download, upload, mkdir, and thumbnails", capabilities)
	}
}

func TestImmichManagerCapabilities(t *testing.T) {
	capabilities := ManagerCapabilitiesFor("immich")
	if !capabilities.Browse || !capabilities.NativePagination || !capabilities.Download || !capabilities.Thumbnails {
		t.Fatalf("Immich manager capabilities = %#v, want browse, pagination, download, and thumbnails", capabilities)
	}
}

func TestManagerCapabilityInterfaceSatisfaction(t *testing.T) {
	t.Setenv("LOCAL_STORAGE_ROOT", t.TempDir())
	ctx := WithLocalUserScope(context.Background(), "test-user")

	dedicatedProviders := []struct {
		name     string
		url      string
		username string
		password string
	}{
		{"nextcloud", "https://example.com", "u", "p"},
		{"opencloud", "https://example.com", "u", "p"},
		{"dropbox", "", "u", "token"},
		{"google", "", "u", "token"},
		{"onedrive", "oauth://onedrive", "u", "token"},
		{"hidrive", "", "u", "token"},
		{"magentacloud", "", "u", "p"},
		{"koofr", "", "u", "p"},
		{"immich", "https://example.com", "", "apikey"},
		{"seafile", "https://example.com", "u", "p"},
	}

	for _, tc := range dedicatedProviders {
		p, err := NewProvider(ctx, tc.name, tc.url, tc.username, tc.password)
		if err != nil {
			t.Fatalf("NewProvider(%q) error = %v", tc.name, err)
		}
		caps := ManagerCapabilitiesFor(tc.name)

		if caps.Browse {
			if _, ok := p.(ManagerLister); !ok {
				t.Errorf("Dedicated provider %q has Browse: true but does not implement ManagerLister", tc.name)
			}
			if _, ok := p.(ManagerPathResolver); !ok {
				t.Errorf("Dedicated provider %q has Browse: true but does not implement ManagerPathResolver", tc.name)
			}
		}
		if caps.Download {
			if _, ok := p.(ManagerDownloader); !ok {
				t.Errorf("Dedicated provider %q has Download: true but does not implement ManagerDownloader", tc.name)
			}
		}
		if caps.Upload {
			if _, ok := p.(ManagerUploader); !ok {
				t.Errorf("Dedicated provider %q has Upload: true but does not implement ManagerUploader", tc.name)
			}
		}
		if caps.Mkdir {
			if _, ok := p.(ManagerDirectoryCreator); !ok {
				t.Errorf("Dedicated provider %q has Mkdir: true but does not implement ManagerDirectoryCreator", tc.name)
			}
		}
		if caps.Thumbnails {
			if _, ok := p.(ManagerThumbnailer); !ok {
				t.Errorf("Dedicated provider %q has Thumbnails: true but does not implement ManagerThumbnailer", tc.name)
			}
		}
		if caps.Rename {
			if _, ok := NewManagerRenamer(tc.name, p); !ok {
				t.Errorf("Dedicated provider %q has Rename: true but no manager renamer", tc.name)
			}
		}
		if caps.Move {
			if _, ok := NewManagerMover(tc.name, p); !ok {
				t.Errorf("Dedicated provider %q has Move: true but no manager mover", tc.name)
			}
		}
		if caps.Copy {
			if _, ok := NewManagerCopier(tc.name, p); !ok {
				t.Errorf("Dedicated provider %q has Copy: true but no manager copier", tc.name)
			}
		}
	}
}

func TestManagerMutationCapabilityRouting(t *testing.T) {
	for _, providerType := range ValidProviders {
		if providerType == "local" && runtime.GOOS == "windows" {
			continue
		}
		capabilities := ManagerCapabilitiesFor(providerType)
		if capabilities.Rename || capabilities.Move || capabilities.Copy {
			entry, ok := defaultProviderCatalogue.lookup(providerType)
			if !ok || (providerType != "google" && !entry.ManagerPathMutation) {
				t.Errorf("%s advertises manager mutations without a path or dedicated route", providerType)
			}
		}
	}

	mockProvider := &mockStorageProvider{
		inspectFn: func(_ context.Context, _ string, p string) (CloudResource, error) {
			switch p {
			case "/from", "/to", "/folder", "/Library/from", "/Library/to", "/Library/folder":
				return CloudResource{Path: p, Name: "dir", IsDir: true}, nil
			default:
				return CloudResource{Path: p, Name: "report.txt", Size: 10}, nil
			}
		},
	}

	s3Mover, ok := NewManagerMover("s3", mockProvider)
	if !ok {
		t.Fatal("NewManagerMover(s3) = false, want true")
	}
	if res, err := s3Mover.MoveManagerItem(context.Background(), ManagerLocator{Path: "/from/report.txt"}, ManagerLocator{Path: "/to"}, "report.txt", ManagerMutationOptions{}); err != nil || res.Native {
		t.Fatalf("S3 move = (%+v, %v), want Native=false without error", res, err)
	}

	megaMover, ok := NewManagerMover("mega", mockProvider)
	if !ok {
		t.Fatal("NewManagerMover(mega) = false, want true")
	}
	if res, err := megaMover.MoveManagerItem(context.Background(), ManagerLocator{Path: "/from/report.txt"}, ManagerLocator{Path: "/to"}, "report.txt", ManagerMutationOptions{}); err != nil || res.Native {
		t.Fatalf("MEGA cross-directory move = (%+v, %v), want Native=false without error", res, err)
	}
	megaRenamer, ok := NewManagerRenamer("mega", mockProvider)
	if !ok {
		t.Fatal("NewManagerRenamer(mega) = false, want true")
	}
	if res, err := megaRenamer.RenameManagerItem(context.Background(), ManagerLocator{Path: "/folder/report.txt"}, ManagerLocator{Path: "/folder"}, "renamed.txt", ManagerMutationOptions{}); err != nil || !res.Native {
		t.Fatalf("MEGA same-directory rename = (%+v, %v), want Native=true without error", res, err)
	}

	seafileMover, ok := NewManagerMover("seafile", mockProvider)
	if !ok {
		t.Fatal("NewManagerMover(seafile) = false, want true")
	}
	if res, err := seafileMover.MoveManagerItem(context.Background(), ManagerLocator{Path: "/Library/from/report.txt"}, ManagerLocator{Path: "/Library/to"}, "renamed.txt", ManagerMutationOptions{}); err != nil || res.Native {
		t.Fatalf("Seafile move+rename = (%+v, %v), want Native=false without error", res, err)
	}
	seafileRenamer, ok := NewManagerRenamer("seafile", mockProvider)
	if !ok {
		t.Fatal("NewManagerRenamer(seafile) = false, want true")
	}
	if res, err := seafileRenamer.RenameManagerItem(context.Background(), ManagerLocator{Path: "/Library/folder/report.txt"}, ManagerLocator{Path: "/Library/folder"}, "renamed.txt", ManagerMutationOptions{}); err != nil || !res.Native {
		t.Fatalf("Seafile same-directory rename = (%+v, %v), want Native=true without error", res, err)
	}
}

func TestPublicFactoryConstructsEveryProvider(t *testing.T) {
	t.Setenv("LOCAL_STORAGE_ROOT", t.TempDir())
	ctx := WithLocalUserScope(context.Background(), "factory-test-user")
	cases := map[string]struct {
		url, username, password string
		expected                StorageProvider
	}{
		"nextcloud":    {"https://1.1.1.1", "user", "secret", &NextcloudProvider{}},
		"opencloud":    {"https://1.1.1.1", "user", "secret", &OpenCloudProvider{}},
		"webdav":       {"https://1.1.1.1/dav", "user", "secret", &WebDAVProvider{}},
		"dropbox":      {"", "", "token", &DropboxProvider{}},
		"google":       {"", "", "token", &GoogleProvider{}},
		"onedrive":     {"", "", "token", &OneDriveProvider{}},
		"hidrive":      {"", "", "token", &HiDriveProvider{}},
		"smb":          {"smb://1.1.1.1/share", "user", "secret", &SMBProvider{}},
		"s3":           {"s3://bucket", "key", "secret", &S3Provider{}},
		"sftp":         {"sftp://1.1.1.1/?host_key=SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "user", "secret", &SFTPProvider{}},
		"ftp":          {"ftps://1.1.1.1", "user", "secret", &FTPProvider{}},
		"magentacloud": {"", "user", "secret", &MagentacloudProvider{}},
		"koofr":        {"", "user", "secret", &KoofrProvider{}},
		"local":        {"", "", "", &LocalProvider{}},
		"immich":       {"https://1.1.1.1", "", "key", &ImmichProvider{}},
		"seafile":      {"https://1.1.1.1", "user", "secret", &SeafileProvider{}},
		"mega":         {"", "user@example.com", "secret", &MegaProvider{}},
	}
	for providerType, tt := range cases {
		t.Run(providerType, func(t *testing.T) {
			provider, err := NewProvider(ctx, providerType, tt.url, tt.username, tt.password)
			if err != nil {
				if providerType == "local" && runtime.GOOS == "windows" {
					t.Skip("local provider mutations are intentionally unavailable on Windows")
				}
				t.Fatal(err)
			}
			defer provider.Close()
			if reflect.TypeOf(provider) != reflect.TypeOf(tt.expected) {
				t.Fatalf("NewProvider() type = %T, want %T", provider, tt.expected)
			}
		})
	}
	if len(cases) != len(defaultProviderCatalogue.entries) {
		t.Fatalf("factory assertions = %d, catalogue providers = %d", len(cases), len(defaultProviderCatalogue.entries))
	}
}

func TestNewProviderOAuthProviders(t *testing.T) {
	// dropbox/google take the token in the password field; no egress validation.
	if p, err := NewProvider(context.Background(), "dropbox", "", "u", "oauth-token"); err != nil || p == nil {
		t.Errorf("dropbox: got p=%v err=%v", p, err)
	}
	if p, err := NewProvider(context.Background(), "google", "", "u", "oauth-token"); err != nil || p == nil {
		t.Errorf("google: got p=%v err=%v", p, err)
	}
	if p, err := NewProvider(context.Background(), "onedrive", "oauth://onedrive", "u", "oauth-token"); err != nil || p == nil {
		t.Errorf("onedrive: got p=%v err=%v", p, err)
	}
	if _, err := NewProvider(context.Background(), "onedrive", "", "", ""); err == nil {
		t.Error("onedrive without token: expected error")
	}
	if p, err := NewProvider(context.Background(), "hidrive", "", "u", "oauth-token"); err != nil || p == nil {
		t.Errorf("hidrive: got p=%v err=%v", p, err)
	}
	// Fixed-endpoint providers ignore url.
	if p, err := NewProvider(context.Background(), "magentacloud", "", "u", "p"); err != nil || p == nil {
		t.Errorf("magentacloud: got p=%v err=%v", p, err)
	}
	if p, err := NewProvider(context.Background(), "koofr", "", "u", "p"); err != nil || p == nil {
		t.Errorf("koofr: got p=%v err=%v", p, err)
	}
}

func TestNewProviderMega(t *testing.T) {
	session := MegaSession{ID: "session-id", MasterKey: []byte{1, 2, 3}}
	ctx := WithMegaSession(context.Background(), session)

	provider, err := NewProvider(ctx, "mega", "", "user@example.com", "password")
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}

	megaProvider, ok := provider.(*MegaProvider)
	if !ok {
		t.Fatalf("NewProvider() type = %T, want *MegaProvider", provider)
	}
	if megaProvider.email != "user@example.com" || megaProvider.password != "password" {
		t.Errorf("provider credentials = %q, %q, want supplied credentials", megaProvider.email, megaProvider.password)
	}

	gotSession := megaProvider.Session()
	if gotSession.ID != session.ID || string(gotSession.MasterKey) != string(session.MasterKey) {
		t.Errorf("provider session = %+v, want %+v", gotSession, session)
	}
	gotSession.MasterKey[0] = 9
	if megaProvider.Session().MasterKey[0] != 1 {
		t.Error("Session() returned a mutable reference to the provider session")
	}
}

func TestProviderCatalogueValidationSeam(t *testing.T) {
	stubConstruct := func(context.Context, string, string, string) (StorageProvider, error) {
		return &WebDAVProvider{}, nil
	}

	t.Run("rejects invalid catalogue registrations", func(t *testing.T) {
		cases := []struct {
			name    string
			entries []providerRegistration
		}{
			{
				name: "empty provider type",
				entries: []providerRegistration{{
					Type:                   "",
					SupportedResourceTypes: map[string]bool{"files": true},
					VerificationMode:       VerificationSizeOnly,
					Construct:              stubConstruct,
				}},
			},
			{
				name: "duplicate provider type",
				entries: []providerRegistration{
					{Type: "custom", SupportedResourceTypes: map[string]bool{"files": true}, VerificationMode: VerificationSizeOnly, Construct: stubConstruct},
					{Type: "custom", SupportedResourceTypes: map[string]bool{"files": true}, VerificationMode: VerificationSizeOnly, Construct: stubConstruct},
				},
			},
			{
				name: "missing constructor",
				entries: []providerRegistration{{
					Type:                   "custom",
					SupportedResourceTypes: map[string]bool{"files": true},
					VerificationMode:       VerificationSizeOnly,
				}},
			},
			{
				name: "empty resource types",
				entries: []providerRegistration{{
					Type:             "custom",
					VerificationMode: VerificationSizeOnly,
					Construct:        stubConstruct,
				}},
			},
			{
				name: "requires https without host",
				entries: []providerRegistration{{
					Type:                   "custom",
					RequiresHTTPS:          true,
					SupportedResourceTypes: map[string]bool{"files": true},
					VerificationMode:       VerificationSizeOnly,
					Construct:              stubConstruct,
				}},
			},
			{
				name: "rejects url userinfo without egress validation",
				entries: []providerRegistration{{
					Type:                   "custom",
					RejectsURLUserinfo:     true,
					SupportedResourceTypes: map[string]bool{"files": true},
					VerificationMode:       VerificationSizeOnly,
					Construct:              stubConstruct,
				}},
			},
			{
				name: "missing verification mode",
				entries: []providerRegistration{{
					Type:                   "custom",
					SupportedResourceTypes: map[string]bool{"files": true},
					Construct:              stubConstruct,
				}},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := newProviderCatalogue(tc.entries); err == nil {
					t.Fatalf("newProviderCatalogue(%s) expected error, got nil", tc.name)
				}
			})
		}
	})

	t.Run("single registration supplies validation rules and ordered list", func(t *testing.T) {
		cat, err := newProviderCatalogue([]providerRegistration{
			{
				Type:                     "alpha",
				RequiresHost:             true,
				RequiresHTTPS:            true,
				RequiresEgressValidation: true,
				SupportedResourceTypes:   map[string]bool{"files": true, "calendars": true},
				VerificationMode:         VerificationSizeOnly,
				Construct:                stubConstruct,
			},
			{
				Type:                   "beta",
				IsVirtual:              true,
				SupportedResourceTypes: map[string]bool{"files": true},
				VerificationMode:       VerificationCryptographicHash,
				Construct:              stubConstruct,
			},
		})
		if err != nil {
			t.Fatalf("newProviderCatalogue() error = %v", err)
		}

		wantOrder := []string{"alpha", "beta"}
		if !reflect.DeepEqual(cat.validProviders(), wantOrder) {
			t.Fatalf("validProviders() = %v, want %v", cat.validProviders(), wantOrder)
		}
		if !cat.isValidProvider("alpha") || !cat.isValidProvider("beta") || cat.isValidProvider("gamma") {
			t.Fatalf("isValidProvider returned unexpected results")
		}
		if !cat.providerSupportsResourceType("alpha", "calendars") || cat.providerSupportsResourceType("beta", "calendars") {
			t.Fatalf("providerSupportsResourceType returned unexpected results")
		}
		if cat.isVirtualProvider("alpha") || !cat.isVirtualProvider("beta") {
			t.Fatalf("isVirtualProvider returned unexpected results")
		}
		if err := cat.validateProviderURL("alpha", "http://example.test"); err == nil {
			t.Fatalf("validateProviderURL(alpha, http) expected error")
		}
		if err := cat.validateProviderURL("alpha", "https://example.test"); err != nil {
			t.Fatalf("validateProviderURL(alpha, https) error = %v", err)
		}
		if err := cat.validateProviderURL("beta", ""); err != nil {
			t.Fatalf("validateProviderURL(beta, empty) error = %v", err)
		}
	})

	t.Run("production ValidProviders are unique and support files", func(t *testing.T) {
		seen := make(map[string]bool, len(ValidProviders))
		for _, p := range ValidProviders {
			if seen[p] {
				t.Fatalf("duplicate provider %q in ValidProviders", p)
			}
			seen[p] = true
			if !IsValidProvider(p) {
				t.Errorf("IsValidProvider(%q) = false, want true", p)
			}
			if !ProviderSupportsResourceType(p, "files") {
				t.Errorf("ProviderSupportsResourceType(%q, files) = false, want true", p)
			}
			if got, want := IsVirtualProvider(p), p == "immich"; got != want {
				t.Errorf("IsVirtualProvider(%q) = %t, want %t", p, got, want)
			}
			entry, ok := defaultProviderCatalogue.lookup(p)
			if !ok {
				t.Fatalf("missing catalogue entry for %q", p)
			}
			if entry.RequiresHost {
				if err := ValidateProviderURL(p, ""); err == nil {
					t.Errorf("ValidateProviderURL(%q, empty) expected error", p)
				}
				if entry.RequiresHTTPS {
					if err := ValidateProviderURL(p, "http://example.test"); err == nil {
						t.Errorf("ValidateProviderURL(%q, http) expected error", p)
					}
					if err := ValidateProviderURL(p, "https://example.test"); err != nil {
						t.Errorf("ValidateProviderURL(%q, https) unexpected error: %v", p, err)
					}
				}
			} else if err := ValidateProviderURL(p, ""); err != nil {
				t.Errorf("ValidateProviderURL(%q, empty) unexpected error: %v", p, err)
			}
		}
	})
}

func TestProviderCatalogueConstructionSeam(t *testing.T) {
	t.Run("single registration drives userinfo handling, SSRF guard, and construction", func(t *testing.T) {
		var gotURL, gotUser, gotPass string
		cat, err := newProviderCatalogue([]providerRegistration{
			{
				Type:                     "egress-extract",
				RequiresHost:             true,
				RequiresHTTPS:            true,
				RequiresEgressValidation: true,
				SupportedResourceTypes:   map[string]bool{"files": true},
				VerificationMode:         VerificationSizeOnly,
				Construct: func(_ context.Context, urlStr, username, password string) (StorageProvider, error) {
					gotURL, gotUser, gotPass = urlStr, username, password
					return &WebDAVProvider{}, nil
				},
			},
			{
				Type:                     "egress-no-userinfo",
				RequiresHost:             true,
				RequiresEgressValidation: true,
				RejectsURLUserinfo:       true,
				SupportedResourceTypes:   map[string]bool{"files": true},
				VerificationMode:         VerificationSizeOnly,
				Construct: func(_ context.Context, urlStr, username, password string) (StorageProvider, error) {
					return &FTPProvider{}, nil
				},
			},
		})
		if err != nil {
			t.Fatalf("newProviderCatalogue() error = %v", err)
		}

		if _, err := cat.newProvider(context.Background(), "unknown", "https://example.test", "", ""); err == nil {
			t.Fatal("expected error for unregistered provider")
		}
		if _, err := cat.newProvider(context.Background(), "egress-extract", "https://127.0.0.1/dav", "u", "p"); err == nil {
			t.Fatal("expected SSRF block for loopback endpoint")
		}
		if _, err := cat.newProvider(context.Background(), "egress-no-userinfo", "ftps://u:secret@10.0.0.5", "", ""); err == nil {
			t.Fatal("expected userinfo rejection when RejectsURLUserinfo is true")
		}
		p, err := cat.newProvider(context.Background(), "egress-extract", "https://alice:s3cr3t@10.0.0.5/dav", "", "")
		if err != nil {
			t.Fatalf("newProvider(egress-extract) error = %v", err)
		}
		if p == nil {
			t.Fatal("expected constructed provider")
		}
		if gotURL != "https://10.0.0.5/dav" || gotUser != "alice" || gotPass != "s3cr3t" {
			t.Fatalf("Construct got (%q, %q, %q), want stripped URL and extracted credentials", gotURL, gotUser, gotPass)
		}
	})
}

func TestProviderCatalogueManagerSeam(t *testing.T) {
	t.Run("single registration supplies capabilities, file manager, and mutation routing", func(t *testing.T) {
		cat, err := newProviderCatalogue([]providerRegistration{
			{
				Type:                   "path-custom",
				SupportedResourceTypes: map[string]bool{"files": true},
				VerificationMode:       VerificationSizeOnly,
				ManagerCapabilities:    ManagerCapabilities{Browse: true, Download: true, Rename: true, Move: true, Copy: true},
				ManagerPathMutation:    true,
				ManagerSingleOp: func(locator, destination ManagerLocator, _ string) bool {
					return locator.Path == "/same/file.txt" && destination.Path == "/same"
				},
				Construct: func(context.Context, string, string, string) (StorageProvider, error) {
					return &mockStorageProvider{}, nil
				},
			},
			{
				Type:                      "virtual-custom",
				IsVirtual:                 true,
				SupportedResourceTypes:    map[string]bool{"files": true},
				VerificationMode:          VerificationSizeOnly,
				ManagerCapabilities:       ManagerCapabilities{Browse: true, Download: true},
				ManagerPathMutation:       false,
				ManagerRootNativeID:       "custom-root",
				ManagerDownloadByNativeID: true,
				Construct: func(context.Context, string, string, string) (StorageProvider, error) {
					return &mockStorageProvider{}, nil
				},
			},
			{
				Type:                   "windows-blocked",
				SupportedResourceTypes: map[string]bool{"files": true},
				VerificationMode:       VerificationSizeOnly,
				ManagerCapabilities:    ManagerCapabilities{Browse: true, Rename: true, Move: true, Copy: true},
				WindowsManagerBlocked:  true,
				ManagerPathMutation:    true,
				Construct: func(context.Context, string, string, string) (StorageProvider, error) {
					return &mockStorageProvider{}, nil
				},
			},
		})
		if err != nil {
			t.Fatalf("newProviderCatalogue() error = %v", err)
		}

		if got := cat.managerCapabilitiesFor("unknown"); got != (ManagerCapabilities{}) {
			t.Fatalf("managerCapabilitiesFor(unknown) = %+v, want zero", got)
		}
		if got := cat.managerCapabilitiesFor("path-custom"); !got.Rename || !got.Copy {
			t.Fatalf("managerCapabilitiesFor(path-custom) = %+v, want Rename and Copy", got)
		}

		provider := &mockStorageProvider{
			inspectFn: func(_ context.Context, _ string, p string) (CloudResource, error) {
				if p == "/same" || p == "/other" {
					return CloudResource{Path: p, Name: "dir", IsDir: true}, nil
				}
				return CloudResource{Path: p, Name: "file.txt", Size: 10}, nil
			},
		}

		fm, err := cat.newFileManager("path-custom", provider)
		if err != nil {
			t.Fatalf("newFileManager(path-custom) error = %v", err)
		}
		renameRes, err := fm.RenameManagerItem(context.Background(), ManagerLocator{Path: "/same/file.txt"}, ManagerLocator{Path: "/same"}, "renamed.txt", ManagerMutationOptions{})
		if err != nil {
			t.Fatalf("RenameManagerItem error = %v", err)
		}
		if !renameRes.Native {
			t.Fatal("expected single-op classifier to mark same-dir rename as Native=true")
		}
		moveRes, err := fm.MoveManagerItem(context.Background(), ManagerLocator{Path: "/same/file.txt"}, ManagerLocator{Path: "/other"}, "file.txt", ManagerMutationOptions{})
		if err != nil {
			t.Fatalf("MoveManagerItem error = %v", err)
		}
		if moveRes.Native {
			t.Fatal("expected single-op classifier to mark cross-dir move as Native=false")
		}

		virtualFM, err := cat.newFileManager("virtual-custom", provider)
		if err != nil {
			t.Fatalf("newFileManager(virtual-custom) error = %v", err)
		}
		if !virtualFM.IsRootLocator(ManagerLocator{NativeID: "custom-root"}) {
			t.Fatal("expected ManagerRootNativeID to drive IsRootLocator")
		}
		if _, ok := cat.newManagerRenamer("virtual-custom", provider); ok {
			t.Fatal("virtual-custom without ManagerPathMutation should not fall back to path renamer")
		}
		if _, ok := cat.newManagerMover("virtual-custom", provider); ok {
			t.Fatal("virtual-custom without ManagerPathMutation should not fall back to path mover")
		}
		if _, ok := cat.newManagerCopier("virtual-custom", provider); ok {
			t.Fatal("virtual-custom without ManagerPathMutation should not fall back to path copier")
		}

		_, wantBlocked := cat.newManagerRenamer("windows-blocked", provider)
		if wantBlocked == (runtime.GOOS == "windows") {
			t.Fatalf("newManagerRenamer(windows-blocked) = %t on %s, want %t", wantBlocked, runtime.GOOS, runtime.GOOS != "windows")
		}
	})
}
