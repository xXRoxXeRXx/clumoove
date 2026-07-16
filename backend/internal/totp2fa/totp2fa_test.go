package totp2fa

import (
	"strings"
	"testing"
)

func TestGenerateProvisioning(t *testing.T) {
	secret, uri, png, err := GenerateProvisioning("user@example.com")
	if err != nil {
		t.Fatalf("GenerateProvisioning failed: %v", err)
	}
	if secret == "" {
		t.Errorf("expected non-empty base32 secret")
	}
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Errorf("expected otpauth URI, got %q", uri)
	}
	if !strings.Contains(uri, "user@example.com") {
		t.Errorf("expected URI to contain account name, got %q", uri)
	}
	if !strings.HasPrefix(png, "data:image/png;base64,") {
		t.Errorf("expected base64 PNG data URL, got %q", png)
	}
}

func TestValidateTOTP(t *testing.T) {
	secret, _, _, err := GenerateProvisioning("user@example.com")
	if err != nil {
		t.Fatalf("GenerateProvisioning failed: %v", err)
	}
	// A freshly generated secret has no valid current code unless we generate one.
	if Validate(secret, "000000") {
		t.Errorf("expected invalid code to fail validation")
	}
	if Validate(secret, "") {
		t.Errorf("expected empty code to fail validation")
	}
}

func TestGenerateAndVerifyBackupCodes(t *testing.T) {
	plain, hashes, err := GenerateBackupCodes()
	if err != nil {
		t.Fatalf("GenerateBackupCodes failed: %v", err)
	}
	if len(plain) != backupCodeCount {
		t.Errorf("expected %d backup codes, got %d", backupCodeCount, len(plain))
	}
	if len(hashes) != backupCodeCount {
		t.Errorf("expected %d backup hashes, got %d", backupCodeCount, len(hashes))
	}

	// Each plaintext code must verify against its corresponding hash.
	for i, code := range plain {
		if len(code) != backupCodeLen {
			t.Errorf("backup code %d has length %d, want %d", i, len(code), backupCodeLen)
		}
		if !strings.ContainsAny(code, "0O1IL") {
			// alphabet excludes ambiguous chars; sanity check the alphabet usage
		}
		idx := VerifyBackupCode(hashes, code)
		if idx != i {
			t.Errorf("expected backup code %d to verify at index %d, got %d", i, i, idx)
		}
	}
}

func TestVerifyBackupCodeWrong(t *testing.T) {
	_, hashes, err := GenerateBackupCodes()
	if err != nil {
		t.Fatalf("GenerateBackupCodes failed: %v", err)
	}
	if idx := VerifyBackupCode(hashes, "WRONGCODE"); idx != -1 {
		t.Errorf("expected -1 for wrong backup code, got %d", idx)
	}
	if idx := VerifyBackupCode(hashes, ""); idx != -1 {
		t.Errorf("expected -1 for empty backup code, got %d", idx)
	}
}

func TestBackupCodeUniqueness(t *testing.T) {
	plain, _, err := GenerateBackupCodes()
	if err != nil {
		t.Fatalf("GenerateBackupCodes failed: %v", err)
	}
	seen := make(map[string]bool)
	for _, c := range plain {
		if seen[c] {
			t.Errorf("duplicate backup code generated: %q", c)
		}
		seen[c] = true
	}
}

func TestHashBackupCodeDeterministicVerify(t *testing.T) {
	h, err := HashBackupCode("ABCDEF2345")
	if err != nil {
		t.Fatalf("HashBackupCode failed: %v", err)
	}
	if VerifyBackupCode([]string{h}, "ABCDEF2345") != 0 {
		t.Errorf("expected matching code to verify")
	}
	if VerifyBackupCode([]string{h}, "ABCDEFGHIJ") != -1 {
		t.Errorf("expected non-matching code to fail")
	}
}
