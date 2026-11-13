// Package megasecret centralizes short-lived MEGA session decoding so every
// background path applies the same encrypted-storage boundary.
package megasecret

import (
	"encoding/base64"
	"errors"
	"fmt"

	"backend/internal/crypto"
	"backend/internal/storage"
)

// DecodeMegaSession decrypts persisted MEGA session material. The returned
// master key belongs to the caller and must be cleared when it is no longer
// needed.
func DecodeMegaSession(sessionIDEncrypted, masterKeyEncrypted, encryptionKey string) (storage.MegaSession, error) {
	if sessionIDEncrypted == "" || masterKeyEncrypted == "" {
		return storage.MegaSession{}, errors.New("incomplete encrypted MEGA session")
	}

	idBytes, err := crypto.DecryptBytesWithDomain(sessionIDEncrypted, encryptionKey, crypto.DomainMegaSessionID)
	if err != nil {
		return storage.MegaSession{}, fmt.Errorf("decrypt mega session id: %w", err)
	}
	defer clear(idBytes)
	if len(idBytes) == 0 {
		return storage.MegaSession{}, errors.New("invalid encrypted MEGA session")
	}

	keyText, err := crypto.DecryptBytesWithDomain(masterKeyEncrypted, encryptionKey, crypto.DomainMegaMasterKey)
	if err != nil {
		return storage.MegaSession{}, fmt.Errorf("decrypt mega master key: %w", err)
	}
	defer clear(keyText)

	key := make([]byte, base64.StdEncoding.DecodedLen(len(keyText)))
	n, err := base64.StdEncoding.Decode(key, keyText)
	if err != nil || n == 0 {
		clear(key)
		return storage.MegaSession{}, errors.New("invalid encrypted MEGA session")
	}
	key = key[:n]
	return storage.MegaSession{ID: string(idBytes), MasterKey: key}, nil
}

// DecodeProviderMegaSession decrypts persisted MEGA session material when the
// provider is "mega" and encrypted session fields are present. Callers must
// clear the returned MasterKey when finished.
func DecodeProviderMegaSession(provider, sessionIDEncrypted, masterKeyEncrypted, encryptionKey string) (storage.MegaSession, error) {
	if provider != "mega" || (sessionIDEncrypted == "" && masterKeyEncrypted == "") {
		return storage.MegaSession{}, nil
	}
	return DecodeMegaSession(sessionIDEncrypted, masterKeyEncrypted, encryptionKey)
}
