package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"backend/internal/backuprepo"
	"backend/internal/db"
	"backend/internal/observability"
)

func TestNewCoordinatorValidatesPackWriterLimit(t *testing.T) {
	for _, limit := range []int{0, 5} {
		if _, err := NewCoordinator(&sql.DB{}, "key", limit); err == nil {
			t.Errorf("NewCoordinator(limit=%d) succeeded", limit)
		}
	}
	coordinator, err := NewCoordinator(&sql.DB{}, "key", 2)
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	if cap(coordinator.packWriterSlots) != 2 {
		t.Fatalf("pack writer capacity = %d, want 2", cap(coordinator.packWriterSlots))
	}
}

func TestRepositoryPath(t *testing.T) {
	tests := []struct {
		name string
		root string
		want string
		err  bool
	}{
		{name: "nested repository", root: "clumoove/repositories/123", want: "clumoove/repositories/123/packs/a.pack"},
		{name: "absolute repository", root: "/backups/123", want: "/backups/123/packs/a.pack"},
		{name: "reject traversal", root: "backups/../other", err: true},
		{name: "reject backslash", root: `backups\other`, err: true},
		{name: "reject empty", root: "", err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repositoryPath(tt.root, "packs", "a.pack")
			if (err != nil) != tt.err {
				t.Fatalf("repositoryPath() error = %v, want error=%v", err, tt.err)
			}
			if got != tt.want {
				t.Fatalf("repositoryPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSafeRemotePath(t *testing.T) {
	for _, value := range []string{"backup/../outside", `backup\outside`, ""} {
		if err := safeRemotePath(value); err == nil {
			t.Errorf("safeRemotePath(%q) succeeded", value)
		}
	}
}

func TestFailureCodeForState(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  string
	}{
		{name: "scan", state: "SCANNING", want: "BACKUP_SCAN_FAILED"},
		{name: "run", state: "RUNNING", want: "BACKUP_RUN_FAILED"},
		{name: "verify", state: "VERIFYING", want: "BACKUP_VERIFICATION_FAILED"},
		{name: "unknown defaults to run", state: "UNKNOWN", want: "BACKUP_RUN_FAILED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := failureCodeForState(test.state); got != test.want {
				t.Fatalf("failureCodeForState(%q) = %q, want %q", test.state, got, test.want)
			}
		})
	}
}

func TestPackBuilderDeduplicatesPendingBlocksAndPreservesCatalogIDs(t *testing.T) {
	builder := newPackBuilder(nil, nil, nil)
	entry := backuprepo.Entry{Data: []byte("repeated backup data")}
	entry.Hash = sha256.Sum256(entry.Data)
	hashKey := string(entry.Hash[:])

	if err := builder.add(context.Background(), entry); err != nil {
		t.Fatalf("first add() error = %v", err)
	}
	if err := builder.add(context.Background(), entry); err != nil {
		t.Fatalf("duplicate add() error = %v", err)
	}
	if len(builder.entries) != 1 {
		t.Fatalf("pending entries = %d, want 1", len(builder.entries))
	}
	if !builder.hasPendingBlock(hashKey) {
		t.Fatal("pending block was not tracked")
	}

	builder.ids[hashKey] = "catalog-block-id"
	if got := builder.resolveBlockID(hashKey); got != "catalog-block-id" {
		t.Fatalf("resolveBlockID(hash) = %q, want catalog ID", got)
	}
	if got := builder.resolveBlockID("existing-catalog-block-id"); got != "existing-catalog-block-id" {
		t.Fatalf("resolveBlockID(existing ID) = %q, want unchanged ID", got)
	}
}

func TestBackupRunLoggerIncludesCorrelationFields(t *testing.T) {
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)))
	job := &db.BackupJob{ID: "backup-job"}
	run := &db.BackupRun{ID: "backup-run", Generation: 7}

	backupRunLogger(ctx, job, run).Info("backup_run_started")

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode log record: %v", err)
	}
	for key, want := range map[string]any{
		"component":     "backup",
		"backup_job_id": "backup-job",
		"backup_run_id": "backup-run",
		"generation":    float64(7),
	} {
		if got := record[key]; got != want {
			t.Errorf("log %s = %#v, want %#v", key, got, want)
		}
	}
}

func TestBackupFailureAttrsIncludesCodeAndClassifiedCause(t *testing.T) {
	attrs := backupFailureAttrs("BACKUP_CONNECTION_FAILED", errors.New("connection refused"))
	values := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		values[attr.Key] = attr.Value.String()
	}
	if values["error_code"] != "BACKUP_CONNECTION_FAILED" {
		t.Errorf("error_code = %q", values["error_code"])
	}
	if values["error"] == "" {
		t.Error("error attribute is missing")
	}
	if values["error_kind"] != "network" {
		t.Errorf("error_kind = %q, want network", values["error_kind"])
	}
}
