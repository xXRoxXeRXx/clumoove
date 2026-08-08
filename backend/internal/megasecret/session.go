// Package megasecret centralizes short-lived MEGA session decoding so every
// background path applies the same encrypted-storage boundary.
package megasecret

import (
	"context"
	"encoding/base64"
	"errors"

	"backend/internal/crypto"
	"backend/internal/storage"
)

func WithSession(ctx context.Context, provider, sessionIDEncrypted, masterKeyEncrypted, encryptionKey string) (context.Context, error) {
	if provider != "mega" || (sessionIDEncrypted == "" && masterKeyEncrypted == "") {
		return ctx, nil
	}
	if sessionIDEncrypted == "" || masterKeyEncrypted == "" {
		return nil, errors.New("incomplete encrypted MEGA session")
	}
	id, err := crypto.Decrypt(sessionIDEncrypted, encryptionKey)
	if err != nil {
		return nil, err
	}
	keyText, err := crypto.Decrypt(masterKeyEncrypted, encryptionKey)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil || id == "" || len(key) == 0 {
		return nil, errors.New("invalid encrypted MEGA session")
	}
	return storage.WithMegaSession(ctx, storage.MegaSession{ID: id, MasterKey: key}), nil
}
