package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// setupTokenSinkDB creates temp tables carrying only the columns the token sinks
// touch, one per owner type, so every adapter runs against a real PostgreSQL
// UPDATE with the same CAS semantics as production.
func setupTokenSinkDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping OAuth token sink contract test")
	}
	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		database.Close()
		t.Fatalf("ping db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	const credentials = `
		source_password_encrypted TEXT,
		source_refresh_token_encrypted TEXT,
		source_token_expires_at TIMESTAMP WITH TIME ZONE,
		target_password_encrypted TEXT,
		target_refresh_token_encrypted TEXT,
		target_token_expires_at TIMESTAMP WITH TIME ZONE,
		updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP`
	if _, err := database.Exec(`
		CREATE TEMP TABLE migrations (id TEXT PRIMARY KEY, ` + credentials + `);
		CREATE TEMP TABLE sync_jobs (id TEXT PRIMARY KEY, ` + credentials + `);
		CREATE TEMP TABLE backup_jobs (id TEXT PRIMARY KEY, deletion_state TEXT NOT NULL DEFAULT 'ACTIVE', ` + credentials + `);
		CREATE TEMP TABLE restore_runs (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL DEFAULT 'RUNNING',
			target_password_encrypted TEXT,
			target_refresh_token_encrypted TEXT,
			target_token_expires_at TIMESTAMP WITH TIME ZONE
		);
		CREATE TEMP TABLE connection_profiles (
			id TEXT PRIMARY KEY,
			password_encrypted TEXT,
			refresh_token_encrypted TEXT,
			token_expires_at TIMESTAMP WITH TIME ZONE,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
		);`); err != nil {
		t.Fatalf("create temp tables: %v", err)
	}
	return database
}

type tokenSinkOwner struct {
	name string
	// seed inserts the owner row holding the "old" tokens in every credential slot.
	seed func(t *testing.T, database *sql.DB, access, refresh string, expires time.Time)
	// sink builds the adapter for the seeded row.
	sink func(database *sql.DB) OAuthTokenSink
	// read returns the tokens stored in the slot the sink rotates.
	read func(t *testing.T, database *sql.DB) (access, refresh string)
	// readOther returns the tokens stored in a slot the sink must never touch, or
	// ok=false when the owner has only one credential slot.
	readOther func(t *testing.T, database *sql.DB) (access, refresh string, ok bool)
}

func seedCredentialRow(table string) func(t *testing.T, database *sql.DB, access, refresh string, expires time.Time) {
	return func(t *testing.T, database *sql.DB, access, refresh string, expires time.Time) {
		t.Helper()
		query := `INSERT INTO ` + table + ` (id, source_password_encrypted, source_refresh_token_encrypted, source_token_expires_at, target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at) VALUES ('owner-1', $1, $2, $3, $1, $2, $3)`
		if _, err := database.Exec(query, access, refresh, expires); err != nil {
			t.Fatalf("seed %s: %v", table, err)
		}
	}
}

func readCredentialSlot(table, role string) func(t *testing.T, database *sql.DB) (string, string) {
	return func(t *testing.T, database *sql.DB) (string, string) {
		t.Helper()
		var access, refresh string
		query := `SELECT ` + role + `_password_encrypted, ` + role + `_refresh_token_encrypted FROM ` + table + ` WHERE id = 'owner-1'`
		if err := database.QueryRow(query).Scan(&access, &refresh); err != nil {
			t.Fatalf("read %s %s: %v", table, role, err)
		}
		return access, refresh
	}
}

func tokenSinkOwners() []tokenSinkOwner {
	var owners []tokenSinkOwner
	for _, role := range []string{"source", "target"} {
		other := "target"
		if role == "target" {
			other = "source"
		}
		role, other := role, other
		for _, table := range []string{"migrations", "sync_jobs", "backup_jobs"} {
			table := table
			owner := tokenSinkOwner{
				name: table + "/" + role,
				seed: seedCredentialRow(table),
				read: readCredentialSlot(table, role),
				readOther: func(t *testing.T, database *sql.DB) (string, string, bool) {
					access, refresh := readCredentialSlot(table, other)(t, database)
					return access, refresh, true
				},
			}
			switch table {
			case "migrations":
				owner.sink = func(database *sql.DB) OAuthTokenSink { return NewMigrationTokenSink(database, "owner-1", role) }
			case "sync_jobs":
				owner.sink = func(database *sql.DB) OAuthTokenSink { return NewSyncJobTokenSink(database, "owner-1", role) }
			default:
				owner.sink = func(database *sql.DB) OAuthTokenSink { return NewBackupJobTokenSink(database, "owner-1", role) }
			}
			owners = append(owners, owner)
		}
	}
	owners = append(owners,
		tokenSinkOwner{
			name: "backup_repository",
			seed: seedCredentialRow("backup_jobs"),
			sink: func(database *sql.DB) OAuthTokenSink { return NewBackupRepositoryTokenSink(database, "owner-1") },
			read: readCredentialSlot("backup_jobs", "target"),
			readOther: func(t *testing.T, database *sql.DB) (string, string, bool) {
				access, refresh := readCredentialSlot("backup_jobs", "source")(t, database)
				return access, refresh, true
			},
		},
		tokenSinkOwner{
			name: "restore_run",
			seed: func(t *testing.T, database *sql.DB, access, refresh string, expires time.Time) {
				t.Helper()
				if _, err := database.Exec(`INSERT INTO restore_runs (id, target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at) VALUES ('owner-1', $1, $2, $3)`, access, refresh, expires); err != nil {
					t.Fatalf("seed restore_runs: %v", err)
				}
			},
			sink: func(database *sql.DB) OAuthTokenSink { return NewRestoreRunTokenSink(database, "owner-1") },
			read: readCredentialSlot("restore_runs", "target"),
			readOther: func(*testing.T, *sql.DB) (string, string, bool) {
				return "", "", false
			},
		},
		tokenSinkOwner{
			name: "connection_profile",
			seed: func(t *testing.T, database *sql.DB, access, refresh string, expires time.Time) {
				t.Helper()
				if _, err := database.Exec(`INSERT INTO connection_profiles (id, password_encrypted, refresh_token_encrypted, token_expires_at) VALUES ('owner-1', $1, $2, $3)`, access, refresh, expires); err != nil {
					t.Fatalf("seed connection_profiles: %v", err)
				}
			},
			sink: func(database *sql.DB) OAuthTokenSink { return NewConnectionProfileTokenSink(database, "owner-1") },
			read: func(t *testing.T, database *sql.DB) (string, string) {
				t.Helper()
				var access, refresh string
				if err := database.QueryRow(`SELECT password_encrypted, refresh_token_encrypted FROM connection_profiles WHERE id = 'owner-1'`).Scan(&access, &refresh); err != nil {
					t.Fatalf("read connection_profiles: %v", err)
				}
				return access, refresh
			},
			readOther: func(*testing.T, *sql.DB) (string, string, bool) { return "", "", false },
		},
	)
	return owners
}

func rotated(access, refresh string) EncryptedOAuthTokens {
	return EncryptedOAuthTokens{Access: access, Refresh: refresh, ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second)}
}

// Every owner type shares one CAS contract: a rotation anchored to the stored
// refresh token succeeds, anything else is reported as ErrOAuthTokenConflict
// without touching the row, and the loser of a race can adopt the winner.
func TestOAuthTokenSinkContract(t *testing.T) {
	ctx := context.Background()
	for _, owner := range tokenSinkOwners() {
		owner := owner
		t.Run(owner.name, func(t *testing.T) {
			database := setupTokenSinkDB(t)
			owner.seed(t, database, "old-access", "old-refresh", time.Now().Truncate(time.Second))
			sink := owner.sink(database)

			t.Run("rotate succeeds", func(t *testing.T) {
				if err := sink.Persist(ctx, rotated("access-1", "refresh-1"), "old-refresh"); err != nil {
					t.Fatalf("Persist() error = %v", err)
				}
				access, refresh := owner.read(t, database)
				if access != "access-1" || refresh != "refresh-1" {
					t.Fatalf("stored tokens = %q/%q, want access-1/refresh-1", access, refresh)
				}
				if otherAccess, otherRefresh, ok := owner.readOther(t, database); ok && (otherAccess != "old-access" || otherRefresh != "old-refresh") {
					t.Fatalf("rotation leaked into the other credential slot: %q/%q", otherAccess, otherRefresh)
				}
			})

			t.Run("stale expected token is rejected", func(t *testing.T) {
				err := sink.Persist(ctx, rotated("stale-access", "stale-refresh"), "old-refresh")
				if !errors.Is(err, ErrOAuthTokenConflict) {
					t.Fatalf("Persist(stale) error = %v, want ErrOAuthTokenConflict", err)
				}
				access, refresh := owner.read(t, database)
				if access != "access-1" || refresh != "refresh-1" {
					t.Fatalf("stale rotation overwrote the row: %q/%q", access, refresh)
				}
			})

			t.Run("empty expected token is rejected", func(t *testing.T) {
				err := sink.Persist(ctx, rotated("blank-access", "blank-refresh"), "")
				if !errors.Is(err, ErrOAuthTokenConflict) {
					t.Fatalf("Persist(empty expected) error = %v, want ErrOAuthTokenConflict", err)
				}
				access, refresh := owner.read(t, database)
				if access != "access-1" || refresh != "refresh-1" {
					t.Fatalf("empty-expected rotation overwrote the row: %q/%q", access, refresh)
				}
			})

			t.Run("concurrent rotation is adopted", func(t *testing.T) {
				// The winner rotates first; the loser still holds the pre-race token.
				if err := sink.Persist(ctx, rotated("winner-access", "winner-refresh"), "refresh-1"); err != nil {
					t.Fatalf("winner Persist() error = %v", err)
				}
				err := sink.Persist(ctx, rotated("loser-access", "loser-refresh"), "refresh-1")
				if !errors.Is(err, ErrOAuthTokenConflict) {
					t.Fatalf("loser Persist() error = %v, want ErrOAuthTokenConflict", err)
				}
				access, refresh := owner.read(t, database)
				if access != "winner-access" || refresh != "winner-refresh" {
					t.Fatalf("loser displaced the winner: %q/%q", access, refresh)
				}
				// Having adopted the winner's token the loser can keep rotating from it.
				if err := sink.Persist(ctx, rotated("next-access", "next-refresh"), refresh); err != nil {
					t.Fatalf("Persist() from the adopted token error = %v", err)
				}
			})
		})
	}
}

func TestOAuthTokenSinkRejectsInvalidRole(t *testing.T) {
	database := setupTokenSinkDB(t)
	for name, sink := range map[string]OAuthTokenSink{
		"migration":  NewMigrationTokenSink(database, "owner-1", "sideways"),
		"sync job":   NewSyncJobTokenSink(database, "owner-1", "sideways"),
		"backup job": NewBackupJobTokenSink(database, "owner-1", "sideways"),
	} {
		if err := sink.Persist(context.Background(), rotated("a", "r"), "old-refresh"); err == nil {
			t.Errorf("%s: Persist() with an invalid role succeeded", name)
		}
	}
}

// Re-authorization is the user supplying brand-new tokens, so it replaces the
// stored pair even when no refresh token was previously recorded.
func TestOAuthReauthSinkReplacesTokensUnconditionally(t *testing.T) {
	ctx := context.Background()
	covered := 0
	for _, owner := range tokenSinkOwners() {
		owner := owner
		if _, ok := owner.sink(nil).(OAuthReauthSink); !ok {
			continue
		}
		covered++
		t.Run(owner.name, func(t *testing.T) {
			database := setupTokenSinkDB(t)
			owner.seed(t, database, "old-access", "old-refresh", time.Now().Truncate(time.Second))
			reauth := owner.sink(database).(OAuthReauthSink)
			if err := reauth.Reauthorize(ctx, rotated("fresh-access", "fresh-refresh")); err != nil {
				t.Fatalf("Reauthorize() error = %v", err)
			}
			access, refresh := owner.read(t, database)
			if access != "fresh-access" || refresh != "fresh-refresh" {
				t.Fatalf("stored tokens = %q/%q, want fresh-access/fresh-refresh", access, refresh)
			}
			if otherAccess, otherRefresh, ok := owner.readOther(t, database); ok && (otherAccess != "old-access" || otherRefresh != "old-refresh") {
				t.Fatalf("re-authorization leaked into the other credential slot: %q/%q", otherAccess, otherRefresh)
			}
		})
	}
	// Migrations and sync jobs, source and target each.
	if covered != 4 {
		t.Fatalf("re-authorization covered %d owners, want 4", covered)
	}
}

// A sink must hand its context to the database so a rotation persisted under a
// lock-bounded deadline cannot outlive that lease. A canceled context fails
// before any connection is attempted, so this needs no live database.
func TestOAuthTokenSinksHonorContext(t *testing.T) {
	database, err := sql.Open("postgres", "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, sink := range map[string]OAuthTokenSink{
		"migration":          NewMigrationTokenSink(database, "id", "source"),
		"sync job":           NewSyncJobTokenSink(database, "id", "target"),
		"backup job":         NewBackupJobTokenSink(database, "id", "source"),
		"backup repository":  NewBackupRepositoryTokenSink(database, "id"),
		"restore run":        NewRestoreRunTokenSink(database, "id"),
		"connection profile": NewConnectionProfileTokenSink(database, "id"),
	} {
		if err := sink.Persist(ctx, rotated("a", "r"), "old-refresh"); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: Persist(canceled ctx) error = %v, want context.Canceled", name, err)
		}
	}
	for name, sink := range map[string]OAuthReauthSink{
		"migration": NewMigrationTokenSink(database, "id", "source"),
		"sync job":  NewSyncJobTokenSink(database, "id", "target"),
	} {
		if err := sink.Reauthorize(ctx, rotated("a", "r")); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: Reauthorize(canceled ctx) error = %v, want context.Canceled", name, err)
		}
	}
}
