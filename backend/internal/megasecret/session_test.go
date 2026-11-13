package megasecret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"backend/internal/crypto"
	"backend/internal/storage"
)

const testEncryptionKey = "test-encryption-secret-key-32-bytes-long!"

func TestDecodeProviderMegaSession(t *testing.T) {
	idEncrypted := encrypt(t, "session-id", crypto.DomainMegaSessionID)
	keyEncrypted := encrypt(t, base64.StdEncoding.EncodeToString([]byte("master-key")), crypto.DomainMegaMasterKey)
	emptyIDEncrypted := encryptEmpty(t, crypto.DomainMegaSessionID)

	tests := []struct {
		name               string
		provider           string
		sessionIDEncrypted string
		masterKeyEncrypted string
		wantErr            string
		wantEmpty          bool
		wantSession        storage.MegaSession
	}{
		{
			name:               "non mega provider",
			provider:           "dropbox",
			sessionIDEncrypted: idEncrypted,
			masterKeyEncrypted: keyEncrypted,
			wantEmpty:          true,
		},
		{
			name:      "mega without persisted session",
			provider:  "mega",
			wantEmpty: true,
		},
		{
			name:               "incomplete session",
			provider:           "mega",
			sessionIDEncrypted: idEncrypted,
			wantErr:            "incomplete encrypted MEGA session",
		},
		{
			name:               "session id decrypt failure",
			provider:           "mega",
			sessionIDEncrypted: "invalid",
			masterKeyEncrypted: keyEncrypted,
			wantErr:            "decrypt mega session id",
		},
		{
			name:               "master key decrypt failure",
			provider:           "mega",
			sessionIDEncrypted: idEncrypted,
			masterKeyEncrypted: "invalid",
			wantErr:            "decrypt mega master key",
		},
		{
			name:               "invalid base64 master key",
			provider:           "mega",
			sessionIDEncrypted: idEncrypted,
			masterKeyEncrypted: encrypt(t, "not base64", crypto.DomainMegaMasterKey),
			wantErr:            "invalid encrypted MEGA session",
		},
		{
			name:               "empty plaintext session id",
			provider:           "mega",
			sessionIDEncrypted: emptyIDEncrypted,
			masterKeyEncrypted: keyEncrypted,
			wantErr:            "invalid encrypted MEGA session",
		},
		{
			name:               "valid session",
			provider:           "mega",
			sessionIDEncrypted: idEncrypted,
			masterKeyEncrypted: keyEncrypted,
			wantSession:        storage.MegaSession{ID: "session-id", MasterKey: []byte("master-key")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, err := DecodeProviderMegaSession(tt.provider, tt.sessionIDEncrypted, tt.masterKeyEncrypted, testEncryptionKey)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer clear(session.MasterKey)
			if tt.wantEmpty {
				if session.ID != "" || len(session.MasterKey) != 0 {
					t.Fatalf("session = %#v, want empty", session)
				}
				return
			}
			if session.ID != tt.wantSession.ID || string(session.MasterKey) != string(tt.wantSession.MasterKey) {
				t.Fatalf("session = %#v, want %#v", session, tt.wantSession)
			}
		})
	}
}

func encrypt(t *testing.T, plaintext, domain string) string {
	t.Helper()
	ciphertext, err := crypto.EncryptWithDomain(plaintext, testEncryptionKey, domain)
	if err != nil {
		t.Fatal(err)
	}
	return ciphertext
}

func encryptEmpty(t *testing.T, domain string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(testEncryptionKey))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	ciphertext := gcm.Seal(nil, nonce, nil, []byte(domain))
	return "v1:" + hex.EncodeToString(append(nonce, ciphertext...))
}
