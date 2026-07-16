package crypto

import (
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"
	plain := "my-super-secret-credential"

	cipher, err := Encrypt(plain, secret)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	if cipher == plain {
		t.Errorf("ciphertext must differ from plaintext, got identical value")
	}
	if cipher == "" {
		t.Errorf("ciphertext must not be empty")
	}

	decrypted, err := Decrypt(cipher, secret)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if decrypted != plain {
		t.Errorf("decrypted %q, want %q", decrypted, plain)
	}
}

func TestEncryptDecryptDifferentCiphertexts(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"

	c1, err := Encrypt("same-input", secret)
	if err != nil {
		t.Fatalf("Encrypt 1 failed: %v", err)
	}
	c2, err := Encrypt("same-input", secret)
	if err != nil {
		t.Fatalf("Encrypt 2 failed: %v", err)
	}
	if c1 == c2 {
		t.Errorf("expected different ciphertexts for identical plaintext due to random nonce")
	}

	d1, err := Decrypt(c1, secret)
	if err != nil {
		t.Fatalf("Decrypt 1 failed: %v", err)
	}
	d2, err := Decrypt(c2, secret)
	if err != nil {
		t.Fatalf("Decrypt 2 failed: %v", err)
	}
	if d1 != "same-input" || d2 != "same-input" {
		t.Errorf("both decryptions must yield original plaintext")
	}
}

func TestEncryptDecryptEmpty(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"

	c, err := Encrypt("", secret)
	if err != nil {
		t.Fatalf("Encrypt empty failed: %v", err)
	}
	if c != "" {
		t.Errorf("expected empty ciphertext for empty plaintext, got %q", c)
	}
	d, err := Decrypt("", secret)
	if err != nil {
		t.Fatalf("Decrypt empty failed: %v", err)
	}
	if d != "" {
		t.Errorf("expected empty plaintext for empty ciphertext, got %q", d)
	}
}

func TestDecryptWrongKey(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"
	wrong := "another-secret-key-which-is-also-32-bytes!!"

	c, err := Encrypt("payload", secret)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	if _, err := Decrypt(c, wrong); err == nil {
		t.Errorf("expected Decrypt to fail with wrong key, got nil error")
	}
}

func TestDecryptInvalidHex(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"
	if _, err := Decrypt("not-hex!!", secret); err == nil {
		t.Errorf("expected Decrypt to fail on invalid hex input")
	}
}

func TestDecryptTooShort(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"
	if _, err := Decrypt("abcd", secret); err == nil {
		t.Errorf("expected Decrypt to fail on ciphertext shorter than nonce")
	}
}

// heapString returns a string that is NOT a string literal in read-only memory,
// so it is safe to mutate via ZeroString (which overwrites backing bytes).
func heapString(s string) string {
	return strings.Clone(s)
}

func TestZeroString(t *testing.T) {
	s := heapString("sensitive-data")
	ZeroString(&s)
	if s != "" {
		t.Errorf("expected ZeroString to reset string to empty, got %q", s)
	}

	// nil and empty must be no-ops and not panic.
	ZeroString(nil)
	empty := heapString("")
	ZeroString(&empty)
	if empty != "" {
		t.Errorf("expected empty string to remain empty")
	}
}

func TestDeriveKeyLength(t *testing.T) {
	for _, s := range []string{"short", strings.Repeat("x", 100)} {
		k := deriveKey(s)
		if len(k) != 32 {
			t.Errorf("deriveKey(%q) returned %d bytes, want 32", s, len(k))
		}
	}
}
