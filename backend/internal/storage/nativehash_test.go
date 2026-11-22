package storage

import (
	"fmt"
	"strings"
	"testing"
)

// TestNativeHashDeclarations pins the streaming hash each whitelisted provider
// declares. A new provider must add a row here, which forces the author to
// decide its hash rules deliberately.
func TestNativeHashDeclarations(t *testing.T) {
	want := map[string]struct {
		algorithm         string
		hasherType        string
		fixedOnSource     bool
		ignoresListedHash bool
	}{
		"nextcloud":    {algorithm: "SHA1", hasherType: "sha1"},
		"opencloud":    {algorithm: "SHA1", hasherType: "sha1"},
		"webdav":       {algorithm: "SHA1", hasherType: "sha1", ignoresListedHash: true},
		"dropbox":      {algorithm: "DROPBOX", hasherType: "DropboxHasher", fixedOnSource: true},
		"google":       {algorithm: "MD5", hasherType: "md5", fixedOnSource: true},
		"onedrive":     {algorithm: "QUICKXOR", hasherType: "QuickXorHasher", fixedOnSource: true},
		"hidrive":      {algorithm: "HIDRIVE", hasherType: "HiDriveHasher"},
		"smb":          {algorithm: "SHA1", hasherType: "sha1"},
		"s3":           {algorithm: "SHA256", hasherType: "sha256"},
		"sftp":         {algorithm: "SHA1", hasherType: "sha1"},
		"ftp":          {algorithm: "SHA1", hasherType: "sha1"},
		"magentacloud": {algorithm: "SHA1", hasherType: "sha1"},
		"koofr":        {algorithm: "MD5", hasherType: "md5", fixedOnSource: true},
		"local":        {algorithm: "SHA1", hasherType: "sha1"},
		"immich":       {algorithm: "SHA1", hasherType: "sha1"},
		"seafile":      {algorithm: "SHA1", hasherType: "sha1"},
		"mega":         {algorithm: "SHA1", hasherType: "sha1"},
		"box":          {algorithm: "SHA1", hasherType: "sha1"},
	}

	if len(want) != len(ValidProviders) {
		t.Fatalf("table covers %d providers, whitelist has %d", len(want), len(ValidProviders))
	}
	for _, providerType := range ValidProviders {
		expected, ok := want[providerType]
		if !ok {
			t.Errorf("provider %q has no native hash row in this table", providerType)
			continue
		}
		t.Run(providerType, func(t *testing.T) {
			native := NativeHashFor(providerType)
			if native.Algorithm != expected.algorithm {
				t.Fatalf("Algorithm = %q, want %q", native.Algorithm, expected.algorithm)
			}
			if native.New == nil {
				t.Fatal("New is nil")
			}
			if got := fmt.Sprintf("%T", native.New()); !strings.Contains(got, expected.hasherType) {
				t.Fatalf("hasher type = %s, want it to contain %q", got, expected.hasherType)
			}
			if native.FixedOnSource != expected.fixedOnSource {
				t.Fatalf("FixedOnSource = %v, want %v", native.FixedOnSource, expected.fixedOnSource)
			}
			if got := IgnoresListedSourceHash(providerType); got != expected.ignoresListedHash {
				t.Fatalf("IgnoresListedSourceHash = %v, want %v", got, expected.ignoresListedHash)
			}
		})
	}
}

func TestNativeHashForUnknownProviderDefaultsToSHA1(t *testing.T) {
	for _, providerType := range []string{"", "not-a-provider"} {
		native := NativeHashFor(providerType)
		if native.Algorithm != "SHA1" || native.New == nil || native.FixedOnSource {
			t.Fatalf("NativeHashFor(%q) = %+v, want the SHA1 default", providerType, native)
		}
		if IgnoresListedSourceHash(providerType) {
			t.Fatalf("IgnoresListedSourceHash(%q) = true, want false", providerType)
		}
	}
}

func TestNativeHashNewReturnsFreshHashers(t *testing.T) {
	native := NativeHashFor("s3")
	first, second := native.New(), native.New()
	_, _ = first.Write([]byte("data"))
	if second.Size() == 0 || fmt.Sprintf("%x", second.Sum(nil)) == fmt.Sprintf("%x", first.Sum(nil)) {
		t.Fatal("New must return an independent hasher on every call")
	}
}
