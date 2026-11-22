package db

import (
	"context"
	"errors"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Recording driver: a stand-in for PostgreSQL that captures statements so the
// domain stores can be checked for faithful delegation without a database.

type recordedStatement struct {
	query string
	args  []driver.Value
}

type recordingDriver struct {
	statements []recordedStatement
	// queryColumns/queryRow override the default single-column row returned by queries.
	queryColumns []string
	queryRow     []driver.Value
}

type recordingConn struct{ owner *recordingDriver }

type recordingStmt struct {
	conn  *recordingConn
	query string
}

type recordedRows struct {
	done    bool
	columns []string
	row     []driver.Value
}

var recordingDriverSeq atomic.Int64

func (d *recordingDriver) Open(string) (driver.Conn, error) { return &recordingConn{owner: d}, nil }

func (c *recordingConn) Prepare(query string) (driver.Stmt, error) {
	return &recordingStmt{conn: c, query: query}, nil
}
func (c *recordingConn) Close() error              { return nil }
func (c *recordingConn) Begin() (driver.Tx, error) { return recordingTx{}, nil }

type recordingTx struct{}

func (recordingTx) Commit() error   { return nil }
func (recordingTx) Rollback() error { return nil }

func (s *recordingStmt) Close() error  { return nil }
func (s *recordingStmt) NumInput() int { return -1 }
func (s *recordingStmt) record(args []driver.Value) {
	s.conn.owner.statements = append(s.conn.owner.statements, recordedStatement{query: s.query, args: args})
}
func (s *recordingStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.record(args)
	return driver.RowsAffected(1), nil
}
func (s *recordingStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.record(args)
	if d := s.conn.owner; d.queryColumns != nil {
		return &recordedRows{columns: d.queryColumns, row: d.queryRow}, nil
	}
	return &recordedRows{}, nil
}

func (r *recordedRows) Columns() []string {
	if r.columns != nil {
		return r.columns
	}
	return []string{"id"}
}
func (r *recordedRows) Close() error { return nil }
func (r *recordedRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	if r.row != nil {
		copy(dest, r.row)
		return nil
	}
	dest[0] = "row-id"
	return nil
}

func openRecordingDB(t *testing.T) (*sql.DB, *recordingDriver) {
	t.Helper()
	rec := &recordingDriver{}
	name := "recording-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + string(rune('a'+recordingDriverSeq.Add(1)%26))
	sql.Register(name, rec)
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open recording db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, rec
}

func requireSingleStatement(t *testing.T, rec *recordingDriver, wantQuery string, wantArgs ...any) {
	t.Helper()
	if len(rec.statements) != 1 {
		t.Fatalf("recorded %d statements, want 1: %+v", len(rec.statements), rec.statements)
	}
	got := rec.statements[0]
	if !strings.Contains(got.query, wantQuery) {
		t.Errorf("query = %q, want it to contain %q", got.query, wantQuery)
	}
	if len(got.args) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", got.args, wantArgs)
	}
	for i, want := range wantArgs {
		if got.args[i] != driver.Value(want) {
			t.Errorf("arg[%d] = %#v, want %#v", i, got.args[i], want)
		}
	}
}

func TestBackupStoreDelegatesToBackupPersistence(t *testing.T) {
	database, rec := openRecordingDB(t)
	var store BackupStore = NewBackupStore(database)

	if err := store.TouchBackupRunContext(context.Background(), "job-1", 7, "run-1"); err != nil {
		t.Fatalf("TouchBackupRunContext() error: %v", err)
	}

	requireSingleStatement(t, rec, "UPDATE backup_runs", "run-1", "job-1", int64(7))
}

func TestRestoreStoreDelegatesToRestorePersistence(t *testing.T) {
	database, rec := openRecordingDB(t)
	var store RestoreStore = NewRestoreStore(database)

	if err := store.ExpireRestorePreviewsContext(context.Background()); err != nil {
		t.Fatalf("ExpireRestorePreviewsContext() error: %v", err)
	}

	requireSingleStatement(t, rec, "UPDATE restore_previews")
}

func TestRestoreStoreForwardsClaimEpoch(t *testing.T) {
	database, rec := openRecordingDB(t)
	store := NewRestoreStore(database)

	if err := store.HeartbeatRestoreItemContext(context.Background(), Lease{ID: "item-1", Epoch: 4}, "run-1", "worker-1"); err != nil {
		t.Fatalf("HeartbeatRestoreItemContext() error: %v", err)
	}

	if len(rec.statements) != 1 {
		t.Fatalf("recorded %d statements, want 1", len(rec.statements))
	}
	found := false
	for _, arg := range rec.statements[0].args {
		if arg == driver.Value(int64(4)) {
			found = true
		}
	}
	if !found {
		t.Errorf("claim epoch 4 not forwarded to SQL, args = %v", rec.statements[0].args)
	}
}

func TestSyncStoreDelegatesToSyncPersistence(t *testing.T) {
	database, rec := openRecordingDB(t)
	var store SyncStore = NewSyncStore(database)

	updated, err := store.UpdateSyncJobTotals("sync-1", 3, 10, 2048)
	if err != nil {
		t.Fatalf("UpdateSyncJobTotals() error: %v", err)
	}
	if !updated {
		t.Error("UpdateSyncJobTotals() = false, want true when the row is returned")
	}

	requireSingleStatement(t, rec, "UPDATE sync_jobs", int64(10), int64(2048), "sync-1", int64(3))
}

func TestSharedBackupRepositoryCredentialsAreReachableFromBothDomains(t *testing.T) {
	database, _ := openRecordingDB(t)

	var _ BackupRepositoryCredentials = NewBackupStore(database)
	var _ BackupRepositoryCredentials = NewRestoreStore(database)
}

func TestDomainStoreConstructorsRejectNilDatabase(t *testing.T) {
	for name, construct := range map[string]func(){
		"backup":    func() { NewBackupStore(nil) },
		"restore":   func() { NewRestoreStore(nil) },
		"sync":      func() { NewSyncStore(nil) },
		"profile":   func() { NewProfileStore(nil) },
		"migration": func() { NewMigrationStore(nil) },
		"task":      func() { NewTaskStore(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("New%sStore(nil) did not panic", name)
				}
			}()
			construct()
		})
	}
}

func TestRestoreStoreLoadsRestoreRunTargetConnection(t *testing.T) {
	database, rec := openRecordingDB(t)
	rec.queryColumns = []string{"backup_job_id", "target_provider", "target_url", "target_username", "conflict_strategy", "bandwidth_mbps", "target_password", "refresh", "expires", "mega_session", "mega_master"}
	expires := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	rec.queryRow = []driver.Value{"backup-1", "nextcloud", "https://cloud.example", "alice", "RENAME", int64(25), "enc-pass", "enc-refresh", expires, nil, "enc-master"}
	var store RestoreStore = NewRestoreStore(database)

	got, err := store.GetRestoreRunTargetConnectionContext(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("GetRestoreRunTargetConnectionContext() error: %v", err)
	}

	requireSingleStatement(t, rec, "FROM restore_runs r JOIN restore_jobs j ON j.id = r.restore_job_id WHERE r.id = $1", "run-1")
	want := &RestoreRunTargetConnection{
		BackupJobID: "backup-1", TargetProvider: "nextcloud", TargetURL: "https://cloud.example", TargetUsername: "alice",
		ConflictStrategy: "RENAME", BandwidthMbps: 25, TargetPasswordEncrypted: "enc-pass",
		TargetRefreshEncrypted:       sql.NullString{String: "enc-refresh", Valid: true},
		TargetTokenExpiresAt:         sql.NullTime{Time: expires, Valid: true},
		TargetMegaSessionEncrypted:   sql.NullString{},
		TargetMegaMasterKeyEncrypted: sql.NullString{String: "enc-master", Valid: true},
	}
	if *got != *want {
		t.Errorf("connection = %+v, want %+v", *got, *want)
	}
}

// requireFencedByEpoch asserts that the first recorded statement compares the
// stored claim_epoch against the lease epoch and has no escape hatch that
// disables the comparison.
func requireFencedByEpoch(t *testing.T, rec *recordingDriver, lease Lease) {
	t.Helper()
	if len(rec.statements) == 0 {
		t.Fatal("no statement recorded")
	}
	got := rec.statements[0]
	if !strings.Contains(got.query, "claim_epoch = $") {
		t.Errorf("query does not compare claim_epoch: %q", got.query)
	}
	if strings.Contains(got.query, "= 0 OR") {
		t.Errorf("query keeps an escape hatch that disables epoch fencing: %q", got.query)
	}
	foundID, foundEpoch := false, false
	for _, arg := range got.args {
		if arg == driver.Value(lease.ID) {
			foundID = true
		}
		if arg == driver.Value(lease.Epoch) {
			foundEpoch = true
		}
	}
	if !foundID || !foundEpoch {
		t.Errorf("args = %v, want lease id %q and epoch %d", got.args, lease.ID, lease.Epoch)
	}
}

func backupFencedWrites(lease Lease) map[string]func(BackupStore) error {
	return map[string]func(BackupStore) error{
		"complete": func(s BackupStore) error {
			_, err := s.CompleteBackupVerifyTargetContext(context.Background(), lease, true, 10, 10)
			return err
		},
		"mark read": func(s BackupStore) error {
			return s.MarkBackupVerifyTargetReadContext(context.Background(), lease, 10)
		},
		"mark damaged": func(s BackupStore) error {
			return s.MarkBackupVerifyTargetDamagedContext(context.Background(), lease)
		},
	}
}

func TestBackupStoreFencesVerifyTargetWritesByLease(t *testing.T) {
	lease := Lease{ID: "target-1", Epoch: 5}
	for name, write := range backupFencedWrites(lease) {
		t.Run(name, func(t *testing.T) {
			database, rec := openRecordingDB(t)
			if err := write(NewBackupStore(database)); err != nil {
				t.Fatalf("write error: %v", err)
			}
			requireFencedByEpoch(t, rec, lease)
		})
	}
}

func TestBackupVerifyTargetExposesItsClaimAsALease(t *testing.T) {
	target := BackupVerifyTarget{ID: "target-1", ClaimEpoch: 3}
	if got, want := target.Lease(), (Lease{ID: "target-1", Epoch: 3}); got != want {
		t.Errorf("Lease() = %+v, want %+v", got, want)
	}
}

func restoreFencedWrites(lease Lease) map[string]func(RestoreStore) error {
	ctx := context.Background()
	return map[string]func(RestoreStore) error{
		"set item target path": func(s RestoreStore) error {
			return s.SetRestoreItemTargetPathContext(ctx, lease, "run-1", "/target")
		},
		"complete item": func(s RestoreStore) error {
			_, err := s.CompleteRestoreItemContext(ctx, lease, "run-1", 10, "SHA256")
			return err
		},
		"warn": func(s RestoreStore) error {
			return s.WarnRestoreItemContext(ctx, lease, "run-1", "RESTORE_METADATA_INVALID")
		},
		"retry": func(s RestoreStore) error {
			_, err := s.RetryRestoreItemContext(ctx, lease, "run-1", "RESTORE_TRANSFER_FAILED")
			return err
		},
		"fail": func(s RestoreStore) error {
			return s.FailRestoreItemContext(ctx, lease, "run-1", "RESTORE_TRANSFER_FAILED")
		},
		"skip": func(s RestoreStore) error {
			return s.SkipRestoreItemContext(ctx, lease, "run-1", "RESTORE_TARGET_EXISTS")
		},
		"set directory target path": func(s RestoreStore) error {
			return s.SetRestoreDirectoryTargetPathContext(ctx, lease, "run-1", "dir", "/target/dir")
		},
		"complete directory": func(s RestoreStore) error {
			return s.CompleteRestoreDirectoryContext(ctx, lease, "run-1")
		},
	}
}

func TestRestoreStoreFencesItemWritesByLease(t *testing.T) {
	lease := Lease{ID: "item-1", Epoch: 6}
	for name, write := range restoreFencedWrites(lease) {
		t.Run(name, func(t *testing.T) {
			database, rec := openRecordingDB(t)
			if err := write(NewRestoreStore(database)); err != nil {
				t.Fatalf("write error: %v", err)
			}
			// Item completion also bumps run counters in the same transaction;
			// the fenced item update is always the first statement.
			requireFencedByEpoch(t, rec, lease)
		})
	}
}

func TestRestoreItemExposesItsClaimAsALease(t *testing.T) {
	item := RestoreItem{ID: "item-1", ClaimEpoch: 8}
	if got, want := item.Lease(), (Lease{ID: "item-1", Epoch: 8}); got != want {
		t.Errorf("Lease() = %+v, want %+v", got, want)
	}
}

func TestFencedWritesRejectAZeroLeaseWithoutTouchingTheDatabase(t *testing.T) {
	for _, lease := range []Lease{{}, {ID: "item-1"}} {
		for name, write := range backupFencedWrites(lease) {
			t.Run("backup/"+name, func(t *testing.T) {
				database, rec := openRecordingDB(t)
				if err := write(NewBackupStore(database)); !errors.Is(err, ErrInvalidLease) {
					t.Errorf("error = %v, want ErrInvalidLease", err)
				}
				if len(rec.statements) != 0 {
					t.Errorf("recorded statements for a zero lease: %+v", rec.statements)
				}
			})
		}
		for name, write := range restoreFencedWrites(lease) {
			t.Run("restore/"+name, func(t *testing.T) {
				database, rec := openRecordingDB(t)
				if err := write(NewRestoreStore(database)); !errors.Is(err, ErrInvalidLease) {
					t.Errorf("error = %v, want ErrInvalidLease", err)
				}
				if len(rec.statements) != 0 {
					t.Errorf("recorded statements for a zero lease: %+v", rec.statements)
				}
			})
		}
	}
}

func TestProfileStoreDelegatesToProfilePersistence(t *testing.T) {
	t.Run("ownership check is scoped to the profile and the user", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		rec.queryColumns = []string{"exists"}
		rec.queryRow = []driver.Value{true}
		var store ProfileStore = NewProfileStore(database)

		owned, err := store.VerifyProfileOwnership(context.Background(), "profile-1", "user-1")
		if err != nil {
			t.Fatalf("VerifyProfileOwnership() error: %v", err)
		}
		if !owned {
			t.Error("VerifyProfileOwnership() = false, want true")
		}
		requireSingleStatement(t, rec, "FROM connection_profiles WHERE id = $1 AND user_id = $2", "profile-1", "user-1")
	})

	t.Run("delete is keyed by profile id", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		if err := NewProfileStore(database).DeleteConnectionProfile(context.Background(), "profile-1"); err != nil {
			t.Fatalf("DeleteConnectionProfile() error: %v", err)
		}
		requireSingleStatement(t, rec, "DELETE FROM connection_profiles WHERE id = $1", "profile-1")
	})

	t.Run("update with no fields issues no statement", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		if err := NewProfileStore(database).UpdateConnectionProfile(context.Background(), "profile-1", UpdateConnectionProfileInput{}); err != nil {
			t.Fatalf("UpdateConnectionProfile() error: %v", err)
		}
		if len(rec.statements) != 0 {
			t.Fatalf("recorded %d statements, want 0", len(rec.statements))
		}
	})

	t.Run("update binds the profile id first", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		name := "renamed"
		if err := NewProfileStore(database).UpdateConnectionProfile(context.Background(), "profile-1", UpdateConnectionProfileInput{Name: &name}); err != nil {
			t.Fatalf("UpdateConnectionProfile() error: %v", err)
		}
		requireSingleStatement(t, rec, "UPDATE connection_profiles SET name = $2 WHERE id = $1", "profile-1", "renamed")
	})

	t.Run("token sink is guarded by the previously read refresh token", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		err := NewProfileStore(database).TokenSink("profile-1").Persist(context.Background(), EncryptedOAuthTokens{Access: "a", Refresh: "r"}, "old-refresh")
		if err != nil {
			t.Fatalf("Persist() error: %v", err)
		}
		if len(rec.statements) != 1 || !strings.Contains(rec.statements[0].query, "UPDATE connection_profiles") || !strings.Contains(rec.statements[0].query, "refresh_token_encrypted = $5") {
			t.Fatalf("statements = %+v", rec.statements)
		}
	})
}

func TestMigrationStoreDelegatesToMigrationPersistence(t *testing.T) {
	t.Run("threads update is keyed by migration id", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		var store MigrationStore = NewMigrationStore(database)

		if err := store.UpdateMigrationThreads("mig-1", 8); err != nil {
			t.Fatalf("UpdateMigrationThreads() error: %v", err)
		}
		requireSingleStatement(t, rec, "SET threads = $1", int64(8), "mig-1")
	})

	t.Run("bandwidth update is keyed by migration id", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		if err := NewMigrationStore(database).UpdateMigrationBandwidthLimit("mig-1", 25); err != nil {
			t.Fatalf("UpdateMigrationBandwidthLimit() error: %v", err)
		}
		requireSingleStatement(t, rec, "SET bandwidth_limit_mbps = $1", int64(25), "mig-1")
	})

	t.Run("cancelling pending tasks only touches PENDING rows of that migration", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		if err := NewMigrationStore(database).CancelPendingTasks("mig-1"); err != nil {
			t.Fatalf("CancelPendingTasks() error: %v", err)
		}
		requireSingleStatement(t, rec, "WHERE migration_id = $1 AND status = 'PENDING'", "mig-1")
	})

	t.Run("owned lookup binds both the migration and the owner", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		_, _ = NewMigrationStore(database).GetOwnedMigrationContext(context.Background(), "mig-1", "user-1")
		if len(rec.statements) != 1 {
			t.Fatalf("recorded %d statements, want 1", len(rec.statements))
		}
		got := rec.statements[0]
		if len(got.args) != 2 || got.args[0] != driver.Value("mig-1") || got.args[1] != driver.Value("user-1") {
			t.Fatalf("args = %v, want [mig-1 user-1]", got.args)
		}
	})

	t.Run("file manager transfers are selected by the caller's marker", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		rec.queryColumns = []string{"id", "picker_session_id", "status", "source", "target", "total", "processed", "failed", "skipped"}
		rec.queryRow = []driver.Value{"mig-1", "file-manager-transfer:copy", "RUNNING", "src", "dst", int64(4), int64(2), int64(1), int64(0)}

		transfers, err := NewMigrationStore(database).ListFileManagerTransfers(context.Background(), "user-1", "profile-1", "file-manager-transfer:")
		if err != nil {
			t.Fatalf("ListFileManagerTransfers() error: %v", err)
		}
		requireSingleStatement(t, rec, "m.picker_session_id LIKE $3", "user-1", "profile-1", "file-manager-transfer:")
		want := FileManagerTransfer{ID: "mig-1", Marker: "file-manager-transfer:copy", Status: "RUNNING", SourceProfileName: "src", TargetProfileName: "dst", TotalFiles: 4, ProcessedFiles: 2, FailedFiles: 1}
		if len(transfers) != 1 || transfers[0] != want {
			t.Fatalf("transfers = %+v, want [%+v]", transfers, want)
		}
	})
}

func TestMigrationStoreWorkerCallsKeepTheirFences(t *testing.T) {
	t.Run("indexing lease renewal is fenced by status and generation", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		renewed, err := NewMigrationStore(database).RenewMigrationIndexingLease(context.Background(), "mig-1", 3, 90*time.Second)
		if err != nil {
			t.Fatalf("RenewMigrationIndexingLease() error: %v", err)
		}
		if !renewed {
			t.Error("RenewMigrationIndexingLease() = false, want true when one row matches")
		}
		requireSingleStatement(t, rec, "status = 'INDEXING' AND discovery_generation = $2", "mig-1", int64(3), int64(90))
	})

	t.Run("live byte counters are keyed by migration id", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		if err := NewMigrationStore(database).AddLiveBytes(context.Background(), "mig-1", 512); err != nil {
			t.Fatalf("AddLiveBytes() error: %v", err)
		}
		requireSingleStatement(t, rec, "SET live_bytes = live_bytes + $1", int64(512), "mig-1")
	})

	t.Run("owner lookup reads the migration owner", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		owner, err := NewMigrationStore(database).GetMigrationOwnerID("mig-1")
		if err != nil {
			t.Fatalf("GetMigrationOwnerID() error: %v", err)
		}
		if owner != "row-id" {
			t.Errorf("owner = %q, want the scanned user_id", owner)
		}
		requireSingleStatement(t, rec, "SELECT user_id FROM migrations WHERE id = $1", "mig-1")
	})
}

func TestTaskStoreKeepsItsClaimFences(t *testing.T) {
	t.Run("an early transition is fenced by status and claim epoch", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		var store TaskStore = NewTaskStore(database)

		if err := store.TransitionClaimedTask(context.Background(), Lease{ID: "task-1", Epoch: 5}, "PENDING"); err != nil {
			t.Fatalf("TransitionClaimedTask() error: %v", err)
		}
		requireSingleStatement(t, rec, "WHERE id = $2 AND status = 'RUNNING' AND claim_epoch = $3", "PENDING", "task-1", int64(5))
	})

	t.Run("a claim heartbeat is fenced by status and claim epoch", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		owned, err := NewTaskStore(database).HeartbeatTaskClaim(context.Background(), Lease{ID: "task-1", Epoch: 5})
		if err != nil {
			t.Fatalf("HeartbeatTaskClaim() error: %v", err)
		}
		if !owned {
			t.Error("HeartbeatTaskClaim() = false, want true when the claim row matches")
		}
		requireSingleStatement(t, rec, "WHERE id = $1 AND status = 'RUNNING' AND claim_epoch = $2", "task-1", int64(5))
	})

	t.Run("a sync heartbeat also binds the pass generation", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		if _, err := NewTaskStore(database).HeartbeatSyncTaskClaim(context.Background(), Lease{ID: "task-1", Epoch: 5}, 9); err != nil {
			t.Fatalf("HeartbeatSyncTaskClaim() error: %v", err)
		}
		requireSingleStatement(t, rec, "t.pass_generation = $3", "task-1", int64(5), int64(9))
	})

	t.Run("a zero lease is rejected without touching the database", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		store := NewTaskStore(database)

		if err := store.TransitionClaimedTask(context.Background(), Lease{}, "PENDING"); err == nil {
			t.Error("TransitionClaimedTask() accepted a zero lease")
		}
		if _, err := store.HeartbeatTaskClaim(context.Background(), Lease{}); err == nil {
			t.Error("HeartbeatTaskClaim() accepted a zero lease")
		}
		if len(rec.statements) != 0 {
			t.Fatalf("recorded %d statements, want 0", len(rec.statements))
		}
	})

	t.Run("checksum verification marks only the given task", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		if err := NewTaskStore(database).MarkTaskChecksumVerified(context.Background(), "task-1", "abc"); err != nil {
			t.Fatalf("MarkTaskChecksumVerified() error: %v", err)
		}
		requireSingleStatement(t, rec, "SET checksum_verified = TRUE", "task-1", "abc")
	})
}

func TestSyncStoreWorkerCallsKeepTheirGenerationFences(t *testing.T) {
	t.Run("a status change is scoped to the run generation", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		var store SyncStore = NewSyncStore(database)

		msg := "auth failed"
		if err := store.UpdateSyncJobStatusForGeneration("sync-1", 3, "FAILED", &msg); err != nil {
			t.Fatalf("UpdateSyncJobStatusForGeneration() error: %v", err)
		}
		requireSingleStatement(t, rec, "WHERE id = $3 AND run_generation = $4", "FAILED", sql.NullString{String: msg, Valid: true}.String, "sync-1", int64(3))
	})

	t.Run("live bytes only count while the generation is running", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		if err := NewSyncStore(database).AddSyncJobLiveBytesForGeneration(context.Background(), "sync-1", 3, 256); err != nil {
			t.Fatalf("AddSyncJobLiveBytesForGeneration() error: %v", err)
		}
		requireSingleStatement(t, rec, "run_generation = $3 AND status = 'RUNNING'", int64(256), "sync-1", int64(3))
	})

	t.Run("a non-positive byte delta never reaches the database", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		if err := NewSyncStore(database).AddSyncJobLiveBytesForGeneration(context.Background(), "sync-1", 3, 0); err != nil {
			t.Fatalf("AddSyncJobLiveBytesForGeneration() error: %v", err)
		}
		if len(rec.statements) != 0 {
			t.Fatalf("recorded %d statements, want 0", len(rec.statements))
		}
	})

	t.Run("verification lease renewal is fenced by both generations", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		renewed, err := NewSyncStore(database).RenewSyncJobVerificationLease(context.Background(), "sync-1", 3, 8)
		if err != nil {
			t.Fatalf("RenewSyncJobVerificationLease() error: %v", err)
		}
		if !renewed {
			t.Error("RenewSyncJobVerificationLease() = false, want true when one row matches")
		}
		requireSingleStatement(t, rec, "AND verification_generation = $3 AND verification_lease_until > NOW()", "sync-1", int64(3), int64(8))
	})

	t.Run("verification lease release is fenced by the verification generation", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		if err := NewSyncStore(database).ReleaseSyncJobVerificationLease(context.Background(), "sync-1", 8); err != nil {
			t.Fatalf("ReleaseSyncJobVerificationLease() error: %v", err)
		}
		requireSingleStatement(t, rec, "status = 'VERIFYING' AND verification_generation = $2", "sync-1", int64(8))
	})
}

func TestSyncStoreAPICallsStayOwnerAndStateScoped(t *testing.T) {
	t.Run("ownership check binds the job and the owner", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		rec.queryColumns = []string{"exists"}
		rec.queryRow = []driver.Value{true}

		owned, err := NewSyncStore(database).VerifySyncJobOwnershipContext(context.Background(), "sync-1", "user-1")
		if err != nil {
			t.Fatalf("VerifySyncJobOwnershipContext() error: %v", err)
		}
		if !owned {
			t.Error("VerifySyncJobOwnershipContext() = false, want true")
		}
		requireSingleStatement(t, rec, "FROM sync_jobs WHERE id = $1 AND user_id = $2", "sync-1", "user-1")
	})

	t.Run("owned lookup binds both the job and the owner", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		_, _ = NewSyncStore(database).GetOwnedSyncJobContext(context.Background(), "sync-1", "user-1")
		requireSingleStatement(t, rec, "user_id = $2", "sync-1", "user-1")
	})

	t.Run("failing a job only touches one that is still in flight", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		failed, err := NewSyncStore(database).FailSyncJobIfActive(context.Background(), "sync-1", "refresh failed")
		if err != nil {
			t.Fatalf("FailSyncJobIfActive() error: %v", err)
		}
		if !failed {
			t.Error("FailSyncJobIfActive() = false, want true when a row changed")
		}
		requireSingleStatement(t, rec, "WHERE id = $2 AND status IN ('INDEXING', 'RUNNING', 'VERIFYING')", "refresh failed", "sync-1")
	})

	t.Run("threads update is keyed by job id", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		if err := NewSyncStore(database).UpdateSyncJobThreads("sync-1", 6); err != nil {
			t.Fatalf("UpdateSyncJobThreads() error: %v", err)
		}
		requireSingleStatement(t, rec, "SET threads = $1", int64(6), "sync-1")
	})
}
