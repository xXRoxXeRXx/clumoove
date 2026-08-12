// Package crypto provides encryption for persisted application secrets.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

const (
	// envelopeV1Prefix makes new ciphertext self-identifying without
	// ambiguously colliding with the legacy hex-only envelope.
	envelopeV1Prefix = "v1:"
	// gcmNonceSize is the standard 96-bit nonce size used by cipher.NewGCM.
	gcmNonceSize = 12

	DomainProviderPassword   = "clumoove:provider-password"
	DomainOAuthAccessToken   = "clumoove:oauth-access-token"
	DomainOAuthRefreshToken  = "clumoove:oauth-refresh-token"
	DomainTOTPSecret         = "clumoove:totp-secret"
	DomainMegaSessionID      = "clumoove:mega-session-id"
	DomainMegaMasterKey      = "clumoove:mega-master-key"
	DomainSMTPPassword       = "clumoove:smtp-password"
	DomainOAuthClientSecret  = "clumoove:oauth-client-secret"
	DomainNotificationConfig = "clumoove:notification-config"
)

var (
	// ErrCiphertextTooShort indicates that an envelope cannot contain a GCM
	// nonce and authentication tag.
	ErrCiphertextTooShort = errors.New("crypto: ciphertext too short")
	// ErrAuthentication indicates that a ciphertext was tampered with, was
	// encrypted with a different key, or was encrypted for another domain.
	ErrAuthentication = errors.New("crypto: authentication failed")
	// ErrUnsupportedCiphertextVersion indicates an unknown envelope version.
	ErrUnsupportedCiphertextVersion = errors.New("crypto: unsupported ciphertext version")
	// ErrInvalidCiphertextEncoding indicates a malformed hex envelope.
	ErrInvalidCiphertextEncoding = errors.New("crypto: invalid ciphertext encoding")
)

// ConnectionCredentialDomain returns the AAD domain for the polymorphic
// password_encrypted columns. OAuth providers store an access token there;
// other providers store a password or API key.
func ConnectionCredentialDomain(isOAuthProvider bool) string {
	if isOAuthProvider {
		return DomainOAuthAccessToken
	}
	return DomainProviderPassword
}

// deriveKey ensures the key is exactly 32 bytes using SHA-256. The input is
// the deployment's high-entropy ENCRYPTION_SECRET_KEY, not a user password;
// this is key derivation for AES, never password verification. The digest is
// deliberately not cached so the derived key is not retained process-wide.
func deriveKey(secret string) []byte {
	hash := sha256.Sum256([]byte(secret))
	return hash[:]
}

// EncryptWithDomain encrypts plaintext using AES-256-GCM and authenticates it
// to domain. Empty plaintext is a no-op sentinel and returns an empty
// ciphertext. Domain must be a stable, non-empty identifier for the persisted
// field type; it prevents ciphertext from another secret field being replayed.
func EncryptWithDomain(plainText, secretKey, domain string) (string, error) {
	if plainText == "" {
		return "", nil
	}
	if domain == "" {
		return "", errors.New("crypto: empty encryption domain")
	}

	block, err := aes.NewCipher(deriveKey(secretKey))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcmNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(plainText), []byte(domain))
	combined := make([]byte, len(nonce)+len(sealed))
	copy(combined, nonce)
	copy(combined[len(nonce):], sealed)
	return envelopeV1Prefix + hex.EncodeToString(combined), nil
}

// DecryptWithDomain decrypts a field-specific ciphertext into a string. The
// temporary GCM plaintext allocation is cleared before this function returns.
// Prefer DecryptBytesWithDomain when the receiving API can avoid strings.
func DecryptWithDomain(cipherText string, secretKey, domain string) (string, error) {
	plainText, err := DecryptBytesWithDomain(cipherText, secretKey, domain)
	if err != nil {
		return "", err
	}
	defer clear(plainText)
	return string(plainText), nil
}

// ZeroString releases the caller's reference to a sensitive string as soon as
// it is no longer needed. Go strings are immutable, so their backing storage
// cannot be reliably overwritten; callers that can avoid a string entirely
// should use DecryptBytesWithDomain and clear the returned buffer instead.
func ZeroString(value *string) {
	if value != nil {
		*value = ""
	}
}

// DecryptBytesWithDomain decrypts a field-specific ciphertext. It accepts the
// pre-versioned, unauthenticated-domain envelope for existing database rows;
// callers should re-encrypt legacy values during their normal next write.
// The returned buffer contains the GCM plaintext allocation and must be
// cleared by callers handling secrets.
func DecryptBytesWithDomain(cipherText, secretKey, domain string) ([]byte, error) {
	if cipherText == "" {
		return nil, nil
	}
	if domain == "" {
		return nil, errors.New("crypto: empty encryption domain")
	}

	legacy := false
	encoded := cipherText
	if len(cipherText) >= len(envelopeV1Prefix) && cipherText[:len(envelopeV1Prefix)] == envelopeV1Prefix {
		encoded = cipherText[len(envelopeV1Prefix):]
	} else if len(cipherText) >= 2 && cipherText[0] == 'v' && cipherText[1] >= '0' && cipherText[1] <= '9' {
		return nil, ErrUnsupportedCiphertextVersion
	} else {
		legacy = true
	}

	combined, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCiphertextEncoding, err)
	}
	block, err := aes.NewCipher(deriveKey(secretKey))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(combined) < gcmNonceSize+gcm.Overhead() {
		return nil, ErrCiphertextTooShort
	}

	additionalData := []byte(domain)
	if legacy {
		additionalData = nil
	}
	plainText, err := gcm.Open(nil, combined[:gcmNonceSize], combined[gcmNonceSize:], additionalData)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthentication, err)
	}
	return plainText, nil
}
