package sync

import (
	"context"
	"database/sql"
	"testing"

	"backend/internal/db"
)

// fakeAuditStore records writes; any other AuditStore call panics through the
// nil embedded interface.
type fakeAuditStore struct {
	db.AuditStore
	written []db.AuditEntry
}

func (f *fakeAuditStore) WriteAuditLog(e db.AuditEntry) { f.written = append(f.written, e) }

func (f *fakeAuditStore) WriteAuditLogContext(_ context.Context, e db.AuditEntry) {
	f.written = append(f.written, e)
}

func TestPostgresPassStoreAuditsRecoveryThroughTheAuditStore(t *testing.T) {
	audit := &fakeAuditStore{}
	ps := postgresPassStore{audit: audit}

	if err := ps.AuditRecovered(context.Background(), RecoveredPass{ID: "sync-1", UserID: "user-1"}); err != nil {
		t.Fatalf("AuditRecovered() = %v", err)
	}

	if len(audit.written) != 1 {
		t.Fatalf("written = %d entries, want 1", len(audit.written))
	}
	got := audit.written[0]
	if got.Action != db.AuditSyncRecovered || got.Target != "sync-1" || got.UserID != (sql.NullString{String: "user-1", Valid: true}) {
		t.Fatalf("entry = %+v, want SYNC_RECOVERED on sync-1 for user-1", got)
	}
}

func TestPostgresPassStoreAuditsRecoveryWithoutAnAuditStoreAsANoOp(t *testing.T) {
	if err := (postgresPassStore{}).AuditRecovered(context.Background(), RecoveredPass{ID: "sync-1"}); err != nil {
		t.Fatalf("AuditRecovered() = %v, want nil", err)
	}
}

func TestEngineAuditsThroughTheAuditStore(t *testing.T) {
	audit := &fakeAuditStore{}
	e := &Engine{auditLog: audit}

	e.audit(db.AuditEntry{Action: db.AuditSyncFailed, Target: "sync-2"})
	(&Engine{}).audit(db.AuditEntry{Action: db.AuditSyncFailed, Target: "ignored"})

	if len(audit.written) != 1 || audit.written[0].Target != "sync-2" {
		t.Fatalf("written = %+v, want a single entry for sync-2", audit.written)
	}
}
