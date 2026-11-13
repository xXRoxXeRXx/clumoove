package oauth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/db"
)

type stubRotationRow struct {
	nextHash, encrypted string
}

type stubRotationDB struct {
	mu      sync.Mutex
	rows    map[string]stubRotationRow
	execErr error
}

func (s *stubRotationDB) Connect(context.Context) (driver.Conn, error) {
	return &stubRotationConn{db: s}, nil
}
func (*stubRotationDB) Driver() driver.Driver { return nil }

type stubRotationConn struct{ db *stubRotationDB }

func (*stubRotationConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*stubRotationConn) Close() error                        { return nil }
func (*stubRotationConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (c *stubRotationConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "oauth_token_rotations") {
		c.db.mu.Lock()
		defer c.db.mu.Unlock()
		if c.db.execErr != nil {
			return nil, c.db.execErr
		}
		if c.db.rows == nil {
			c.db.rows = make(map[string]stubRotationRow)
		}
		k := fmt.Sprintf("%v\x00%v\x00%v", args[0].Value, args[1].Value, args[2].Value)
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

	t.Run("rotation history DB write failure aborts loudly instead of silently caching", func(t *testing.T) {
		dbErr := errors.New("db write failed")
		state := &stubRotationDB{execErr: dbErr}
		sqlDB := sql.OpenDB(state)
		t.Cleanup(func() { _ = sqlDB.Close() })

		cache := NewRotationCache()
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
		})
		if !errors.Is(err, dbErr) {
			t.Fatalf("RefreshWithRotationLock() error = %v, want %v", err, dbErr)
		}
		if _, ok := cache.Lookup("u1", "google", "T1", key, now); ok {
			t.Fatal("cache must not record an unpersisted DB rotation on error")
		}
	})
}
