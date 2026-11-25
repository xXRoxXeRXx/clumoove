package db

import (
	"context"
	"database/sql"
)

// RestoreAPIStore is the persistence seam for the owner-facing restore API:
// previews, runs, items and restore jobs. Worker-side planning and execution use
// RestoreStore instead. It delegates to the package-level functions without
// adding queries.
//
// Every call takes the owning user id and its query scopes on it, so a preview,
// run or job that does not exist or belongs to someone else is indistinguishable
// (sql.ErrNoRows, the ErrRestorePreview* sentinels, or false). Credentials in a
// RestorePreview are already encrypted by the caller; the store never sees
// plaintext or the key.
type RestoreAPIStore interface {
	// Previews. ConsumeRestorePreviewContext turns a ready preview into a job and
	// run, or returns one of ErrRestorePreviewNotFound, ErrRestorePreviewExpired,
	// ErrRestorePreviewStale, ErrRestoreSnapshotUnavailable or ErrRestoreRetryMismatch.
	CreateRestorePreviewContext(ctx context.Context, preview *RestorePreview) (string, error)
	GetRestorePreviewForOwnerContext(ctx context.Context, previewID, userID string) (*RestorePreview, error)
	CancelRestorePreviewForOwnerContext(ctx context.Context, previewID, userID string) (bool, error)
	ConsumeRestorePreviewContext(ctx context.Context, previewID, userID string) (*RestoreJob, *RestoreRun, error)
	// GetRestoreRetrySourceContext returns sql.ErrNoRows for an absent or foreign job.
	GetRestoreRetrySourceContext(ctx context.Context, restoreJobID, userID string) (*RestoreRetrySource, error)

	// Runs, items and jobs. The cancel and delete calls report false when the
	// run or job is absent, foreign or not in a state that allows the change.
	ListRestoreRunsForOwnerContext(ctx context.Context, userID string) ([]RestoreRun, error)
	GetRestoreRunForOwnerContext(ctx context.Context, runID, userID string) (*RestoreRun, error)
	ListRestoreItemsForOwnerContext(ctx context.Context, runID, userID string) ([]RestoreItem, error)
	ListRestoreItemsForRunOwnerContext(ctx context.Context, runID, userID string) ([]RestoreItem, error)
	CancelRestoreRunForOwnerContext(ctx context.Context, runID, userID string) (bool, error)
	DeleteRestoreJobForOwnerContext(ctx context.Context, jobID, userID string) (bool, error)
}

type postgresRestoreAPIStore struct{ db *sql.DB }

// NewRestoreAPIStore returns the PostgreSQL-backed RestoreAPIStore. It panics on
// a nil handle so a wiring mistake fails at startup instead of on the first query.
func NewRestoreAPIStore(database *sql.DB) RestoreAPIStore {
	if database == nil {
		panic("db: NewRestoreAPIStore requires a database handle")
	}
	return postgresRestoreAPIStore{db: database}
}

func (s postgresRestoreAPIStore) CreateRestorePreviewContext(ctx context.Context, preview *RestorePreview) (string, error) {
	return createRestorePreviewContext(ctx, s.db, preview)
}

func (s postgresRestoreAPIStore) GetRestorePreviewForOwnerContext(ctx context.Context, previewID, userID string) (*RestorePreview, error) {
	return getRestorePreviewForOwnerContext(ctx, s.db, previewID, userID)
}

func (s postgresRestoreAPIStore) CancelRestorePreviewForOwnerContext(ctx context.Context, previewID, userID string) (bool, error) {
	return cancelRestorePreviewForOwnerContext(ctx, s.db, previewID, userID)
}

func (s postgresRestoreAPIStore) ConsumeRestorePreviewContext(ctx context.Context, previewID, userID string) (*RestoreJob, *RestoreRun, error) {
	return consumeRestorePreviewContext(ctx, s.db, previewID, userID)
}

func (s postgresRestoreAPIStore) GetRestoreRetrySourceContext(ctx context.Context, restoreJobID, userID string) (*RestoreRetrySource, error) {
	return getRestoreRetrySourceContext(ctx, s.db, restoreJobID, userID)
}

func (s postgresRestoreAPIStore) ListRestoreRunsForOwnerContext(ctx context.Context, userID string) ([]RestoreRun, error) {
	return listRestoreRunsForOwnerContext(ctx, s.db, userID)
}

func (s postgresRestoreAPIStore) GetRestoreRunForOwnerContext(ctx context.Context, runID, userID string) (*RestoreRun, error) {
	return getRestoreRunForOwnerContext(ctx, s.db, runID, userID)
}

func (s postgresRestoreAPIStore) ListRestoreItemsForOwnerContext(ctx context.Context, runID, userID string) ([]RestoreItem, error) {
	return listRestoreItemsForOwnerContext(ctx, s.db, runID, userID)
}

func (s postgresRestoreAPIStore) ListRestoreItemsForRunOwnerContext(ctx context.Context, runID, userID string) ([]RestoreItem, error) {
	return listRestoreItemsForRunOwnerContext(ctx, s.db, runID, userID)
}

func (s postgresRestoreAPIStore) CancelRestoreRunForOwnerContext(ctx context.Context, runID, userID string) (bool, error) {
	return cancelRestoreRunForOwnerContext(ctx, s.db, runID, userID)
}

func (s postgresRestoreAPIStore) DeleteRestoreJobForOwnerContext(ctx context.Context, jobID, userID string) (bool, error) {
	return deleteRestoreJobForOwnerContext(ctx, s.db, jobID, userID)
}
