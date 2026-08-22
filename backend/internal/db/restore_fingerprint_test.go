package db

import (
	"fmt"
	"testing"
)

func TestRestoreConfigFingerprintCanonicalizesEquivalentConfiguration(t *testing.T) {
	first, err := RestoreConfigFingerprint("snapshot", StringArray{"/documents/", "photos/sub"}, "Google", "/restore/", "profile", "rename")
	if err != nil {
		t.Fatal(err)
	}
	second, err := RestoreConfigFingerprint("snapshot", StringArray{"photos/sub/", "documents"}, "google", "restore", "profile", "RENAME")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("equivalent configurations produced different fingerprints")
	}
}

func TestRestoreConfigFingerprintPathNormalization(t *testing.T) {
	// Root and clean paths
	first, err := RestoreConfigFingerprint("snap1", StringArray{"/"}, "s3", "/target", "p1", "OVERWRITE")
	if err != nil {
		t.Fatal(err)
	}
	second, err := RestoreConfigFingerprint("snap1", StringArray{""}, "s3", "target/", "p1", "overwrite")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("root path representations produced different fingerprints")
	}

	// Redundant slashes and dots
	a, err := RestoreConfigFingerprint("snap1", StringArray{"a//b/c", "d/./e"}, "local", "/", "p1", "SKIP")
	if err != nil {
		t.Fatal(err)
	}
	b, err := RestoreConfigFingerprint("snap1", StringArray{"d/e", "/a/b/c/"}, "local", "", "p1", "skip")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("unclean paths produced different fingerprints from clean paths")
	}
}

func TestRestoreConfigFingerprintChangesForTargetOrSelection(t *testing.T) {
	base, err := RestoreConfigFingerprint("snapshot", StringArray{"documents"}, "google", "restore", "profile", "RENAME")
	if err != nil {
		t.Fatal(err)
	}
	changedPath, err := RestoreConfigFingerprint("snapshot", StringArray{"photos"}, "google", "restore", "profile", "RENAME")
	if err != nil {
		t.Fatal(err)
	}
	changedRoot, err := RestoreConfigFingerprint("snapshot", StringArray{"documents"}, "google", "different", "profile", "RENAME")
	if err != nil {
		t.Fatal(err)
	}
	changedIdentity, err := RestoreConfigFingerprintWithIdentity("snapshot", StringArray{"documents"}, "google", "restore", "user@example.com", "RENAME")
	if err != nil {
		t.Fatal(err)
	}
	if base == changedPath || base == changedRoot || base == changedIdentity {
		t.Fatal("different configurations produced the same fingerprint")
	}
}

func TestRestoreConfigFingerprintGolden(t *testing.T) {
	fp, err := RestoreConfigFingerprint(
		"00000000-0000-0000-0000-000000000001",
		StringArray{"pics", "docs"},
		"Nextcloud",
		"/Restored/",
		"11111111-1111-1111-1111-111111111111",
		"rename",
	)
	if err != nil {
		t.Fatal(err)
	}
	// Expected canonical JSON:
	// {"format":"restore-config-v1","snapshot":"00000000-0000-0000-0000-000000000001","paths":["docs","pics"],"provider":"nextcloud","root":"Restored","identity":"profile:11111111-1111-1111-1111-111111111111","conflict":"RENAME"}
	const expectedHex = "987c4d8749e0a9a32b1cc5a581c5e7ac2d8478c9dc7e95a05cd41b185aecaf02"
	actualHex := fmt.Sprintf("%x", fp)
	if actualHex != expectedHex {
		t.Fatalf("golden fingerprint mismatch:\n  got:  %s\n  want: %s", actualHex, expectedHex)
	}

	// Direct connection golden test
	fpDirect, err := RestoreConfigFingerprintWithIdentity(
		"00000000-0000-0000-0000-000000000002",
		StringArray{"/data/files/", "notes.txt"},
		"S3",
		"backup-restore",
		"mybucket/user1",
		"skip",
	)
	if err != nil {
		t.Fatal(err)
	}
	// Expected canonical JSON:
	// {"format":"restore-config-v1","snapshot":"00000000-0000-0000-0000-000000000002","paths":["data/files","notes.txt"],"provider":"s3","root":"backup-restore","identity":"mybucket/user1","conflict":"SKIP"}
	const expectedDirectHex = "c5d3b054610599cd1998dc0c3b8159f483b8cf5d371dd5db7b28b8028702b222"
	actualDirectHex := fmt.Sprintf("%x", fpDirect)
	if actualDirectHex != expectedDirectHex {
		t.Fatalf("golden direct fingerprint mismatch:\n  got:  %s\n  want: %s", actualDirectHex, expectedDirectHex)
	}
}
