package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"testing"
)

type rotationStubKey struct {
	userID, provider, hash string
}

type rotationStubVal struct {
	nextHash, encrypted string
}

type rotationStubConnector struct {
	store map[rotationStubKey]rotationStubVal
}

func (c rotationStubConnector) Connect(context.Context) (driver.Conn, error) {
	return rotationStubConn{store: c.store}, nil
}
func (rotationStubConnector) Driver() driver.Driver { return nil }

type rotationStubConn struct {
	store map[rotationStubKey]rotationStubVal
}

func (rotationStubConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (rotationStubConn) Close() error                        { return nil }
func (rotationStubConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (c rotationStubConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "oauth_token_rotations") && len(args) == 5 {
		k := rotationStubKey{
			userID:   args[0].Value.(string),
			provider: args[1].Value.(string),
			hash:     args[2].Value.(string),
		}
		c.store[k] = rotationStubVal{
			nextHash:  args[3].Value.(string),
			encrypted: args[4].Value.(string),
		}
		return driver.RowsAffected(1), nil
	}
	return nil, driver.ErrSkip
}
func (c rotationStubConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "FROM oauth_token_rotations") && len(args) == 3 {
		k := rotationStubKey{
			userID:   args[0].Value.(string),
			provider: args[1].Value.(string),
			hash:     args[2].Value.(string),
		}
		v, ok := c.store[k]
		if !ok {
			return &rotationStubRows{}, nil
		}
		return &rotationStubRows{
			rows: [][]driver.Value{{v.nextHash, v.encrypted}},
		}, nil
	}
	return nil, driver.ErrSkip
}

type rotationStubRows struct {
	rows [][]driver.Value
	pos  int
}

func (rotationStubRows) Columns() []string {
	return []string{"rotated_token_hash", "rotated_refresh_token_encrypted"}
}
func (rotationStubRows) Close() error { return nil }
func (r *rotationStubRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

func TestOAuthTokenRotationTransitiveAndCycleSafe(t *testing.T) {
	store := make(map[rotationStubKey]rotationStubVal)
	database := sql.OpenDB(rotationStubConnector{store: store})
	t.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	hA := OAuthTokenRotationHash("u1", "google", "tok-A")
	hB := OAuthTokenRotationHash("u1", "google", "tok-B")
	hC := OAuthTokenRotationHash("u1", "google", "tok-C")

	if err := RecordOAuthTokenRotation(ctx, database, "u1", "google", hA, hB, "enc-B"); err != nil {
		t.Fatalf("record A->B: %v", err)
	}
	if err := RecordOAuthTokenRotation(ctx, database, "u1", "google", hB, hC, "enc-C"); err != nil {
		t.Fatalf("record B->C: %v", err)
	}

	gotHash, gotEnc, found, err := LookupOAuthTokenRotation(ctx, database, "u1", "google", hA)
	if err != nil || !found || gotHash != hC || gotEnc != "enc-C" {
		t.Fatalf("Lookup(A) = (%q, %q, %v, %v), want (%q, enc-C, true, nil)", gotHash, gotEnc, found, err, hC)
	}

	// Isolation: different user or provider must not see u1/google rotation.
	if _, _, found, err := LookupOAuthTokenRotation(ctx, database, "u2", "google", hA); err != nil || found {
		t.Fatalf("Lookup(u2) = (found=%v, err=%v), want (false, nil)", found, err)
	}

	// Cycle protection: A->B->C->A must terminate cleanly without hanging.
	if err := RecordOAuthTokenRotation(ctx, database, "u1", "google", hC, hA, "enc-A"); err != nil {
		t.Fatalf("record C->A: %v", err)
	}
	_, _, found, err = LookupOAuthTokenRotation(ctx, database, "u1", "google", hA)
	if err != nil || !found {
		t.Fatalf("Lookup(cycle) = (found=%v, err=%v), want (true, nil)", found, err)
	}
}
