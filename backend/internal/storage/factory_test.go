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
	for providerType := range providerRegistry {
		if _, ok := validProviderSet[providerType]; !ok {
			t.Errorf("providerRegistry entry %q is missing from ValidProviders", providerType)
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
	for providerType, meta := range providerRegistry {
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
		t.Fatalf("egress-validated registry providers = %d, test inputs = %d", guarded, len(blockedURLs))
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
	for providerType, meta := range providerRegistry {
		provider, ok := runtimeProviders[providerType]
		if !ok {
			t.Errorf("%s is registered without a runtime capability assertion", providerType)
			continue
		}
		if got := provider.VerificationMode(); got != meta.VerificationMode {
			t.Errorf("%s VerificationMode() = %q, registry = %q", providerType, got, meta.VerificationMode)
		}
		if got := provider.SupportsAtomicRename(); got != meta.SupportsAtomicRename {
			t.Errorf("%s SupportsAtomicRename() = %t, registry = %t", providerType, got, meta.SupportsAtomicRename)
		}
	}
	if len(runtimeProviders) != len(providerRegistry) {
		t.Fatalf("runtime capability assertions = %d, registry providers = %d", len(runtimeProviders), len(providerRegistry))
	}
}

func TestManagerCapabilityRegistryMatchesProviders(t *testing.T) {
	for _, providerType := range ValidProviders {
		if _, ok := managerCapabilityRegistry[providerType]; !ok {
			t.Errorf("%s is missing manager capabilities", providerType)
		}
	}
	for providerType := range managerCapabilityRegistry {
		if !IsValidProvider(providerType) {
			t.Errorf("manager capabilities registered for unsupported provider %s", providerType)
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

func TestGoogleManagerUploadCapabilities(t *testing.T) {
	capabilities := ManagerCapabilitiesFor("google")
	if !capabilities.Upload || !capabilities.ConflictSkip || !capabilities.ConflictOverwrite || !capabilities.ConflictOverwriteAtomic || !capabilities.ConflictRename {
		t.Fatalf("Google manager capabilities = %#v, want tested upload support", capabilities)
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
	if len(cases) != len(providerRegistry) {
		t.Fatalf("factory assertions = %d, registry providers = %d", len(cases), len(providerRegistry))
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
