package db

import (
	"context"
	"database/sql"
)

// AuditStore is the persistence seam for the audit log. It delegates to the
// package-level functions without adding queries.
//
// Writes are bounded, best-effort and asynchronous: a failed insert is logged
// and never delays or fails the operation that produced the event. Reads are
// the admin audit-log listing.
type AuditStore interface {
	WriteAuditLog(e AuditEntry)
	WriteAuditLogContext(ctx context.Context, e AuditEntry)
	ListAuditLog(p AuditLogParams) ([]AuditLogRow, int, error)
}

type postgresAuditStore struct{ db *sql.DB }

// NewAuditStore returns the PostgreSQL-backed AuditStore. It panics on a nil
// handle so a wiring mistake fails at startup instead of on the first query.
func NewAuditStore(database *sql.DB) AuditStore {
	if database == nil {
		panic("db: NewAuditStore requires a database handle")
	}
	return postgresAuditStore{db: database}
}

func (s postgresAuditStore) WriteAuditLog(e AuditEntry) {
	writeAuditLog(s.db, e)
}

func (s postgresAuditStore) WriteAuditLogContext(ctx context.Context, e AuditEntry) {
	writeAuditLogContext(ctx, s.db, e)
}

func (s postgresAuditStore) ListAuditLog(p AuditLogParams) ([]AuditLogRow, int, error) {
	return listAuditLog(s.db, p)
}
