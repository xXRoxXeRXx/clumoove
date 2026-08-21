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
	"backend/internal/storage"
	"io"
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

type mockBackupTarget struct {
	listings map[string][]storage.CloudResource
}

func (m *mockBackupTarget) Close() error                              { return nil }
func (m *mockBackupTarget) Connect(ctx context.Context) (bool, error) { return true, nil }
func (m *mockBackupTarget) InspectResource(ctx context.Context, resourceType, path string) (storage.CloudResource, error) {
	return storage.CloudResource{}, errors.New("not implemented")
}
func (m *mockBackupTarget) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (m *mockBackupTarget) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	return errors.New("not implemented")
}
func (m *mockBackupTarget) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	return errors.New("not implemented")
}
func (m *mockBackupTarget) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	return errors.New("not implemented")
}
func (m *mockBackupTarget) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	return "", errors.New("not implemented")
}
func (m *mockBackupTarget) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	return errors.New("not implemented")
}
func (m *mockBackupTarget) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	return errors.New("not implemented")
}
func (m *mockBackupTarget) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	return errors.New("not implemented")
}
func (m *mockBackupTarget) SupportsAtomicRename() bool { return true }
func (m *mockBackupTarget) VerificationMode() storage.VerificationMode {
	return storage.VerificationSizeOnly
}
func (m *mockBackupTarget) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]storage.CloudResource, error) {
	return m.listings[dirPath], nil
}
func (m *mockBackupTarget) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	return false, 0, nil
}

func TestEnsureDedicatedTarget(t *testing.T) {
	const (
		targetDir    = "/backups"
		repoID       = "38256494-2dce-416a-b4fd-46f5b25de01c"
		container    = "/backups/.clumoove-backup"
		repoRoot     = "/backups/.clumoove-backup/38256494-2dce-416a-b4fd-46f5b25de01c"
	)

	t.Run("empty target directory succeeds", func(t *testing.T) {
		target := &mockBackupTarget{
			listings: map[string][]storage.CloudResource{
				targetDir: {},
			},
		}
		if err := ensureDedicatedTarget(context.Background(), target, targetDir, repoID); err != nil {
			t.Fatalf("ensureDedicatedTarget() error = %v, want nil", err)
		}
	})

	t.Run("webdav trailing slashes on directories succeed", func(t *testing.T) {
		target := &mockBackupTarget{
			listings: map[string][]storage.CloudResource{
				targetDir: {
					{Path: container + "/", Name: ".clumoove-backup", IsDir: true},
				},
				container: {
					{Path: repoRoot + "/", Name: repoID, IsDir: true},
				},
			},
		}
		if err := ensureDedicatedTarget(context.Background(), target, targetDir, repoID); err != nil {
			t.Fatalf("ensureDedicatedTarget() with trailing slashes error = %v, want nil", err)
		}
	})

	t.Run("clean paths without trailing slashes succeed", func(t *testing.T) {
		target := &mockBackupTarget{
			listings: map[string][]storage.CloudResource{
				targetDir: {
					{Path: container, Name: ".clumoove-backup", IsDir: true},
				},
				container: {
					{Path: repoRoot, Name: repoID, IsDir: true},
				},
			},
		}
		if err := ensureDedicatedTarget(context.Background(), target, targetDir, repoID); err != nil {
			t.Fatalf("ensureDedicatedTarget() clean paths error = %v, want nil", err)
		}
	})

	t.Run("non-empty directory with unexpected file fails", func(t *testing.T) {
		target := &mockBackupTarget{
			listings: map[string][]storage.CloudResource{
				targetDir: {
					{Path: "/backups/existing.docx", Name: "existing.docx", IsDir: false},
				},
			},
		}
		err := ensureDedicatedTarget(context.Background(), target, targetDir, repoID)
		if err == nil || err.Error() != "backup target directory is not empty" {
			t.Fatalf("ensureDedicatedTarget() error = %v, want 'backup target directory is not empty'", err)
		}
	})

	t.Run("container with multiple repos fails dedicated check", func(t *testing.T) {
		target := &mockBackupTarget{
			listings: map[string][]storage.CloudResource{
				targetDir: {
					{Path: container, Name: ".clumoove-backup", IsDir: true},
				},
				container: {
					{Path: repoRoot, Name: repoID, IsDir: true},
					{Path: container + "/other-repo", Name: "other-repo", IsDir: true},
				},
			},
		}
		err := ensureDedicatedTarget(context.Background(), target, targetDir, repoID)
		if err == nil || err.Error() != "backup target directory is not dedicated" {
			t.Fatalf("ensureDedicatedTarget() error = %v, want 'backup target directory is not dedicated'", err)
		}
	})

	t.Run("container belonging to another repo fails", func(t *testing.T) {
		target := &mockBackupTarget{
			listings: map[string][]storage.CloudResource{
				targetDir: {
					{Path: container, Name: ".clumoove-backup", IsDir: true},
				},
				container: {
					{Path: container + "/other-repo-id", Name: "other-repo-id", IsDir: true},
				},
			},
		}
		err := ensureDedicatedTarget(context.Background(), target, targetDir, repoID)
		if err == nil || err.Error() != "backup target directory belongs to another repository" {
			t.Fatalf("ensureDedicatedTarget() error = %v, want 'backup target directory belongs to another repository'", err)
		}
	})
}
