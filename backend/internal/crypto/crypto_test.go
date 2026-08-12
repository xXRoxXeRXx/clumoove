package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

const testDomain = "test:credential"

func TestEncryptDecryptRoundTrip(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"
	plain := "my-super-secret-credential"
	ciphertext, err := EncryptWithDomain(plain, secret, testDomain)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	if ciphertext == plain || ciphertext == "" || !strings.HasPrefix(ciphertext, envelopeV1Prefix) {
		t.Errorf("unexpected ciphertext %q", ciphertext)
	}
	decrypted, err := DecryptBytesWithDomain(ciphertext, secret, testDomain)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	defer clear(decrypted)
	if string(decrypted) != plain {
		t.Errorf("decrypted %q, want %q", decrypted, plain)
	}
}

func TestEncryptDecryptDifferentCiphertexts(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"
	c1, err := EncryptWithDomain("same-input", secret, testDomain)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := EncryptWithDomain("same-input", secret, testDomain)
	if err != nil {
		t.Fatal(err)
	}
	if c1 == c2 {
		t.Error("expected random nonces to produce distinct ciphertexts")
	}
	for _, ciphertext := range []string{c1, c2} {
		plain, err := DecryptBytesWithDomain(ciphertext, secret, testDomain)
		if err != nil {
			t.Fatal(err)
		}
		if string(plain) != "same-input" {
			t.Errorf("got %q", plain)
		}
		clear(plain)
	}
}

func TestEmptySentinel(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"
	ciphertext, err := EncryptWithDomain("", secret, testDomain)
	if err != nil || ciphertext != "" {
		t.Errorf("Encrypt empty = %q, %v", ciphertext, err)
	}
	plain, err := DecryptBytesWithDomain("", secret, testDomain)
	if err != nil || plain != nil {
		t.Errorf("Decrypt empty = %q, %v", plain, err)
	}
}

func TestDecryptAuthenticationFailures(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"
	ciphertext, err := EncryptWithDomain("payload", secret, testDomain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptBytesWithDomain(ciphertext, "another-secret-key-which-is-also-32-bytes!!", testDomain); !errors.Is(err, ErrAuthentication) {
		t.Errorf("wrong key error = %v", err)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(ciphertext, envelopeV1Prefix))
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{0, len(raw) - 1} {
		tampered := append([]byte(nil), raw...)
		tampered[index] ^= 1
		if _, err := DecryptBytesWithDomain(envelopeV1Prefix+hex.EncodeToString(tampered), secret, testDomain); !errors.Is(err, ErrAuthentication) {
			t.Errorf("tampered ciphertext error = %v", err)
		}
	}
}

func TestDecryptDomainMismatch(t *testing.T) {
	ciphertext, err := EncryptWithDomain("payload", "test-encryption-secret-key-32-bytes-long!", testDomain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptBytesWithDomain(ciphertext, "test-encryption-secret-key-32-bytes-long!", "test:other"); !errors.Is(err, ErrAuthentication) {
		t.Errorf("domain mismatch error = %v", err)
	}
}

func TestConnectionCredentialDomain(t *testing.T) {
	if got := ConnectionCredentialDomain(false); got != DomainProviderPassword {
		t.Errorf("non-OAuth domain = %q", got)
	}
	if got := ConnectionCredentialDomain(true); got != DomainOAuthAccessToken {
		t.Errorf("OAuth domain = %q", got)
	}
}

func TestDecryptMalformedCiphertexts(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"
	if _, err := DecryptBytesWithDomain("not-hex!!", secret, testDomain); !errors.Is(err, ErrInvalidCiphertextEncoding) {
		t.Errorf("invalid hex error = %v", err)
	}
	if _, err := DecryptBytesWithDomain("v2:00", secret, testDomain); !errors.Is(err, ErrUnsupportedCiphertextVersion) {
		t.Errorf("version error = %v", err)
	}
	for size := 0; size < gcmNonceSize+16; size++ {
		ciphertext := envelopeV1Prefix + hex.EncodeToString(make([]byte, size))
		if _, err := DecryptBytesWithDomain(ciphertext, secret, testDomain); !errors.Is(err, ErrCiphertextTooShort) {
			t.Errorf("size %d error = %v", size, err)
		}
	}
}

func TestUnicodeAndLargePayloadRoundTrip(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"
	for _, want := range []string{"\u3071\u3059\u308f\u30fc\u3069\U0001f510", strings.Repeat("x", 1<<20)} {
		ciphertext, err := EncryptWithDomain(want, secret, testDomain)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecryptBytesWithDomain(ciphertext, secret, testDomain)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Error("round trip mismatch")
		}
		clear(got)
	}
}

func TestNonceUniquenessSanity(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		ciphertext, err := EncryptWithDomain("same-input", secret, testDomain)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := hex.DecodeString(strings.TrimPrefix(ciphertext, envelopeV1Prefix))
		if err != nil {
			t.Fatal(err)
		}
		nonce := string(raw[:gcmNonceSize])
		if _, ok := seen[nonce]; ok {
			t.Fatal("duplicate nonce")
		}
		seen[nonce] = struct{}{}
	}
}

func TestDecryptLegacyEnvelope(t *testing.T) {
	secret := "test-encryption-secret-key-32-bytes-long!"
	block, err := aes.NewCipher(deriveKey(secret))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	legacy := append(nonce, gcm.Seal(nil, nonce, []byte("legacy"), nil)...)
	plain, err := DecryptBytesWithDomain(hex.EncodeToString(legacy), secret, testDomain)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plain)
	if string(plain) != "legacy" {
		t.Errorf("legacy plaintext = %q", plain)
	}
}

func TestDeriveKeyLength(t *testing.T) {
	for _, s := range []string{"short", strings.Repeat("x", 100)} {
		if k := deriveKey(s); len(k) != 32 {
			t.Errorf("deriveKey(%q) returned %d bytes, want 32", s, len(k))
		}
	}
}
