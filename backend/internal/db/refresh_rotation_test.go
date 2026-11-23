package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// scriptedRule answers the first statement whose text contains match. A rule
// with no rows makes QueryRow report sql.ErrNoRows.
type scriptedRule struct {
	match   string
	columns []string
	rows    [][]driver.Value
	err     error
}

type scriptedDriver struct {
	rules []scriptedRule
	log   []string                  // BEGIN, statements in order, COMMIT or ROLLBACK
	args  map[string][]driver.Value // bound arguments of the last run of each normalized statement
}

type scriptedConn struct{ d *scriptedDriver }
type scriptedStmt struct {
	c     *scriptedConn
	query string
}
type scriptedTx struct{ d *scriptedDriver }
type scriptedRows struct {
	columns []string
	rows    [][]driver.Value
	pos     int
}

var scriptedDriverSeq atomic.Int64

func (d *scriptedDriver) Open(string) (driver.Conn, error) { return &scriptedConn{d: d}, nil }

func (c *scriptedConn) Prepare(query string) (driver.Stmt, error) {
	return &scriptedStmt{c: c, query: query}, nil
}
func (c *scriptedConn) Close() error { return nil }
func (c *scriptedConn) Begin() (driver.Tx, error) {
	c.d.log = append(c.d.log, "BEGIN")
	return scriptedTx{d: c.d}, nil
}

func (t scriptedTx) Commit() error   { t.d.log = append(t.d.log, "COMMIT"); return nil }
func (t scriptedTx) Rollback() error { t.d.log = append(t.d.log, "ROLLBACK"); return nil }

func (s *scriptedStmt) Close() error  { return nil }
func (s *scriptedStmt) NumInput() int { return -1 }

func (s *scriptedStmt) rule(args []driver.Value) *scriptedRule {
	query := normalizeSQL(s.query)
	s.c.d.log = append(s.c.d.log, query)
	if s.c.d.args == nil {
		s.c.d.args = map[string][]driver.Value{}
	}
	s.c.d.args[query] = args
	// The longest matching rule wins, so a specific statement is not captured by a
	// rule written for a shorter prefix of it.
	var best *scriptedRule
	for i := range s.c.d.rules {
		r := &s.c.d.rules[i]
		if strings.Contains(query, r.match) && (best == nil || len(r.match) > len(best.match)) {
			best = r
		}
	}
	return best
}

func (s *scriptedStmt) Exec(args []driver.Value) (driver.Result, error) {
	if r := s.rule(args); r != nil && r.err != nil {
		return nil, r.err
	}
	return driver.RowsAffected(1), nil
}

func (s *scriptedStmt) Query(args []driver.Value) (driver.Rows, error) {
	r := s.rule(args)
	if r == nil {
		return &scriptedRows{}, nil
	}
	if r.err != nil {
		return nil, r.err
	}
	return &scriptedRows{columns: r.columns, rows: r.rows}, nil
}

func (r *scriptedRows) Columns() []string { return r.columns }
func (r *scriptedRows) Close() error      { return nil }
func (r *scriptedRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

func normalizeSQL(q string) string { return strings.Join(strings.Fields(q), " ") }

func openScriptedDB(t *testing.T, rules ...scriptedRule) (*sql.DB, *scriptedDriver) {
	t.Helper()
	d := &scriptedDriver{rules: rules}
	name := "scripted-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + strconv.FormatInt(scriptedDriverSeq.Add(1), 10)
	sql.Register(name, d)
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open scripted db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, d
}

func rotationUserRow(active bool) scriptedRule {
	now := time.Now()
	return scriptedRule{
		match: "FROM users WHERE id = $1",
		columns: []string{"id", "email", "password_hash", "display_name", "language", "role", "active", "must_change_password", "avatar", "avatar_mime",
			"created_at", "updated_at", "totp_enabled", "totp_secret_enc", "totp_backup_codes", "totp_failed_attempts", "totp_locked_until",
			"login_failed_attempts", "login_locked_until", "last_login_at"},
		rows: [][]driver.Value{{"user-1", "a@example.test", "hash", "A", "en", "USER", active, false, nil, nil,
			now, now, false, nil, []byte("[]"), int64(0), nil, int64(0), nil, nil}},
	}
}

func consumedTokenRow() scriptedRule {
	return scriptedRule{
		match:   "DELETE FROM refresh_tokens WHERE token_hash = $1",
		columns: []string{"user_id", "user_agent"},
		rows:    [][]driver.Value{{"user-1", "Firefox"}},
	}
}

func lockedActiveRow(active bool) scriptedRule {
	return scriptedRule{match: "SELECT active FROM users WHERE id = $1 FOR UPDATE", columns: []string{"active"}, rows: [][]driver.Value{{active}}}
}

func TestRotateRefreshTokenConsumesLocksAndReplaces(t *testing.T) {
	expires := time.Now().Add(7 * 24 * time.Hour)

	t.Run("an active account gets the replacement in the same transaction", func(t *testing.T) {
		database, d := openScriptedDB(t, consumedTokenRow(), rotationUserRow(true), lockedActiveRow(true))

		outcome, user, err := NewAuthTokenStore(database).RotateRefreshToken(context.Background(), "old-hash", "new-hash", expires)

		if err != nil || outcome != RefreshRotated || user == nil || user.ID != "user-1" {
			t.Fatalf("RotateRefreshToken() = (%v, %+v, %v), want a rotation for user-1", outcome, user, err)
		}
		want := []string{"BEGIN", "consume", "load user", "lock", "insert", "COMMIT"}
		if got := summarizeRotationLog(d.log); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("statement order = %v, want %v\nfull log: %v", got, want, d.log)
		}
		for query, args := range d.args {
			switch {
			case strings.HasPrefix(query, "DELETE FROM refresh_tokens WHERE token_hash"):
				if len(args) != 1 || args[0] != driver.Value("old-hash") {
					t.Errorf("consume args = %v, want only the presented token hash", args)
				}
			case strings.HasPrefix(query, "INSERT INTO refresh_tokens"):
				// The replacement keeps the user and user agent of the token it replaces.
				if len(args) != 4 || args[0] != driver.Value("new-hash") || args[1] != driver.Value("user-1") || args[2] != driver.Value("Firefox") {
					t.Errorf("insert args = %v, want [new-hash user-1 Firefox expiry]", args)
				}
			}
		}
	})

	t.Run("a suspended account loses all its tokens and gets no replacement", func(t *testing.T) {
		database, d := openScriptedDB(t, consumedTokenRow(), rotationUserRow(false), lockedActiveRow(false))

		outcome, user, err := NewAuthTokenStore(database).RotateRefreshToken(context.Background(), "old-hash", "new-hash", expires)

		if err != nil || outcome != RefreshUserDisabled || user != nil {
			t.Fatalf("RotateRefreshToken() = (%v, %+v, %v), want RefreshUserDisabled", outcome, user, err)
		}
		want := []string{"BEGIN", "consume", "load user", "lock", "purge user tokens", "COMMIT"}
		if got := summarizeRotationLog(d.log); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("statement order = %v, want %v", got, want)
		}
	})

	t.Run("an unknown or expired token changes nothing", func(t *testing.T) {
		database, d := openScriptedDB(t, scriptedRule{match: "DELETE FROM refresh_tokens WHERE token_hash = $1", columns: []string{"user_id", "user_agent"}})

		outcome, user, err := NewAuthTokenStore(database).RotateRefreshToken(context.Background(), "unknown", "new-hash", expires)

		if err != nil || outcome != RefreshTokenInvalid || user != nil {
			t.Fatalf("RotateRefreshToken() = (%v, %+v, %v), want RefreshTokenInvalid", outcome, user, err)
		}
		if got := d.log[len(d.log)-1]; got != "ROLLBACK" {
			t.Fatalf("last transaction step = %q, want ROLLBACK (log: %v)", got, d.log)
		}
	})

	t.Run("a token whose user is gone is invalid", func(t *testing.T) {
		missingUser := rotationUserRow(true)
		missingUser.rows = nil
		database, _ := openScriptedDB(t, consumedTokenRow(), missingUser)

		outcome, _, err := NewAuthTokenStore(database).RotateRefreshToken(context.Background(), "old-hash", "new-hash", expires)

		if err != nil || outcome != RefreshTokenInvalid {
			t.Fatalf("RotateRefreshToken() = (%v, %v), want RefreshTokenInvalid", outcome, err)
		}
	})

	t.Run("a failed token purge for a suspended account is reported with the disabled outcome", func(t *testing.T) {
		purgeErr := errors.New("deadlock")
		database, _ := openScriptedDB(t,
			consumedTokenRow(), rotationUserRow(false), lockedActiveRow(false),
			scriptedRule{match: "DELETE FROM refresh_tokens WHERE user_id = $1", err: purgeErr},
		)

		outcome, _, err := NewAuthTokenStore(database).RotateRefreshToken(context.Background(), "old-hash", "new-hash", expires)

		if outcome != RefreshUserDisabled || !errors.Is(err, purgeErr) {
			t.Fatalf("RotateRefreshToken() = (%v, %v), want RefreshUserDisabled with the purge error", outcome, err)
		}
	})

	t.Run("a failed insert rolls back and consumes nothing", func(t *testing.T) {
		insertErr := errors.New("disk full")
		database, d := openScriptedDB(t,
			consumedTokenRow(), rotationUserRow(true), lockedActiveRow(true),
			scriptedRule{match: "INSERT INTO refresh_tokens", err: insertErr},
		)

		_, _, err := NewAuthTokenStore(database).RotateRefreshToken(context.Background(), "old-hash", "new-hash", expires)

		if !errors.Is(err, insertErr) {
			t.Fatalf("RotateRefreshToken() error = %v, want %v", err, insertErr)
		}
		if got := d.log[len(d.log)-1]; got != "ROLLBACK" {
			t.Fatalf("last transaction step = %q, want ROLLBACK", got)
		}
	})
}

// summarizeRotationLog maps the recorded SQL to the step names used above.
func summarizeRotationLog(log []string) []string {
	var out []string
	for _, entry := range log {
		switch {
		case entry == "BEGIN" || entry == "COMMIT" || entry == "ROLLBACK":
			out = append(out, entry)
		case strings.HasPrefix(entry, "DELETE FROM refresh_tokens WHERE token_hash"):
			out = append(out, "consume")
		case strings.Contains(entry, "FROM users WHERE id = $1 FOR UPDATE"):
			out = append(out, "lock")
		case strings.Contains(entry, "FROM users WHERE id = $1"):
			out = append(out, "load user")
		case strings.HasPrefix(entry, "DELETE FROM refresh_tokens WHERE user_id"):
			out = append(out, "purge user tokens")
		case strings.HasPrefix(entry, "INSERT INTO refresh_tokens"):
			out = append(out, "insert")
		}
	}
	return out
}
