package oauth

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
)

const rotationFakeKey = "test-rotation-store-encryption-key"

type rotationRecord struct {
	userID, provider, prevHash, rotatedHash, encrypted string
}

// fakeRotationStore is an in-memory db.OAuthRotationStore; any method it does
// not override panics through the nil embedded interface.
type fakeRotationStore struct {
	db.OAuthRotationStore
	chain     map[string]rotationRecord // keyed by previous token hash
	lookupErr error
	recordErr error
	recorded  []rotationRecord
}

func (f *fakeRotationStore) LookupOAuthTokenRotation(_ context.Context, _, _, tokenHash string) (string, string, bool, error) {
	if f.lookupErr != nil {
		return "", "", false, f.lookupErr
	}
	rec, ok := f.chain[tokenHash]
	if !ok {
		return "", "", false, nil
	}
	return rec.rotatedHash, rec.encrypted, true, nil
}

func (f *fakeRotationStore) RecordOAuthTokenRotation(_ context.Context, userID, provider, prevHash, rotatedHash, encrypted string) error {
	f.recorded = append(f.recorded, rotationRecord{userID, provider, prevHash, rotatedHash, encrypted})
	return f.recordErr
}

func mustEncryptRefresh(t *testing.T, plain string) string {
	t.Helper()
	enc, err := crypto.EncryptWithDomain(plain, rotationFakeKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return enc
}

func TestLookupRotatedRefreshTokenResolvesThroughTheRotationStore(t *testing.T) {
	hashA := db.OAuthTokenRotationHash("u1", "google", "A")
	hashB := db.OAuthTokenRotationHash("u1", "google", "B")
	store := &fakeRotationStore{chain: map[string]rotationRecord{
		hashA: {rotatedHash: hashB, encrypted: mustEncryptRefresh(t, "B")},
	}}

	got, ok, err := lookupRotatedRefreshToken(context.Background(), store, nil, NewRotationCache(), rotationFakeKey, "u1", "google", "A", time.Now())

	if err != nil || !ok || got != "B" {
		t.Fatalf("lookup = (%q, %v, %v), want (B, true, nil)", got, ok, err)
	}
}

func TestLookupRotatedRefreshTokenFollowsAChainOfStoredHops(t *testing.T) {
	hashA := db.OAuthTokenRotationHash("u1", "google", "A")
	hashB := db.OAuthTokenRotationHash("u1", "google", "B")
	hashC := db.OAuthTokenRotationHash("u1", "google", "C")
	store := &fakeRotationStore{chain: map[string]rotationRecord{
		hashA: {rotatedHash: hashB, encrypted: mustEncryptRefresh(t, "B")},
		hashB: {rotatedHash: hashC, encrypted: mustEncryptRefresh(t, "C")},
	}}

	got, ok, err := lookupRotatedRefreshToken(context.Background(), store, nil, NewRotationCache(), rotationFakeKey, "u1", "google", "A", time.Now())

	if err != nil || !ok || got != "C" {
		t.Fatalf("lookup = (%q, %v, %v), want (C, true, nil)", got, ok, err)
	}
}

func TestLookupRotatedRefreshTokenReportsStoreFailureAsRotationPersistence(t *testing.T) {
	store := &fakeRotationStore{lookupErr: errors.New("boom")}

	_, _, err := lookupRotatedRefreshToken(context.Background(), store, nil, NewRotationCache(), rotationFakeKey, "u1", "google", "A", time.Now())

	if !errors.Is(err, ErrRotationPersistence) {
		t.Fatalf("error = %v, want ErrRotationPersistence", err)
	}
}

func TestLookupRotatedRefreshTokenTreatsASkippedStoreAsAMiss(t *testing.T) {
	store := &fakeRotationStore{lookupErr: driver.ErrSkip}

	got, ok, err := lookupRotatedRefreshToken(context.Background(), store, nil, NewRotationCache(), rotationFakeKey, "u1", "google", "A", time.Now())

	if err != nil || ok || got != "" {
		t.Fatalf("lookup = (%q, %v, %v), want a clean miss", got, ok, err)
	}
}

func TestRecordRotatedRefreshTokenPersistsEveryPreviousTokenThroughTheRotationStore(t *testing.T) {
	store := &fakeRotationStore{}

	err := recordRotatedRefreshToken(context.Background(), store, nil, NewRotationCache(), rotationFakeKey, "u1", "google", "C", time.Now(), "A", "B", "A", "C")

	if err != nil {
		t.Fatalf("record error = %v", err)
	}
	hashC := db.OAuthTokenRotationHash("u1", "google", "C")
	if len(store.recorded) != 2 {
		t.Fatalf("recorded = %+v, want one mapping each for A and B", store.recorded)
	}
	for i, prev := range []string{"A", "B"} {
		rec := store.recorded[i]
		if rec.userID != "u1" || rec.provider != "google" || rec.prevHash != db.OAuthTokenRotationHash("u1", "google", prev) || rec.rotatedHash != hashC {
			t.Fatalf("recorded[%d] = %+v", i, rec)
		}
		if plain, derr := crypto.DecryptWithDomain(rec.encrypted, rotationFakeKey, crypto.DomainOAuthRefreshToken); derr != nil || plain != "C" {
			t.Fatalf("recorded[%d] ciphertext decrypts to (%q, %v), want C", i, plain, derr)
		}
	}
}

func TestRecordRotatedRefreshTokenReportsStoreFailureAsRotationPersistence(t *testing.T) {
	store := &fakeRotationStore{recordErr: errors.New("boom")}

	err := recordRotatedRefreshToken(context.Background(), store, nil, NewRotationCache(), rotationFakeKey, "u1", "google", "B", time.Now(), "A")

	if !errors.Is(err, ErrRotationPersistence) {
		t.Fatalf("error = %v, want ErrRotationPersistence", err)
	}
}

func TestRotationsWithoutADurableStoreStayInTheProcessCache(t *testing.T) {
	cache := NewRotationCache()
	now := time.Now()

	if err := recordRotatedRefreshToken(context.Background(), nil, nil, cache, rotationFakeKey, "u1", "google", "B", now, "A"); err != nil {
		t.Fatalf("record without a store: %v", err)
	}
	got, ok, err := lookupRotatedRefreshToken(context.Background(), nil, nil, cache, rotationFakeKey, "u1", "google", "A", now)

	if err != nil || !ok || got != "B" {
		t.Fatalf("lookup = (%q, %v, %v), want (B, true, nil) from the cache", got, ok, err)
	}
}

func TestRepairWithoutADurableStoreDoesNothing(t *testing.T) {
	repaired, err := (&RotationRepairState{}).repairWithLimit(context.Background(), nil, nil, 10, time.Now())

	if repaired != 0 || err != nil {
		t.Fatalf("repairWithLimit() = (%d, %v), want (0, nil)", repaired, err)
	}
}
