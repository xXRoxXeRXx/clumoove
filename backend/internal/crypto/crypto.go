package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"unsafe"
)

// deriveKey ensures the key is exactly 32 bytes using SHA-256
func deriveKey(secret string) []byte {
	hash := sha256.Sum256([]byte(secret))
	return hash[:]
}

// Encrypt encrypts plain text using AES-256-GCM with a secret key
func Encrypt(plainText string, secretKey string) (string, error) {
	if plainText == "" {
		return "", nil
	}

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
// Best-effort: it does nothing if the string is empty or unexported internals
// change across Go versions.
func ZeroString(s *string) {
	if s == nil || *s == "" {
		return
	}
	// Convert the string header to a byte slice header and zero the bytes.
	// This is safe because *s is an unaliased decrypted value owned by the
	// caller, never a string literal from read-only memory.
	sh := (*reflect.StringHeader)(unsafe.Pointer(s))
	if sh.Len == 0 {
		return
	}
	b := unsafe.Slice((*byte)(unsafe.Pointer(sh.Data)), sh.Len)
	for i := range b {
		b[i] = 0
	}
	*s = ""
}
