package oauth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
)

type stubRotationRow struct {
	nextHash, encrypted string
}

type stubRotationDB struct {
	mu                sync.Mutex
	rows              map[string]stubRotationRow
	execErr           error
	execFailUser      string
	execFailErr       error
	queryErr          error
	waitCtxDoneOnExec bool
}

func (s *stubRotationDB) Connect(context.Context) (driver.Conn, error) {
	return &stubRotationConn{db: s}, nil
}
func (*stubRotationDB) Driver() driver.Driver { return nil }

type stubRotationConn struct{ db *stubRotationDB }

func (*stubRotationConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*stubRotationConn) Close() error                        { return nil }
func (*stubRotationConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (c *stubRotationConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "oauth_token_rotations") {
		c.db.mu.Lock()
		wait := c.db.waitCtxDoneOnExec
		c.db.mu.Unlock()
		if wait {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		c.db.mu.Lock()
		defer c.db.mu.Unlock()
		if c.db.execErr != nil {
			return nil, c.db.execErr
		}
		if c.db.execFailUser != "" && fmt.Sprint(args[0].Value) == c.db.execFailUser && c.db.execFailErr != nil {
			return nil, c.db.execFailErr
		}
		if c.db.rows == nil {
			c.db.rows = make(map[string]stubRotationRow)
		}
		k := fmt.Sprintf("%v\x00%v\x00%v", args[0].Value, args[1].Value, args[2].Value)
		if strings.Contains(query, "DO NOTHING") {
			if _, exists := c.db.rows[k]; exists {
				return driver.RowsAffected(0), nil
			}
		}
		c.db.rows[k] = stubRotationRow{
			nextHash:  args[3].Value.(string),
			encrypted: args[4].Value.(string),
		}
		return driver.RowsAffected(1), nil
	}
	return nil, driver.ErrSkip
}
func (c *stubRotationConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "FROM oauth_token_rotations") {
		c.db.mu.Lock()
		defer c.db.mu.Unlock()
		if c.db.queryErr != nil {
			return nil, c.db.queryErr
		}
		k := fmt.Sprintf("%v\x00%v\x00%v", args[0].Value, args[1].Value, args[2].Value)
		v, ok := c.db.rows[k]
		if !ok {
			return &stubRotationRows{}, nil
		}
		return &stubRotationRows{rows: [][]driver.Value{{v.nextHash, v.encrypted}}}, nil
	}
	return nil, driver.ErrSkip
}

type stubRotationRows struct {
	rows [][]driver.Value
	pos  int
}

func (*stubRotationRows) Columns() []string {
	return []string{"rotated_token_hash", "rotated_refresh_token_encrypted"}
}
func (*stubRotationRows) Close() error { return nil }
func (r *stubRotationRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

type trackingTokenLocker struct {
	mu      sync.Mutex
	held    map[string]string
	seq     int
	history []string
}

func (l *trackingTokenLocker) TryClaimOAuthLock(_ context.Context, jobType, jobID, role string, _ time.Duration) (string, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held == nil {
		l.held = make(map[string]string)
	}
	key := jobType + ":" + jobID + ":" + role
	if _, busy := l.held[key]; busy {
		return "", false, nil
	}
	l.seq++
	tok := fmt.Sprintf("lock-%d", l.seq)
	l.held[key] = tok
	l.history = append(l.history, "claim:"+key)
	return tok, true, nil
}

func (l *trackingTokenLocker) ReleaseOAuthLock(_ context.Context, jobType, jobID, role, token string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := jobType + ":" + jobID + ":" + role
	if l.held[key] == token {
		delete(l.held, key)
		l.history = append(l.history, "release:"+key)
	}
	return nil
}

func (l *trackingTokenLocker) isHeld(jobType, jobID, role string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.held[jobType+":"+jobID+":"+role]
	return ok
}

type stubBackupStore struct {
	mu           sync.Mutex
	entries      map[string]stubRotationRow
	recordErr    error
	sawCtxErr    error
	lastTTL      time.Duration
	onBeforeLook func()
}

func (s *stubBackupStore) RecordOAuthRotationBackup(ctx context.Context, userID, provider, prevHash, rotatedHash, encryptedToken string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastTTL = ttl
	if s.sawCtxErr == nil && ctx.Err() != nil {
		s.sawCtxErr = ctx.Err()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.recordErr != nil {
		return s.recordErr
	}
	if s.entries == nil {
		s.entries = make(map[string]stubRotationRow)
	}
	k := fmt.Sprintf("%s\x00%s\x00%s", userID, provider, prevHash)
	s.entries[k] = stubRotationRow{nextHash: rotatedHash, encrypted: encryptedToken}
	return nil
}

func (s *stubBackupStore) LookupOAuthRotationBackup(_ context.Context, userID, provider, tokenHash string) (string, string, bool, error) {
	s.mu.Lock()
	hook := s.onBeforeLook
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := fmt.Sprintf("%s\x00%s\x00%s", userID, provider, tokenHash)
	v, ok := s.entries[k]
	if !ok {
		return "", "", false, nil
	}
	return v.nextHash, v.encrypted, true, nil
}

func (s *stubBackupStore) VisitOAuthRotationBackups(_ context.Context, visit func(userID, provider, prevHash, rotatedHash, encryptedToken string) error) error {
	s.mu.Lock()
	keys := make([]string, 0, len(s.entries))
	snapshot := make(map[string]stubRotationRow, len(s.entries))
	for k, v := range s.entries {
		keys = append(keys, k)
		snapshot[k] = v
	}
	s.mu.Unlock()
	sort.Strings(keys)
	for _, k := range keys {
		v := snapshot[k]
		parts := strings.SplitN(k, "\x00", 3)
		if err := visit(parts[0], parts[1], parts[2], v.nextHash, v.encrypted); err != nil {
			return err
		}
	}
	return nil
}

func (s *stubBackupStore) DeleteOAuthRotationBackup(_ context.Context, userID, provider, prevHash, expectedRotatedHash, expectedEncrypted string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := fmt.Sprintf("%s\x00%s\x00%s", userID, provider, prevHash)
	cur, ok := s.entries[k]
	if !ok || cur.nextHash != expectedRotatedHash || cur.encrypted != expectedEncrypted {
		return false, nil
	}
	delete(s.entries, k)
	return true, nil
}

func TestRefreshWithRotationLockEdgeCases(t *testing.T) {
	const key = "01234567890123456789012345678901"
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)

	t.Run("reloaded token releases initial lock and acquires new lock before refreshing", func(t *testing.T) {
		state := &stubRotationDB{}
		sqlDB := sql.OpenDB(state)
		t.Cleanup(func() { _ = sqlDB.Close() })

		locker := &trackingTokenLocker{}
		hashT1 := db.OAuthTokenRotationHash("u1", "google", "T1")
		hashT2 := db.OAuthTokenRotationHash("u1", "google", "T2")

		reloaded := false
		var refreshedTok string
		var lockHeldDuringRefresh bool

		res, err := RefreshWithRotationLock(context.Background(), RotationRefreshRequest{
			DB:            sqlDB,
			Cache:         NewRotationCache(),
			Locker:        locker,
			EncryptionKey: key,
			UserID:        "u1",
			Provider:      "google",
			RefreshToken:  "T1",
			Now:           func() time.Time { return now },
			OnLocked: func(_ context.Context, current string) (string, bool, string, error) {
				if current == "T1" && !reloaded {
					reloaded = true
					return "", false, "T2", nil
				}
				return "", false, "", nil
			},
			Refresh: func(_ context.Context, _, refreshTok string) (*TokenResponse, error) {
				refreshedTok = refreshTok
				lockHeldDuringRefresh = locker.isHeld("oauth-token", hashT2, "refresh") && !locker.isHeld("oauth-token", hashT1, "refresh")
				return &TokenResponse{AccessToken: "at-3", RefreshToken: "T3", ExpiresIn: 3600}, nil
			},
		})
		if err != nil {
			t.Fatalf("RefreshWithRotationLock() error = %v", err)
		}
		if refreshedTok != "T2" {
			t.Fatalf("refreshed token = %q, want T2", refreshedTok)
		}
		if !lockHeldDuringRefresh {
			t.Fatal("Refresh must run while holding T2's lock and NOT holding T1's lock")
		}
		if res.Token.RefreshToken != "T3" {
			t.Fatalf("result refresh = %q, want T3", res.Token.RefreshToken)
		}
	})

	t.Run("rotation history DB write failure still persists replacement elsewhere and caches fallback while failing", func(t *testing.T) {
		dbErr := errors.New("pq: permission denied for table oauth_token_rotations")
		state := &stubRotationDB{execErr: dbErr}
		sqlDB := sql.OpenDB(state)
		t.Cleanup(func() { _ = sqlDB.Close() })

		cache := NewRotationCache()
		var persistedTok string
		_, err := RefreshWithRotationLock(context.Background(), RotationRefreshRequest{
			DB:            sqlDB,
			Cache:         cache,
			EncryptionKey: key,
			UserID:        "u1",
			Provider:      "google",
			RefreshToken:  "T1",
			Now:           func() time.Time { return now },
			Refresh: func(context.Context, string, string) (*TokenResponse, error) {
				return &TokenResponse{AccessToken: "at-2", RefreshToken: "T2", ExpiresIn: 3600}, nil
			},
			Persist: func(_ context.Context, _ string, resp *TokenResponse) (string, bool, error) {
				persistedTok = resp.RefreshToken
				return "", false, nil
			},
		})
		if !errors.Is(err, dbErr) {
			t.Fatalf("RefreshWithRotationLock() error = %v, want %v", err, dbErr)
		}
		if persistedTok != "T2" {
			t.Fatalf("Persist did not run on history write failure: persistedTok=%q, want T2", persistedTok)
		}
		if got, ok, cerr := cache.Lookup("u1", "google", "T1", key, now); cerr != nil || !ok || got != "T2" {
			t.Fatalf("cache.Lookup(T1) = (%q, %v, %v), want (T2, true, nil)", got, ok, cerr)
		}
	})

	t.Run("history lookup and decryption errors abort instead of acting as absent history", func(t *testing.T) {
		lookupErr := errors.New("pq: connection refused")
		state := &stubRotationDB{queryErr: lookupErr}
		sqlDB := sql.OpenDB(state)
		t.Cleanup(func() { _ = sqlDB.Close() })

		refreshCalled := false
		_, err := RefreshWithRotationLock(context.Background(), RotationRefreshRequest{
			DB:            sqlDB,
			Cache:         NewRotationCache(),
			EncryptionKey: key,
			UserID:        "u1",
			Provider:      "google",
			RefreshToken:  "T1",
			Now:           func() time.Time { return now },
			Refresh: func(context.Context, string, string) (*TokenResponse, error) {
				refreshCalled = true
				return &TokenResponse{AccessToken: "at-2", RefreshToken: "T2", ExpiresIn: 3600}, nil
			},
		})
		if !errors.Is(err, lookupErr) {
			t.Fatalf("RefreshWithRotationLock(lookupErr) error = %v, want %v", err, lookupErr)
		}
		if refreshCalled {
			t.Fatal("Refresh must not be called when rotation history lookup fails")
		}

		// Corrupt ciphertext in DB must also abort rather than submitting stale T1.
		h1 := db.OAuthTokenRotationHash("u1", "google", "T1")
		h2 := db.OAuthTokenRotationHash("u1", "google", "T2")
		corruptState := &stubRotationDB{
			rows: map[string]stubRotationRow{
				fmt.Sprintf("u1\x00google\x00%s", h1): {nextHash: h2, encrypted: "not-valid-ciphertext"},
			},
		}
		corruptDB := sql.OpenDB(corruptState)
		t.Cleanup(func() { _ = corruptDB.Close() })

		refreshCalled = false
		_, err = RefreshWithRotationLock(context.Background(), RotationRefreshRequest{
			DB:            corruptDB,
			Cache:         NewRotationCache(),
			EncryptionKey: key,
			UserID:        "u1",
			Provider:      "google",
			RefreshToken:  "T1",
			Now:           func() time.Time { return now },
			Refresh: func(context.Context, string, string) (*TokenResponse, error) {
				refreshCalled = true
				return &TokenResponse{AccessToken: "at-2", RefreshToken: "T2", ExpiresIn: 3600}, nil
			},
		})
		if !errors.Is(err, ErrRotationPersistence) {
			t.Fatalf("RefreshWithRotationLock(corrupt) error = %v, want ErrRotationPersistence", err)
		}
		if refreshCalled {
			t.Fatal("Refresh must not be called when rotation ciphertext fails to decrypt")
		}
	})

	t.Run("lock wait budget exceeds pre-refresh, refresh, and persist holder budget", func(t *testing.T) {
		maxWorkBudget := tokenPreRefreshTimeout + tokenRefreshTimeout + tokenPersistTimeout
		if tokenLockWaitTimeout <= maxWorkBudget {
			t.Fatalf("tokenLockWaitTimeout = %v, must exceed holder work budget %v", tokenLockWaitTimeout, maxWorkBudget)
		}
		if tokenLockTTL < maxWorkBudget+tokenLockSafetyMargin {
			t.Fatalf("tokenLockTTL = %v, must fit holder work budget + safety margin (%v)", tokenLockTTL, maxWorkBudget+tokenLockSafetyMargin)
		}
	})

	t.Run("partial database chain continues through newer cached hop", func(t *testing.T) {
		encB, err := crypto.EncryptWithDomain("B", key, crypto.DomainOAuthRefreshToken)
		if err != nil {
			t.Fatalf("Encrypt(B) error = %v", err)
		}
		encC, err := crypto.EncryptWithDomain("C", key, crypto.DomainOAuthRefreshToken)
		if err != nil {
			t.Fatalf("Encrypt(C) error = %v", err)
		}
		hashA := db.OAuthTokenRotationHash("u1", "google", "A")
		hashB := db.OAuthTokenRotationHash("u1", "google", "B")
		hashC := db.OAuthTokenRotationHash("u1", "google", "C")

		state := &stubRotationDB{
			rows: map[string]stubRotationRow{
				fmt.Sprintf("u1\x00google\x00%s", hashA): {nextHash: hashB, encrypted: encB},
			},
		}
		sqlDB := sql.OpenDB(state)
		t.Cleanup(func() { _ = sqlDB.Close() })

		cache := NewRotationCache()
		cache.RecordEncrypted(hashB, hashC, encC, now)

		got, ok, err := LookupRotatedRefreshToken(context.Background(), sqlDB, cache, key, "u1", "google", "A", now)
		if err != nil || !ok || got != "C" {
			t.Fatalf("LookupRotatedRefreshToken(A) = (%q, %v, %v), want (C, true, nil)", got, ok, err)
		}
	})

	t.Run("failed DB history write recovers across instances via shared backup store", func(t *testing.T) {
		dbErr := errors.New("pq: permission denied for table oauth_token_rotations")
		state := &stubRotationDB{execErr: dbErr}
		sqlDB := sql.OpenDB(state)
		t.Cleanup(func() { _ = sqlDB.Close() })

		sharedBackup := &stubBackupStore{}
		_, err := RefreshWithRotationLock(context.Background(), RotationRefreshRequest{
			DB:            sqlDB,
			BackupStore:   sharedBackup,
			Cache:         NewRotationCache(),
			EncryptionKey: key,
			UserID:        "u1",
			Provider:      "google",
			RefreshToken:  "T1",
			Now:           func() time.Time { return now },
			Refresh: func(context.Context, string, string) (*TokenResponse, error) {
				return &TokenResponse{AccessToken: "at-2", RefreshToken: "T2", ExpiresIn: 3600}, nil
			},
		})
		if !errors.Is(err, dbErr) {
			t.Fatalf("first instance error = %v, want %v", err, dbErr)
		}

		// Simulate a different API/worker instance with an empty local cache and healthy DB writes.
		state.mu.Lock()
		state.execErr = nil
		state.mu.Unlock()
		var secondUsed string
		res2, err := RefreshWithRotationLock(context.Background(), RotationRefreshRequest{
			DB:            sqlDB,
			BackupStore:   sharedBackup,
			Cache:         NewRotationCache(),
			EncryptionKey: key,
			UserID:        "u1",
			Provider:      "google",
			RefreshToken:  "T1",
			Now:           func() time.Time { return now },
			Refresh: func(_ context.Context, _, tok string) (*TokenResponse, error) {
				secondUsed = tok
				return &TokenResponse{AccessToken: "at-3", RefreshToken: "T3", ExpiresIn: 3600}, nil
			},
		})
		if err != nil {
			t.Fatalf("second instance error = %v", err)
		}
		if secondUsed != "T2" || res2.Token.RefreshToken != "T3" {
			t.Fatalf("second instance refreshed (%q -> %q), want (T2 -> T3)", secondUsed, res2.Token.RefreshToken)
		}
	})

	t.Run("persist callback receives bounded deadline strictly inside lock TTL", func(t *testing.T) {
		var hasDeadline bool
		var deadlineRemaining time.Duration
		locker := &trackingTokenLocker{}
		_, err := RefreshWithRotationLock(context.Background(), RotationRefreshRequest{
			Locker:        locker,
			Cache:         NewRotationCache(),
			EncryptionKey: key,
			UserID:        "u1",
			Provider:      "google",
			RefreshToken:  "T1",
			Now:           func() time.Time { return now },
			Refresh: func(context.Context, string, string) (*TokenResponse, error) {
				return &TokenResponse{AccessToken: "at-2", RefreshToken: "T2", ExpiresIn: 3600}, nil
			},
			Persist: func(persistCtx context.Context, _ string, _ *TokenResponse) (string, bool, error) {
				dl, ok := persistCtx.Deadline()
				hasDeadline = ok
				deadlineRemaining = time.Until(dl)
				return "", false, nil
			},
		})
		if err != nil {
			t.Fatalf("RefreshWithRotationLock() error = %v", err)
		}
		if !hasDeadline || deadlineRemaining <= 0 || deadlineRemaining >= tokenLockTTL {
			t.Fatalf("persistCtx deadline=(%v, remaining=%v), want 0 < remaining < %v", hasDeadline, deadlineRemaining, tokenLockTTL)
		}
	})

	t.Run("database timeout still records backup with independent context and surfaces backup errors", func(t *testing.T) {
		state := &stubRotationDB{waitCtxDoneOnExec: true}
		sqlDB := sql.OpenDB(state)
		t.Cleanup(func() { _ = sqlDB.Close() })

		sharedBackup := &stubBackupStore{}
		shortCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()

		err := RecordRotatedRefreshTokenWithStore(shortCtx, sqlDB, sharedBackup, NewRotationCache(), key, "u1", "google", "T2", now, "T1")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("RecordRotatedRefreshTokenWithStore() error = %v, want context.DeadlineExceeded", err)
		}
		if sharedBackup.sawCtxErr != nil {
			t.Fatalf("backup saw cancelled context error = %v, want nil", sharedBackup.sawCtxErr)
		}
		got, ok, lerr := LookupRotatedRefreshTokenWithStore(context.Background(), nil, sharedBackup, NewRotationCache(), key, "u1", "google", "T1", now)
		if lerr != nil || !ok || got != "T2" {
			t.Fatalf("backup lookup after DB timeout = (%q, %v, %v), want (T2, true, nil)", got, ok, lerr)
		}

		// When backup itself fails, the error must not be swallowed.
		backupErr := errors.New("redis unavailable")
		failingBackup := &stubBackupStore{recordErr: backupErr}
		err = RecordRotatedRefreshTokenWithStore(context.Background(), nil, failingBackup, NewRotationCache(), key, "u1", "google", "T2", now, "T1")
		if !errors.Is(err, backupErr) {
			t.Fatalf("RecordRotatedRefreshTokenWithStore(backup failure) error = %v, want %v", err, backupErr)
		}
	})

	t.Run("pre-refresh OnLocked and refresh contexts are bounded inside remaining lock lease", func(t *testing.T) {
		var preHasDeadline, refreshHasDeadline bool
		var preRemaining, refreshRemaining time.Duration
		locker := &trackingTokenLocker{}

		_, err := RefreshWithRotationLock(context.Background(), RotationRefreshRequest{
			Locker:        locker,
			Cache:         NewRotationCache(),
			EncryptionKey: key,
			UserID:        "u1",
			Provider:      "google",
			RefreshToken:  "T1",
			Now:           func() time.Time { return now },
			OnLocked: func(preCtx context.Context, _ string) (string, bool, string, error) {
				dl, ok := preCtx.Deadline()
				preHasDeadline = ok
				preRemaining = time.Until(dl)
				return "", false, "", nil
			},
			Refresh: func(refreshCtx context.Context, _, _ string) (*TokenResponse, error) {
				dl, ok := refreshCtx.Deadline()
				refreshHasDeadline = ok
				refreshRemaining = time.Until(dl)
				return &TokenResponse{AccessToken: "at-2", RefreshToken: "T2", ExpiresIn: 3600}, nil
			},
		})
		if err != nil {
			t.Fatalf("RefreshWithRotationLock() error = %v", err)
		}
		if !preHasDeadline || preRemaining <= 0 || preRemaining > tokenPreRefreshTimeout {
			t.Fatalf("preRefreshCtx deadline=(%v, remaining=%v), want 0 < remaining <= %v", preHasDeadline, preRemaining, tokenPreRefreshTimeout)
		}
		if !refreshHasDeadline || refreshRemaining <= 0 || refreshRemaining > tokenRefreshTimeout {
			t.Fatalf("refreshCtx deadline=(%v, remaining=%v), want 0 < remaining <= %v", refreshHasDeadline, refreshRemaining, tokenRefreshTimeout)
		}
	})

	t.Run("backup-only rotation entry is repaired into postgresql on read", func(t *testing.T) {
		state := &stubRotationDB{}
		sqlDB := sql.OpenDB(state)
		t.Cleanup(func() { _ = sqlDB.Close() })

		sharedBackup := &stubBackupStore{}
		// Record only into backup (simulating prior PostgreSQL failure).
		if err := RecordRotatedRefreshTokenWithStore(context.Background(), nil, sharedBackup, nil, key, "u1", "google", "T2", now, "T1"); err != nil {
			t.Fatalf("setup backup record error = %v", err)
		}

		// Reading with DB + backup should resolve T2 and repair PostgreSQL.
		got, ok, err := LookupRotatedRefreshTokenWithStore(context.Background(), sqlDB, sharedBackup, nil, key, "u1", "google", "T1", now)
		if err != nil || !ok || got != "T2" {
			t.Fatalf("LookupRotatedRefreshTokenWithStore(repair) = (%q, %v, %v), want (T2, true, nil)", got, ok, err)
		}

		// Clearing backup proves PostgreSQL now holds T1 -> T2 durably.
		gotDB, okDB, errDB := LookupRotatedRefreshToken(context.Background(), sqlDB, nil, key, "u1", "google", "T1", now)
		if errDB != nil || !okDB || gotDB != "T2" {
			t.Fatalf("LookupRotatedRefreshToken(after repair, no backup) = (%q, %v, %v), want (T2, true, nil)", gotDB, okDB, errDB)
		}
		if len(sharedBackup.entries) != 0 {
			t.Fatalf("backup entries remaining after read repair = %d, want 0", len(sharedBackup.entries))
		}
	})

	t.Run("backup lookup surfaces repair failure and proactive sweep repairs dormant entries without expiry", func(t *testing.T) {
		repairErr := errors.New("pq: disk full")
		state := &stubRotationDB{execErr: repairErr}
		sqlDB := sql.OpenDB(state)
		t.Cleanup(func() { _ = sqlDB.Close() })

		sharedBackup := &stubBackupStore{}
		if err := RecordRotatedRefreshTokenWithStore(context.Background(), sqlDB, sharedBackup, nil, key, "u1", "google", "T2", now, "T1"); !errors.Is(err, repairErr) {
			t.Fatalf("setup RecordRotatedRefreshTokenWithStore error = %v, want %v", err, repairErr)
		}
		if sharedBackup.lastTTL != 0 {
			t.Fatalf("backup TTL = %v, want 0 (persistent until repaired)", sharedBackup.lastTTL)
		}

		// While PostgreSQL writes fail, read repair must surface ErrRotationPersistence rather than ignoring the error.
		if _, _, err := LookupRotatedRefreshTokenWithStore(context.Background(), sqlDB, sharedBackup, nil, key, "u1", "google", "T1", now); !errors.Is(err, repairErr) {
			t.Fatalf("LookupRotatedRefreshTokenWithStore(failing repair) error = %v, want %v", err, repairErr)
		}
		if repaired, err := RepairRotationBackups(context.Background(), sqlDB, sharedBackup); !errors.Is(err, repairErr) || repaired != 0 {
			t.Fatalf("RepairRotationBackups(failing) = (%d, %v), want (0, %v)", repaired, err, repairErr)
		}
		if len(sharedBackup.entries) != 1 {
			t.Fatalf("backup entries kept on failed repair = %d, want 1", len(sharedBackup.entries))
		}

		// Once PostgreSQL recovers, proactive sweep repairs dormant entries even if never looked up.
		state.mu.Lock()
		state.execErr = nil
		state.mu.Unlock()
		repaired, err := RepairRotationBackups(context.Background(), sqlDB, sharedBackup)
		if err != nil || repaired != 1 {
			t.Fatalf("RepairRotationBackups(recovered) = (%d, %v), want (1, nil)", repaired, err)
		}
		if len(sharedBackup.entries) != 0 {
			t.Fatalf("backup entries remaining after proactive sweep = %d, want 0", len(sharedBackup.entries))
		}
		gotDB, okDB, errDB := LookupRotatedRefreshToken(context.Background(), sqlDB, nil, key, "u1", "google", "T1", now)
		if errDB != nil || !okDB || gotDB != "T2" {
			t.Fatalf("LookupRotatedRefreshToken(after sweep) = (%q, %v, %v), want (T2, true, nil)", gotDB, okDB, errDB)
		}
	})

	t.Run("concurrent move from backup to postgresql is not missed by lookup", func(t *testing.T) {
		state := &stubRotationDB{}
		sqlDB := sql.OpenDB(state)
		t.Cleanup(func() { _ = sqlDB.Close() })

		sharedBackup := &stubBackupStore{}
		if err := RecordRotatedRefreshTokenWithStore(context.Background(), nil, sharedBackup, nil, key, "u1", "google", "T2", now, "T1"); err != nil {
			t.Fatalf("setup backup record error = %v", err)
		}

		// Simulate repair committing T1 -> T2 to DB and deleting it from Redis right
		// after lookup missed DB and right before lookup checks Redis.
		sharedBackup.onBeforeLook = func() {
			sharedBackup.mu.Lock()
			sharedBackup.onBeforeLook = nil
			sharedBackup.mu.Unlock()
			_, _ = RepairRotationBackups(context.Background(), sqlDB, sharedBackup)
		}

		got, ok, err := LookupRotatedRefreshTokenWithStore(context.Background(), sqlDB, sharedBackup, nil, key, "u1", "google", "T1", now)
		if err != nil || !ok || got != "T2" {
			t.Fatalf("LookupRotatedRefreshTokenWithStore(race) = (%q, %v, %v), want (T2, true, nil)", got, ok, err)
		}
	})

	t.Run("repair compare-and-delete and ON CONFLICT DO NOTHING preserve newer rotations and respect batch limits", func(t *testing.T) {
		state := &stubRotationDB{}
		sqlDB := sql.OpenDB(state)
		t.Cleanup(func() { _ = sqlDB.Close() })

		sharedBackup := &stubBackupStore{}
		_ = RecordRotatedRefreshTokenWithStore(context.Background(), nil, sharedBackup, nil, key, "u1", "google", "B", now, "A")
		_ = RecordRotatedRefreshTokenWithStore(context.Background(), nil, sharedBackup, nil, key, "u1", "google", "Y", now, "X")

		// Pre-populate PostgreSQL with newer rotation B -> C (with alias A -> C) before stale
		// A -> B repair arrives, and ensure DeleteOAuthRotationBackup refuses mismatches.
		if err := RecordRotatedRefreshToken(context.Background(), sqlDB, nil, key, "u1", "google", "C", now, "B", "A"); err != nil {
			t.Fatalf("Record(A->C) error = %v", err)
		}
		deleted, derr := sharedBackup.DeleteOAuthRotationBackup(context.Background(), "u1", "google", db.OAuthTokenRotationHash("u1", "google", "A"), "wrong-hash", "wrong-enc")
		if derr != nil || deleted {
			t.Fatalf("DeleteOAuthRotationBackup(mismatch) = (%v, %v), want (false, nil)", deleted, derr)
		}

		// Batch limit = 1 repairs at most one backup per pass without overwriting A -> C.
		repaired, err := RepairRotationBackupsWithLimit(context.Background(), sqlDB, sharedBackup, 1)
		if err != nil || repaired != 1 {
			t.Fatalf("RepairRotationBackupsWithLimit(1) = (%d, %v), want (1, nil)", repaired, err)
		}
		if len(sharedBackup.entries) != 1 {
			t.Fatalf("remaining entries after batch=1 = %d, want 1", len(sharedBackup.entries))
		}
		repaired2, err := RepairRotationBackupsWithLimit(context.Background(), sqlDB, sharedBackup, 1)
		if err != nil || repaired2 != 1 {
			t.Fatalf("second batch = (%d, %v), want (1, nil)", repaired2, err)
		}
		// A -> C in PostgreSQL must remain C, not overwritten by stale A -> B!
		gotA, okA, errA := LookupRotatedRefreshToken(context.Background(), sqlDB, nil, key, "u1", "google", "A", now)
		if errA != nil || !okA || gotA != "C" {
			t.Fatalf("Lookup(A) after stale repair = (%q, %v, %v), want (C, true, nil)", gotA, okA, errA)
		}
	})

	t.Run("repair preserves newer backup when postgresql has older row and lookup still resolves newer token", func(t *testing.T) {
		state := &stubRotationDB{}
		sqlDB := sql.OpenDB(state)
		t.Cleanup(func() { _ = sqlDB.Close() })

		// PostgreSQL holds older A -> B; Redis holds newer A -> C (while B -> C failed).
		if err := RecordRotatedRefreshToken(context.Background(), sqlDB, nil, key, "u1", "google", "B", now, "A"); err != nil {
			t.Fatalf("Record(A->B) error = %v", err)
		}
		sharedBackup := &stubBackupStore{}
		if err := RecordRotatedRefreshTokenWithStore(context.Background(), nil, sharedBackup, nil, key, "u1", "google", "C", now, "A"); err != nil {
			t.Fatalf("BackupRecord(A->C) error = %v", err)
		}

		repairState := NewRotationRepairState()
		repaired, err := repairState.RepairWithLimit(context.Background(), sqlDB, sharedBackup, 10, now)
		if err != nil || repaired != 0 {
			t.Fatalf("RepairWithLimit() = (%d, %v), want (0, nil)", repaired, err)
		}
		if len(sharedBackup.entries) != 1 {
			t.Fatalf("backup entries after diverging DB repair = %d, want 1 (A->C preserved)", len(sharedBackup.entries))
		}

		gotA, okA, errA := LookupRotatedRefreshTokenWithStore(context.Background(), sqlDB, sharedBackup, nil, key, "u1", "google", "A", now)
		if errA != nil || !okA || gotA != "C" {
			t.Fatalf("Lookup(A) with DB(A->B) + backup(A->C) = (%q, %v, %v), want (C, true, nil)", gotA, okA, errA)
		}
		if len(sharedBackup.entries) != 1 {
			t.Fatalf("backup entries after lookup = %d, want 1 (A->C preserved)", len(sharedBackup.entries))
		}

		// Even if PostgreSQL also records an unrelated successor C -> D (with B -> C
		// still missing), C is NOT reachable from A in PostgreSQL, so repair must NOT
		// delete A -> C and lookup must still resolve A -> C -> D.
		if err := RecordRotatedRefreshToken(context.Background(), sqlDB, nil, key, "u1", "google", "D", now, "C"); err != nil {
			t.Fatalf("Record(C->D) error = %v", err)
		}
		repaired, err = repairState.RepairWithLimit(context.Background(), sqlDB, sharedBackup, 10, now.Add(repairFailureCooldown+time.Second))
		if err != nil || repaired != 0 {
			t.Fatalf("RepairWithLimit(C->D present) = (%d, %v), want (0, nil)", repaired, err)
		}
		if len(sharedBackup.entries) != 1 {
			t.Fatalf("backup entries after disjoint C->D repair = %d, want 1 (A->C preserved)", len(sharedBackup.entries))
		}
		gotD, okD, errD := LookupRotatedRefreshTokenWithStore(context.Background(), sqlDB, sharedBackup, nil, key, "u1", "google", "A", now)
		if errD != nil || !okD || gotD != "D" {
			t.Fatalf("Lookup(A) with DB(A->B, C->D) + backup(A->C) = (%q, %v, %v), want (D, true, nil)", gotD, okD, errD)
		}
	})

	t.Run("repeatedly failing repair entries do not starve later valid entries", func(t *testing.T) {
		if repairFailureCooldown <= 5*time.Minute {
			t.Fatalf("repairFailureCooldown = %v, must exceed 5m daemon sweep interval", repairFailureCooldown)
		}
		fkErr := errors.New("pq: insert or update on table oauth_token_rotations violates foreign key constraint")
		state := &stubRotationDB{execFailUser: "deleted-user", execFailErr: fkErr}
		sqlDB := sql.OpenDB(state)
		t.Cleanup(func() { _ = sqlDB.Close() })

		sharedBackup := &stubBackupStore{}
		// Simulate a multi-batch backlog: 150 failing entries sorted strictly ahead of
		// a healthy entry across consecutive 5-minute sweeps with batch limit = 50.
		// Even after pass 3 crosses the 15-minute cooldown window, the healthy entry must
		// still be prioritized and repaired rather than starved by entries 0..49.
		for i := 0; i < 150; i++ {
			_ = RecordRotatedRefreshTokenWithStore(context.Background(), nil, sharedBackup, nil, key, "deleted-user", "google", fmt.Sprintf("B-%03d", i), now, fmt.Sprintf("A-%03d", i))
		}
		_ = RecordRotatedRefreshTokenWithStore(context.Background(), nil, sharedBackup, nil, key, "healthy-user", "google", "B-ok", now, "A-ok")

		repairState := NewRotationRepairState()
		sawHealthyRepaired := false
		for pass := 0; pass < 4; pass++ {
			sweepNow := now.Add(time.Duration(pass) * 5 * time.Minute)
			repaired, _ := repairState.RepairWithLimit(context.Background(), sqlDB, sharedBackup, 50, sweepNow)
			if repaired > 0 {
				sawHealthyRepaired = true
				break
			}
		}
		if !sawHealthyRepaired {
			t.Fatal("later valid entry remained starved behind 150 failing entries across 5m sweeps")
		}
		gotHealthy, okHealthy, errHealthy := LookupRotatedRefreshToken(context.Background(), sqlDB, nil, key, "healthy-user", "google", "A-ok", now)
		if errHealthy != nil || !okHealthy || gotHealthy != "B-ok" {
			t.Fatalf("Lookup(healthy-user) = (%q, %v, %v), want (B-ok, true, nil)", gotHealthy, okHealthy, errHealthy)
		}

		// Also verify fair rotation among previously-failed entries when a transient
		// outage hit 120 entries and later only healthy-user recovers while 119 still fail.
		for i := 0; i < 119; i++ {
			_ = RecordRotatedRefreshTokenWithStore(context.Background(), nil, sharedBackup, nil, key, "deleted-user", "google", fmt.Sprintf("C-%03d", i), now, fmt.Sprintf("D-%03d", i))
		}
		_ = RecordRotatedRefreshTokenWithStore(context.Background(), nil, sharedBackup, nil, key, "healthy-user", "google", "B-transient", now, "A-transient")
		state.mu.Lock()
		state.execErr = fkErr
		state.mu.Unlock()
		transientState := NewRotationRepairState()
		// Fail all entries across initial sweeps at t=0, 5m, 10m so healthy-user has failed once too.
		for pass := 0; pass < 3; pass++ {
			_, _ = transientState.RepairWithLimit(context.Background(), sqlDB, sharedBackup, 50, now.Add(time.Duration(pass)*5*time.Minute))
		}
		// Now healthy-user starts succeeding while deleted-user entries keep failing.
		state.mu.Lock()
		state.execErr = nil
		state.mu.Unlock()
		recovered := false
		for pass := 3; pass < 12; pass++ {
			if rep, _ := transientState.RepairWithLimit(context.Background(), sqlDB, sharedBackup, 50, now.Add(time.Duration(pass)*5*time.Minute)); rep > 0 {
				recovered = true
				break
			}
		}
		if !recovered {
			t.Fatal("previously-failed healthy entry stayed starved behind 119 permanently-failing entries")
		}

		// Exceed repairCooldownMaxSize (4,096) with failing entries strictly ahead of
		// a healthy entry in sorted scan order. Even after earlier failure records are
		// evicted from the cooldown tracker, the persistent scan anchor must reach and
		// repair the healthy entry instead of restarting from the evicted head.
		overCapCount := repairCooldownMaxSize + 150
		for i := 0; i < overCapCount; i++ {
			_ = RecordRotatedRefreshTokenWithStore(context.Background(), nil, sharedBackup, nil, key, "deleted-user", "google", fmt.Sprintf("F-%04d", i), now, fmt.Sprintf("E-%04d", i))
		}
		_ = RecordRotatedRefreshTokenWithStore(context.Background(), nil, sharedBackup, nil, key, "healthy-user", "google", "B-overcap", now, "A-overcap")

		overCapState := NewRotationRepairState()
		repairedOverCap := false
		for pass := 0; pass < 120; pass++ {
			sweepNow := now.Add(time.Duration(pass) * 5 * time.Minute)
			if rep, _ := overCapState.RepairWithLimit(context.Background(), sqlDB, sharedBackup, 50, sweepNow); rep > 0 {
				repairedOverCap = true
				break
			}
		}
		if !repairedOverCap {
			t.Fatalf("healthy entry remained starved behind %d (> repairCooldownMaxSize=%d) failing entries", overCapCount, repairCooldownMaxSize)
		}
		gotOverCap, okOverCap, errOverCap := LookupRotatedRefreshToken(context.Background(), sqlDB, nil, key, "healthy-user", "google", "A-overcap", now)
		if errOverCap != nil || !okOverCap || gotOverCap != "B-overcap" {
			t.Fatalf("Lookup(healthy-user overcap) = (%q, %v, %v), want (B-overcap, true, nil)", gotOverCap, okOverCap, errOverCap)
		}
	})
}
