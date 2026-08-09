package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"runtime"
	"unsafe"
)

// deriveKey ensures the key is exactly 32 bytes using SHA-256. The input is
// the deployment's high-entropy ENCRYPTION_SECRET_KEY, not a user password;
// this is key derivation for AES, never password verification.
func deriveKey(secret string) []byte {
	hash := sha256.Sum256([]byte(secret))
	return hash[:]
}

// Encrypt encrypts plain text using AES-256-GCM with a secret key
func Encrypt(plainText string, secretKey string) (string, error) {
	if plainText == "" {
		return "", nil
	}

	// codeql[go/weak-sensitive-data-hashing]: secretKey is a deployment encryption key; SHA-256 derives a fixed-size AES key and is not used for password hashing.
	key := deriveKey(secretKey)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	// 12-byte nonce for GCM
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	sealed := gcm.Seal(nil, nonce, []byte(plainText), nil)

	// Combine nonce + ciphertext and encode as hex
	combined := append(nonce, sealed...)
	return hex.EncodeToString(combined), nil
}

// Decrypt decrypts hex-encoded cipher text using AES-256-GCM with a secret key
func Decrypt(cipherTextHex string, secretKey string) (string, error) {
	if cipherTextHex == "" {
		return "", nil
	}

	// codeql[go/weak-sensitive-data-hashing]: secretKey is a deployment encryption key; SHA-256 derives a fixed-size AES key and is not used for password hashing.
	key := deriveKey(secretKey)
	combined, err := hex.DecodeString(cipherTextHex)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(combined) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce := combined[:nonceSize]
	cipherText := combined[nonceSize:]

	plainText, err := gcm.Open(nil, nonce, cipherText, nil)
	if err != nil {
		return "", err
	}

	return string(plainText), nil
}

// ZeroString overwrites the backing memory of s with zero bytes so that
// decrypted plaintext credentials do not linger after they are no longer
// needed (zero-plaintext-in-memory goal). It mutates the caller's string via
// a pointer. The string must not be referenced elsewhere (e.g. a constant).
// Best-effort: callers must only pass an unaliased, mutable string allocated
// from decrypted data; string literals and shared strings must never be passed.
func ZeroString(s *string) {
	if s == nil || *s == "" {
		return
	}
	// unsafe.StringData avoids the deprecated reflect.StringHeader layout.
	// The backing storage is owned by the caller (see the contract above).
	b := unsafe.Slice(unsafe.StringData(*s), len(*s))
	clear(b)
	runtime.KeepAlive(b)
	*s = ""
}
