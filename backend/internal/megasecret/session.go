// Package megasecret centralizes short-lived MEGA session decoding so every
// background path applies the same encrypted-storage boundary.
package megasecret

import (
	"context"
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

// WithMegaSession decrypts and attaches persisted MEGA session material to a
// task context. It leaves non-MEGA providers and empty MEGA sessions alone so
// MEGA can fall back to email/password authentication.
func WithMegaSession(ctx context.Context, provider, sessionIDEncrypted, masterKeyEncrypted, encryptionKey string) (context.Context, error) {
	if provider != "mega" || (sessionIDEncrypted == "" && masterKeyEncrypted == "") {
		return ctx, nil
	}

	session, err := DecodeMegaSession(sessionIDEncrypted, masterKeyEncrypted, encryptionKey)
	if err != nil {
		return nil, err
	}
	ctxOut := storage.WithMegaSession(ctx, session)
	clear(session.MasterKey)
	crypto.ZeroString(&session.ID)
	return ctxOut, nil
}
