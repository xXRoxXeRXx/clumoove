package processor

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/indexer"
	"backend/internal/queue"
	"backend/internal/storage"
	"backend/internal/throttle"
)

type boxSHA1TargetProvider struct {
	files      map[string][]byte
	uploadOpts []storage.UploadOptions
}

func newBoxSHA1TargetProvider() *boxSHA1TargetProvider {
	return &boxSHA1TargetProvider{files: make(map[string][]byte)}
}

func (t *boxSHA1TargetProvider) StreamUpload(_ context.Context, _, filePath string, stream io.Reader, _ int64) error {
	data, err := io.ReadAll(stream)
	if err != nil {
		return err
	}
	t.files[filePath] = data
	return nil
}

func (t *boxSHA1TargetProvider) StreamUploadChunked(ctx context.Context, kind, filePath string, stream io.Reader, size int64, _ chan<- int64) error {
	return t.StreamUpload(ctx, kind, filePath, stream, size)
}

func (t *boxSHA1TargetProvider) StreamUploadWithOptions(ctx context.Context, kind, filePath string, stream io.Reader, size int64, opts storage.UploadOptions) (storage.UploadResult, error) {
	t.uploadOpts = append(t.uploadOpts, opts)
	return storage.UploadResult{}, t.StreamUpload(ctx, kind, filePath, stream, size)
}

func (t *boxSHA1TargetProvider) StreamUploadChunkedWithOptions(ctx context.Context, kind, filePath string, stream io.Reader, size int64, _ chan<- int64, opts storage.UploadOptions) (storage.UploadResult, error) {
	return t.StreamUploadWithOptions(ctx, kind, filePath, stream, size, opts)
}

func (t *boxSHA1TargetProvider) FileExists(_ context.Context, _, filePath string) (bool, int64, error) {
	data, ok := t.files[filePath]
	return ok, int64(len(data)), nil
}

func (t *boxSHA1TargetProvider) GetFileHash(_ context.Context, _, filePath string) (string, error) {
	data, ok := t.files[filePath]
	if !ok {
		return "", storage.ErrNotFound
	}
	sum := sha1.Sum(data)
	return "SHA1:" + hex.EncodeToString(sum[:]), nil
}

func (t *boxSHA1TargetProvider) VerificationMode() storage.VerificationMode {
	return storage.VerificationCryptographicHash
}

func (t *boxSHA1TargetProvider) Connect(context.Context) (bool, error) { return true, nil }
func (t *boxSHA1TargetProvider) Close() error                       { return nil }
func (t *boxSHA1TargetProvider) GetDirectoryListing(context.Context, string, string) ([]storage.CloudResource, error) {
	return nil, nil
}
func (t *boxSHA1TargetProvider) CreateDirectory(context.Context, string, string) error { return nil }
func (t *boxSHA1TargetProvider) CreateParentDirectories(context.Context, string, string) error {
	return nil
}
func (t *boxSHA1TargetProvider) DeleteFile(_ context.Context, _, filePath string) error {
	delete(t.files, filePath)
	return nil
}
func (t *boxSHA1TargetProvider) RenameFile(_ context.Context, _, oldPath, newPath string) error {
	data, ok := t.files[oldPath]
	if !ok {
		return storage.ErrNotFound
	}
	delete(t.files, oldPath)
	t.files[newPath] = data
	return nil
}
func (t *boxSHA1TargetProvider) SupportsAtomicRename() bool { return true }

func TestBoxMigrationToSHA1TargetEndToEnd(t *testing.T) {
	const boxToken = "box-e2e-token"
	payload := []byte("Box -> Clumoove end-to-end SHA-1 verified payload 2026!")
	wantSum := sha1.Sum(payload)
	wantSHA1 := "SHA1:" + hex.EncodeToString(wantSum[:])

	var mu sync.Mutex
	var dlAuthHeaders []string
	var serveCorrupted bool

	dlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dlAuthHeaders = append(dlAuthHeaders, r.Header.Get("Authorization"))
		corrupt := serveCorrupted
		mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "authorization header leaked", http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/dl/file-101" {
			http.NotFound(w, r)
			return
		}
		body := payload
		if corrupt {
			body = append([]byte(nil), payload...)
			body[0] ^= 0xff
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer dlServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+boxToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/2.0/folders/0/items":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"entries": []map[string]any{
					{
						"id":                  "folder-10",
						"type":                "folder",
						"name":                "Finance",
						"modified_at":         "2026-02-01T10:00:00Z",
						"content_modified_at": "2026-02-01T10:00:00Z",
					},
				},
				"limit":       1000,
				"next_marker": "",
			})
		case "/2.0/folders/folder-10/items":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"entries": []map[string]any{
					{
						"id":                  "file-101",
						"type":                "file",
						"name":                "ledger.csv",
						"size":                len(payload),
						"sha1":                strings.ToUpper(hex.EncodeToString(wantSum[:])),
						"modified_at":         "2026-02-02T12:00:00Z",
						"content_modified_at": "2026-02-02T11:30:00Z",
					},
				},
				"limit":       1000,
				"next_marker": "",
			})
		case "/2.0/files/file-101/content":
			http.Redirect(w, r, dlServer.URL+"/dl/file-101", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiServer.Close()

	boxSrc, err := storage.NewBoxTestProvider(boxToken, apiServer.URL+"/2.0", apiServer.URL+"/api/2.0", apiServer.Client())
	if err != nil {
		t.Fatalf("NewBoxTestProvider: %v", err)
	}
	defer boxSrc.Close()

	if mode := boxSrc.VerificationMode(); mode != storage.VerificationCryptographicHash {
		t.Fatalf("boxSrc.VerificationMode() = %q, want %q", mode, storage.VerificationCryptographicHash)
	}

	ctx := context.Background()
	listed, err := boxSrc.GetDirectoryListing(ctx, "files", "/Finance")
	if err != nil {
		t.Fatalf("GetDirectoryListing(/Finance): %v", err)
	}
	if len(listed) != 1 || listed[0].Path != "/Finance/ledger.csv" || listed[0].Hash != wantSHA1 {
		t.Fatalf("listed = %+v, want ledger.csv with hash %q", listed, wantSHA1)
	}

	hashFromAPI, err := boxSrc.GetFileHash(ctx, "files", "/Finance/ledger.csv")
	if err != nil || hashFromAPI != wantSHA1 {
		t.Fatalf("GetFileHash = (%q, %v), want %q", hashFromAPI, err, wantSHA1)
	}

	target := newBoxSHA1TargetProvider()
	transfer, err := runTransferCore(transferRequest{
		Context:          ctx,
		Source:           boxSrc,
		Target:           target,
		SourceProvider:   "box",
		TargetProvider:   "nextcloud",
		ResourceType:     "files",
		SourcePath:       listed[0].Path,
		TargetPath:       "/migrated/Finance/ledger.csv",
		VerificationPath: "/migrated/Finance/ledger.csv",
		FileSize:         listed[0].Size,
		SourceHash:       listed[0].Hash,
		UploadOptions: storage.UploadOptions{
			Metadata: storage.FileMetadata{ModifiedTime: listed[0].LastModified},
		},
	})
	if err != nil {
		t.Fatalf("runTransferCore(box -> target): %v", err)
	}
	if transfer.SourceAlgorithm != "SHA1" || transfer.WorkerHash() != wantSHA1 {
		t.Fatalf("transfer result = (%s, %s), want (SHA1, %s)", transfer.SourceAlgorithm, transfer.WorkerHash(), wantSHA1)
	}
	if !bytes.Equal(target.files["/migrated/Finance/ledger.csv"], payload) {
		t.Fatalf("target bytes mismatch: got %q, want %q", target.files["/migrated/Finance/ledger.csv"], payload)
	}
	if len(target.uploadOpts) != 1 || target.uploadOpts[0].Checksum != wantSHA1 {
		t.Fatalf("target uploadOpts = %+v, want Checksum %q", target.uploadOpts, wantSHA1)
	}

	task := &db.Task{
		ResourceType: "files",
		FilePath:     listed[0].Path,
		SourceHash:   sql.NullString{String: listed[0].Hash, Valid: true},
		WorkerHash:   sql.NullString{String: transfer.WorkerHash(), Valid: true},
	}
	if !tryInlineHashVerify(ctx, task, target, "files", "/migrated/Finance/ledger.csv", transfer.UploadReceipt.TargetResourceID) {
		t.Fatal("tryInlineHashVerify() = false, want true for matching SHA-1")
	}
	if !task.ChecksumVerified || task.TargetHash.String != wantSHA1 {
		t.Fatalf("task verification = (verified=%v, targetHash=%q), want (true, %q)", task.ChecksumVerified, task.TargetHash.String, wantSHA1)
	}

	// Corrupted bytes from download host must fail SHA-1 verification against Box's indexed SHA-1.
	mu.Lock()
	serveCorrupted = true
	mu.Unlock()
	corruptTransfer, err := runTransferCore(transferRequest{
		Context:          ctx,
		Source:           boxSrc,
		Target:           target,
		SourceProvider:   "box",
		TargetProvider:   "nextcloud",
		ResourceType:     "files",
		SourcePath:       listed[0].Path,
		TargetPath:       "/migrated/Finance/corrupt.csv",
		VerificationPath: "/migrated/Finance/corrupt.csv",
		FileSize:         listed[0].Size,
		SourceHash:       listed[0].Hash,
	})
	if err != nil {
		t.Fatalf("runTransferCore(corrupt): %v", err)
	}
	targetHash, err := target.GetFileHash(ctx, "files", "/migrated/Finance/corrupt.csv")
	if err != nil {
		t.Fatalf("target.GetFileHash(corrupt): %v", err)
	}
	if cmp := storage.CompareTargetHash(listed[0].Hash, corruptTransfer.WorkerHash(), targetHash); cmp.Matched {
		t.Fatalf("expected corrupted stream to fail SHA-1 comparison against Box source hash, got %+v", cmp)
	}
	_ = fmt.Sprintf("verified")

	mu.Lock()
	auths := append([]string(nil), dlAuthHeaders...)
	mu.Unlock()
	for i, hdr := range auths {
		if hdr != "" {
			t.Fatalf("download request %d carried Authorization header %q", i, hdr)
		}
	}
	if time.Since(listed[0].LastModified) < 0 {
		t.Log("future mtime")
	}
}

type boxMigTestTask struct {
	id           string
	migrationID  string
	resourceType string
	filePath     string
	fileSize     int64
	sourceHash   string
	workerHash   string
	targetHash   string
	verified     bool
	status       string
	attempts     int64
	errorMessage string
	hasNextRetry bool
	claimEpoch   int64
	metadata     []byte
}

type boxMigTestState struct {
	mu                  sync.Mutex
	migID               string
	status              string
	selectedPaths       []string
	encryptedPass       string
	encryptedTargetPass string
	totalFiles          int64
	totalBytes     int64
	processedFiles int64
	processedBytes int64
	liveBytes      int64
	verifiedFiles  int64
	skippedFiles   int64
	failedFiles    int64
	tasks          []*boxMigTestTask
	indexingErrors []db.ErrorListItem
	nextTaskSeq    int
}

var (
	boxMigDriverOnce sync.Once
	boxMigDriverSeq  atomic.Uint64
	boxMigStatesMu   sync.Mutex
	boxMigStates     = make(map[string]*boxMigTestState)
)

func newBoxMigTestDB(t *testing.T, state *boxMigTestState) *sql.DB {
	t.Helper()
	boxMigDriverOnce.Do(func() { sql.Register("box-migration-e2e-test", boxMigDriver{}) })
	dsn := fmt.Sprintf("box-mig-%d", boxMigDriverSeq.Add(1))
	boxMigStatesMu.Lock()
	boxMigStates[dsn] = state
	boxMigStatesMu.Unlock()
	database, err := sql.Open("box-migration-e2e-test", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		boxMigStatesMu.Lock()
		delete(boxMigStates, dsn)
		boxMigStatesMu.Unlock()
	})
	return database
}

type boxMigDriver struct{}

func (boxMigDriver) Open(dsn string) (driver.Conn, error) {
	boxMigStatesMu.Lock()
	state := boxMigStates[dsn]
	boxMigStatesMu.Unlock()
	if state == nil {
		return nil, errors.New("missing box migration test state")
	}
	return boxMigConn{state: state}, nil
}

type boxMigConn struct{ state *boxMigTestState }

func (boxMigConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not implemented") }
func (boxMigConn) Close() error                        { return nil }
func (boxMigConn) Begin() (driver.Tx, error)           { return boxMigTx{}, nil }
func (boxMigConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return boxMigTx{}, nil
}

func (c boxMigConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	switch {
	case strings.Contains(query, "INSERT INTO tasks") && strings.Contains(query, "FROM (VALUES"):
		count := 0
		for i := 2; i < len(args); i += 7 {
			c.state.nextTaskSeq++
			resType, _ := args[i].Value.(string)
			filePath, _ := args[i+1].Value.(string)
			fileSize, _ := args[i+2].Value.(int64)
			srcHash, _ := args[i+3].Value.(string)
			status, _ := args[i+4].Value.(string)
			meta, _ := args[i+5].Value.([]byte)
			errMsg, _ := args[i+6].Value.(string)
			c.state.tasks = append(c.state.tasks, &boxMigTestTask{
				id:           fmt.Sprintf("task-%d", c.state.nextTaskSeq),
				migrationID:  c.state.migID,
				resourceType: resType,
				filePath:     filePath,
				fileSize:     fileSize,
				sourceHash:   srcHash,
				status:       status,
				errorMessage: errMsg,
				claimEpoch:   1,
				metadata:     meta,
			})
			count++
		}
		return driver.RowsAffected(int64(count)), nil
	case strings.Contains(query, "INSERT INTO indexing_errors"):
		for i := 2; i < len(args); i += 4 {
			resType, _ := args[i-1].Value.(string)
			p, _ := args[i].Value.(string)
			msg, _ := args[i+1].Value.(string)
			c.state.indexingErrors = append(c.state.indexingErrors, db.ErrorListItem{
				ID:           fmt.Sprintf("idx-err-%d", len(c.state.indexingErrors)+1),
				Kind:         "indexing",
				ResourceType: resType,
				Path:         p,
				Status:       "INDEXING",
				ErrorMessage: msg,
			})
		}
		return driver.RowsAffected(int64(len(args) / 4)), nil
	case strings.Contains(query, "UPDATE migrations SET total_files ="):
		c.state.totalFiles, _ = args[0].Value.(int64)
		c.state.totalBytes, _ = args[1].Value.(int64)
		return driver.RowsAffected(1), nil
	case strings.Contains(query, "SET status = 'RUNNING'") && strings.Contains(query, "discovery_complete = TRUE"):
		c.state.status = "RUNNING"
		return driver.RowsAffected(1), nil
	case strings.Contains(query, "WITH task_stats AS"):
		var done, skip, fail int64
		var doneBytes int64
		for _, t := range c.state.tasks {
			switch t.status {
			case "COMPLETED":
				done++
				doneBytes += t.fileSize
			case "SKIPPED":
				skip++
			case "FAILED":
				fail++
			}
		}
		c.state.processedFiles = done + skip + fail
		c.state.processedBytes = doneBytes
		c.state.liveBytes = doneBytes
		c.state.skippedFiles = skip
		c.state.failedFiles = fail
		return driver.RowsAffected(1), nil
	case strings.Contains(query, "UPDATE tasks SET status = $1, attempts = $2"):
		status, _ := args[0].Value.(string)
		attempts, _ := args[1].Value.(int64)
		errMsg, _ := args[2].Value.(string)
		_, hasNextRetry := args[3].Value.(time.Time)
		workerHash, _ := args[4].Value.(string)
		srcHash, _ := args[5].Value.(string)
		tgtHash, _ := args[6].Value.(string)
		verified, _ := args[7].Value.(bool)
		taskID, _ := args[8].Value.(string)
		for _, t := range c.state.tasks {
			if t.id == taskID && t.status == "RUNNING" {
				t.status = status
				t.attempts = attempts
				t.errorMessage = errMsg
				t.hasNextRetry = hasNextRetry
				t.workerHash = workerHash
				t.sourceHash = srcHash
				t.targetHash = tgtHash
				t.verified = verified
				return driver.RowsAffected(1), nil
			}
		}
		return driver.RowsAffected(0), nil
	case strings.Contains(query, "UPDATE tasks") && strings.Contains(query, "SET status = $1, attempts = $2"):
		status, _ := args[0].Value.(string)
		attempts, _ := args[1].Value.(int64)
		errMsg, _ := args[2].Value.(string)
		_, hasNextRetry := args[3].Value.(time.Time)
		taskID, _ := args[8].Value.(string)
		for _, t := range c.state.tasks {
			if t.id == taskID && t.status == "RUNNING" {
				t.status = status
				t.attempts = attempts
				t.errorMessage = errMsg
				t.hasNextRetry = hasNextRetry
				return driver.RowsAffected(1), nil
			}
		}
		return driver.RowsAffected(0), nil
	case strings.Contains(query, "UPDATE migrations SET processed_files = processed_files +"):
		filesDelta, _ := args[0].Value.(int64)
		bytesDelta, _ := args[1].Value.(int64)
		verifiedDelta, _ := args[3].Value.(int64)
		skippedDelta, _ := args[4].Value.(int64)
		failedDelta, _ := args[5].Value.(int64)
		c.state.processedFiles += filesDelta
		c.state.processedBytes += bytesDelta
		c.state.verifiedFiles += verifiedDelta
		c.state.skippedFiles += skippedDelta
		c.state.failedFiles += failedDelta
		return driver.RowsAffected(1), nil
	default:
		return driver.RowsAffected(1), nil
	}
}

func (c boxMigConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	switch {
	case strings.Contains(query, "SELECT id, user_id"):
		now := time.Now()
		pathsJSON, _ := json.Marshal(c.state.selectedPaths)
		return &boxMigRows{
			cols: []string{
				"id", "user_id", "source_profile_id", "target_profile_id", "source_url", "source_username", "source_password_encrypted", "source_provider",
				"source_refresh_token_encrypted", "source_token_expires_at", "source_mega_session_id_encrypted", "source_mega_master_key_encrypted",
				"target_url", "target_username", "target_password_encrypted", "target_provider",
				"target_refresh_token_encrypted", "target_token_expires_at", "target_mega_session_id_encrypted", "target_mega_master_key_encrypted",
				"status", "conflict_strategy", "total_files", "total_bytes", "processed_files",
				"processed_bytes", "live_bytes", "verified_files", "skipped_files", "failed_files", "error_message",
				"discovery_complete", "discovery_generation", "indexing_lease_until",
				"created_at", "updated_at", "target_dir", "threads", "bandwidth_limit_mbps",
				"picker_session_id", "selected_paths", "selected_calendars", "selected_contacts",
			},
			rows: [][]driver.Value{{
				c.state.migID, "user-1", nil, nil, "https://api.box.com", "box@example.test", c.state.encryptedPass, "box",
				nil, nil, "", "",
				"https://nc.example.test", "u", c.state.encryptedTargetPass, "nextcloud",
				nil, nil, "", "",
				c.state.status, "SKIP", c.state.totalFiles, c.state.totalBytes, c.state.processedFiles,
				c.state.processedBytes, c.state.liveBytes, c.state.verifiedFiles, c.state.skippedFiles, c.state.failedFiles, nil,
				false, int64(1), nil,
				now, now, "/migrated", int64(4), int64(0),
				"", pathsJSON, []byte("[]"), []byte("[]"),
			}},
		}, nil
	case strings.Contains(query, "UPDATE migrations SET indexing_lease_until"):
		return &boxMigRows{cols: []string{"ok"}, rows: [][]driver.Value{{int64(1)}}}, nil
	case strings.Contains(query, "SELECT resource_type, file_path"):
		return &boxMigRows{cols: []string{"resource_type", "file_path", "action"}}, nil
	case strings.Contains(query, "SELECT COUNT(*), COALESCE(SUM(file_size), 0) FROM tasks"):
		var sum int64
		for _, t := range c.state.tasks {
			sum += t.fileSize
		}
		return &boxMigRows{cols: []string{"count", "sum"}, rows: [][]driver.Value{{int64(len(c.state.tasks)), sum}}}, nil
	case strings.Contains(query, "INSERT INTO tasks"):
		c.state.nextTaskSeq++
		resType, _ := args[1].Value.(string)
		filePath, _ := args[2].Value.(string)
		fileSize, _ := args[3].Value.(int64)
		status, _ := args[4].Value.(string)
		meta, _ := args[5].Value.([]byte)
		srcHash, _ := args[6].Value.(string)
		c.state.tasks = append(c.state.tasks, &boxMigTestTask{
			id:           fmt.Sprintf("task-%d", c.state.nextTaskSeq),
			migrationID:  c.state.migID,
			resourceType: resType,
			filePath:     filePath,
			fileSize:     fileSize,
			sourceHash:   srcHash,
			status:       status,
			claimEpoch:   1,
			metadata:     meta,
		})
		now := time.Now()
		return &boxMigRows{cols: []string{"id", "created_at", "updated_at"}, rows: [][]driver.Value{{fmt.Sprintf("task-%d", c.state.nextTaskSeq), now, now}}}, nil
	case strings.Contains(query, "SELECT status FROM migrations WHERE id ="):
		return &boxMigRows{cols: []string{"status"}, rows: [][]driver.Value{{c.state.status}}}, nil
	case strings.Contains(query, "FROM tasks WHERE id ="):
		taskID, _ := args[0].Value.(string)
		now := time.Now()
		for _, t := range c.state.tasks {
			if t.id == taskID {
				var errMsg any
				if t.errorMessage != "" {
					errMsg = t.errorMessage
				}
				var srcHash, wrkHash, tgtHash any
				if t.sourceHash != "" {
					srcHash = t.sourceHash
				}
				if t.workerHash != "" {
					wrkHash = t.workerHash
				}
				if t.targetHash != "" {
					tgtHash = t.targetHash
				}
				return &boxMigRows{
					cols: []string{"id", "migration_id", "sync_job_id", "prerequisite_task_id", "resource_type", "file_path", "resolved_target_path", "file_size", "status", "attempts", "error_message", "next_retry_at", "worker_hash", "claim_epoch", "pass_generation", "source_hash", "target_hash", "checksum_verified", "metadata", "created_at", "updated_at"},
					rows: [][]driver.Value{{
						t.id, t.migrationID, "", "", t.resourceType, t.filePath, "", t.fileSize, t.status,
						t.attempts, errMsg, nil, wrkHash, t.claimEpoch, int64(0), srcHash, tgtHash,
						t.verified, t.metadata, now, now,
					}},
				}, nil
			}
		}
		return &boxMigRows{cols: []string{"id"}}, nil
	case strings.Contains(query, "WITH errors AS MATERIALIZED"):
		now := time.Now()
		var rows [][]driver.Value
		for _, t := range c.state.tasks {
			if t.status == "FAILED" {
				rows = append(rows, []driver.Value{
					t.id, "transfer", t.resourceType, t.filePath, t.status, t.attempts,
					t.errorMessage, t.metadata, now, int64(0),
				})
			}
		}
		for _, ie := range c.state.indexingErrors {
			rows = append(rows, []driver.Value{
				ie.ID, ie.Kind, ie.ResourceType, ie.Path, ie.Status, int64(0),
				ie.ErrorMessage, []byte("{}"), now, int64(0),
			})
		}
		total := int64(len(rows))
		for i := range rows {
			rows[i][9] = total
		}
		return &boxMigRows{
			cols: []string{"id", "kind", "resource_type", "path", "status", "attempts", "error_message", "metadata", "occurred_at", "total"},
			rows: rows,
		}, nil
	default:
		return &boxMigRows{cols: []string{"c1"}}, nil
	}
}

type boxMigTx struct{}

func (boxMigTx) Commit() error   { return nil }
func (boxMigTx) Rollback() error { return nil }

type boxMigRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *boxMigRows) Columns() []string { return r.cols }
func (r *boxMigRows) Close() error      { return nil }
func (r *boxMigRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

type boxScenarioItem struct {
	id              string
	typ             string
	name            string
	url             string
	content         []byte
	downloadBlocked bool
	blockedCode     string
}

func newFakeBoxScenarioServer(t *testing.T, token string, folders map[string][]boxScenarioItem) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var downloadHits atomic.Int32
	itemsByID := make(map[string]boxScenarioItem)
	for _, list := range folders {
		for _, it := range list {
			itemsByID[it.id] = it
		}
	}

	var dlServer *httptest.Server
	dlServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/dl/")
		it, ok := itemsByID[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(it.content)
	}))
	t.Cleanup(dlServer.Close)

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/2.0/users/me":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"1","type":"user","name":"Box User","login":"box@example.test"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/folders/") && strings.HasSuffix(r.URL.Path, "/items"):
			folderID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2.0/folders/"), "/items")
			list := folders[folderID]
			entries := make([]map[string]any, 0, len(list))
			for _, it := range list {
				sha1Hex := ""
				if it.typ == "file" && len(it.content) > 0 {
					sum := sha1.Sum(it.content)
					sha1Hex = hex.EncodeToString(sum[:])
				}
				entries = append(entries, map[string]any{
					"id":          it.id,
					"type":        it.typ,
					"name":        it.name,
					"size":        len(it.content),
					"sha1":        sha1Hex,
					"modified_at": "2026-02-01T10:00:00Z",
					"url":         it.url,
				})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries, "limit": 1000, "next_marker": ""})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/files/") && strings.HasSuffix(r.URL.Path, "/content"):
			downloadHits.Add(1)
			fileID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2.0/files/"), "/content")
			it, ok := itemsByID[fileID]
			if !ok || it.typ != "file" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if it.downloadBlocked {
				code := it.blockedCode
				if code == "" {
					code = "forbidden_by_policy"
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(fmt.Sprintf(`{"type":"error","status":403,"code":%q}`, code)))
				return
			}
			http.Redirect(w, r, dlServer.URL+"/dl/"+fileID, http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(apiServer.Close)
	return apiServer, &downloadHits
}

func TestBoxMigrationSkipsNotesCanvasWebLinksAndMigratesNormalFile(t *testing.T) {
	const boxToken = "box-mig-token"
	const secretKey = "01234567890123456789012345678901"
	encryptedPass, err := crypto.EncryptWithDomain(boxToken, secretKey, crypto.ConnectionCredentialDomain(true))
	if err != nil {
		t.Fatalf("EncryptWithDomain: %v", err)
	}
	encryptedTargetPass, err := crypto.EncryptWithDomain("target-pass", secretKey, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("EncryptWithDomain(target): %v", err)
	}

	normalContent := []byte("real deliverable content")
	apiServer, downloadHits := newFakeBoxScenarioServer(t, boxToken, map[string][]boxScenarioItem{
		"0": {{id: "folder-ws", typ: "folder", name: "Workspace"}},
		"folder-ws": {
			{id: "note-1", typ: "file", name: "Meeting.boxnote", content: []byte(`{"doc":"note"}`)},
			{id: "canvas-1", typ: "file", name: "Board.boxcanvas", content: []byte(`{"canvas":1}`)},
			{id: "link-1", typ: "web_link", name: "Portal", url: "https://example.test/portal"},
			{id: "file-1", typ: "file", name: "report.txt", content: normalContent},
		},
	})

	state := &boxMigTestState{
		migID:               "mig-box-skip",
		status:              "INDEXING",
		selectedPaths:       []string{"/Workspace"},
		encryptedPass:       encryptedPass,
		encryptedTargetPass: encryptedTargetPass,
	}
	database := newBoxMigTestDB(t, state)

	idx, err := indexer.NewIndexer(database, secretKey, nil, time.Minute)
	if err != nil {
		t.Fatalf("NewIndexer: %v", err)
	}
	idx.SetSourceFactory(func(_ context.Context, _, _, _, password string, _ ...storage.ProviderOption) (indexer.IndexSource, error) {
		return storage.NewBoxTestProvider(password, apiServer.URL+"/2.0", apiServer.URL+"/api/2.0", apiServer.Client())
	})
	idx.Start(context.Background(), state.migID)

	target := newBoxSHA1TargetProvider()
	proc, err := NewProcessor(database, nil, Config{
		WorkerID:      "box-worker",
		SecretKey:     secretKey,
		MaxThreads:    2,
		ProviderSlots: make(chan struct{}, 2),
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	pool := newProviderPool()
	defer pool.Close()
	pool.factory = func(_ context.Context, providerType, _, _, password string, _ ...storage.ProviderOption) (PooledProvider, error) {
		if providerType == "box" {
			return storage.NewBoxTestProvider(password, apiServer.URL+"/2.0", apiServer.URL+"/api/2.0", apiServer.Client())
		}
		return target, nil
	}

	// Execute any PENDING tasks (only report.txt should be PENDING; the other 3 were indexed as SKIPPED).
	state.mu.Lock()
	var pendingIDs []string
	tasksByPath := make(map[string]*boxMigTestTask, len(state.tasks))
	for _, tk := range state.tasks {
		tasksByPath[tk.filePath] = tk
		if tk.status == "PENDING" {
			pendingIDs = append(pendingIDs, tk.id)
		}
	}
	state.mu.Unlock()

	if len(pendingIDs) != 2 {
		t.Fatalf("pending task count = %d, want 2 (mkdir + normal file); tasks = %+v", len(pendingIDs), tasksByPath)
	}
	for _, taskID := range pendingIDs {
		state.mu.Lock()
		for _, tk := range state.tasks {
			if tk.id == taskID {
				tk.status = "RUNNING"
			}
		}
		state.mu.Unlock()
		proc.dispatchClaimedTask(context.Background(), &queue.Payload{
			TaskID:      taskID,
			MigrationID: state.migID,
			ClaimEpoch:  1,
		}, 0, pool)
	}

	if !bytes.Equal(target.files["/migrated/Workspace/report.txt"], normalContent) {
		t.Fatalf("target report.txt = %q (files=%v), want %q", target.files["/migrated/Workspace/report.txt"], target.files, normalContent)
	}
	if got := downloadHits.Load(); got != 1 {
		t.Fatalf("Box download hits = %d, want 1 (only normal file)", got)
	}

	state.mu.Lock()
	for _, skippedPath := range []string{"/Workspace/Meeting.boxnote", "/Workspace/Board.boxcanvas", "/Workspace/Portal"} {
		tk, ok := tasksByPath[skippedPath]
		if !ok {
			state.mu.Unlock()
			t.Fatalf("missing task for %s", skippedPath)
		}
		if tk.status != "SKIPPED" || strings.TrimSpace(tk.errorMessage) == "" {
			state.mu.Unlock()
			t.Fatalf("%s task = (status=%q, err=%q), want SKIPPED with reason", skippedPath, tk.status, tk.errorMessage)
		}
	}
	if state.skippedFiles != 3 || state.failedFiles != 0 || state.processedFiles != 5 {
		t.Fatalf("migration counters = (processed=%d, skipped=%d, failed=%d), want (5, 3, 0)", state.processedFiles, state.skippedFiles, state.failedFiles)
	}

	// Also verify processor directly skips a pre-existing PENDING task for a Box Note via InspectResource.
	preExisting := &boxMigTestTask{
		id:           "task-preexisting-note",
		migrationID:  state.migID,
		resourceType: "files",
		filePath:     "/Workspace/Meeting.boxnote",
		fileSize:     14,
		status:       "RUNNING",
		claimEpoch:   1,
		metadata:     []byte(`{}`),
	}
	state.tasks = append(state.tasks, preExisting)
	state.mu.Unlock()

	proc.dispatchClaimedTask(context.Background(), &queue.Payload{
		TaskID:      preExisting.id,
		MigrationID: state.migID,
		ClaimEpoch:  1,
	}, 0, pool)

	state.mu.Lock()
	defer state.mu.Unlock()
	if preExisting.status != "SKIPPED" || strings.TrimSpace(preExisting.errorMessage) == "" {
		t.Fatalf("pre-existing Box Note task = (status=%q, err=%q), want SKIPPED with reason", preExisting.status, preExisting.errorMessage)
	}
	if got := downloadHits.Load(); got != 1 {
		t.Fatalf("Box download hits after pre-existing Box Note = %d, want still 1", got)
	}
}

func TestBoxMigrationDownloadBlockedFileFailsOnceWithoutRetry(t *testing.T) {
	const boxToken = "box-blocked-token"
	const secretKey = "01234567890123456789012345678901"
	encryptedPass, err := crypto.EncryptWithDomain(boxToken, secretKey, crypto.ConnectionCredentialDomain(true))
	if err != nil {
		t.Fatalf("EncryptWithDomain: %v", err)
	}
	encryptedTargetPass, err := crypto.EncryptWithDomain("target-pass", secretKey, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("EncryptWithDomain(target): %v", err)
	}

	apiServer, downloadHits := newFakeBoxScenarioServer(t, boxToken, map[string][]boxScenarioItem{
		"0": {{id: "folder-r", typ: "folder", name: "Restricted"}},
		"folder-r": {
			{id: "blocked-1", typ: "file", name: "policy.pdf", content: []byte("secret"), downloadBlocked: true, blockedCode: "forbidden_by_policy"},
		},
	})

	state := &boxMigTestState{
		migID:               "mig-box-blocked",
		status:              "INDEXING",
		selectedPaths:       []string{"/Restricted"},
		encryptedPass:       encryptedPass,
		encryptedTargetPass: encryptedTargetPass,
	}
	database := newBoxMigTestDB(t, state)

	idx, err := indexer.NewIndexer(database, secretKey, nil, time.Minute)
	if err != nil {
		t.Fatalf("NewIndexer: %v", err)
	}
	idx.SetSourceFactory(func(_ context.Context, _, _, _, password string, _ ...storage.ProviderOption) (indexer.IndexSource, error) {
		return storage.NewBoxTestProvider(password, apiServer.URL+"/2.0", apiServer.URL+"/api/2.0", apiServer.Client())
	})
	idx.Start(context.Background(), state.migID)

	proc, err := NewProcessor(database, nil, Config{
		WorkerID:      "box-worker",
		SecretKey:     secretKey,
		MaxThreads:    1,
		ProviderSlots: make(chan struct{}, 1),
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	pool := newProviderPool()
	defer pool.Close()
	target := newBoxSHA1TargetProvider()
	pool.factory = func(_ context.Context, providerType, _, _, password string, _ ...storage.ProviderOption) (PooledProvider, error) {
		if providerType == "box" {
			return storage.NewBoxTestProvider(password, apiServer.URL+"/2.0", apiServer.URL+"/api/2.0", apiServer.Client())
		}
		return target, nil
	}

	state.mu.Lock()
	var blockedTask *boxMigTestTask
	for _, tk := range state.tasks {
		if tk.filePath == "/Restricted/policy.pdf" {
			blockedTask = tk
			break
		}
	}
	if blockedTask == nil || blockedTask.status != "PENDING" {
		state.mu.Unlock()
		t.Fatalf("indexed tasks = %+v, want PENDING task for /Restricted/policy.pdf", state.tasks)
	}
	blockedTask.status = "RUNNING"
	state.mu.Unlock()

	proc.dispatchClaimedTask(context.Background(), &queue.Payload{
		TaskID:      blockedTask.id,
		MigrationID: state.migID,
		ClaimEpoch:  1,
	}, 0, pool)

	if got := downloadHits.Load(); got != 1 {
		t.Fatalf("download hits = %d, want 1 (no retries on 403 blocked file)", got)
	}

	state.mu.Lock()
	if blockedTask.status != "FAILED" {
		state.mu.Unlock()
		t.Fatalf("blocked task status = %q, want FAILED", blockedTask.status)
	}
	if blockedTask.attempts != 1 {
		state.mu.Unlock()
		t.Fatalf("blocked task attempts = %d, want 1", blockedTask.attempts)
	}
	if blockedTask.hasNextRetry {
		state.mu.Unlock()
		t.Fatal("blocked task scheduled next_retry_at, want nil (permanent failure)")
	}
	if !strings.Contains(blockedTask.errorMessage, "forbidden_by_policy") {
		state.mu.Unlock()
		t.Fatalf("blocked task errorMessage = %q, want forbidden_by_policy", blockedTask.errorMessage)
	}
	if state.failedFiles != 1 || state.processedFiles != 1 {
		state.mu.Unlock()
		t.Fatalf("migration counters = (processed=%d, failed=%d), want (1, 1)", state.processedFiles, state.failedFiles)
	}
	state.mu.Unlock()

	errItems, total, err := db.GetMigrationErrors(database, state.migID, 50, 0)
	if err != nil {
		t.Fatalf("GetMigrationErrors: %v", err)
	}
	if total != 1 || len(errItems) != 1 || errItems[0].Path != "/Restricted/policy.pdf" || errItems[0].Status != "FAILED" {
		t.Fatalf("GetMigrationErrors = (%+v, total=%d), want 1 FAILED entry for /Restricted/policy.pdf", errItems, total)
	}
}

type boxMigrationStubSource struct {
	files map[string]storage.CloudResource
	data  map[string][]byte
}

func (s *boxMigrationStubSource) InspectResource(_ context.Context, _, filePath string) (storage.CloudResource, error) {
	res, ok := s.files[filePath]
	if !ok {
		return storage.CloudResource{}, storage.ErrNotFound
	}
	return res, nil
}

func (s *boxMigrationStubSource) StreamDownload(_ context.Context, _, filePath string) (io.ReadCloser, error) {
	b, ok := s.data[filePath]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

type boxNoopSQLConnector struct{}

func (c boxNoopSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return boxNoopSQLConn{}, nil
}
func (c boxNoopSQLConnector) Driver() driver.Driver { return nil }

type boxNoopSQLConn struct{}

func (boxNoopSQLConn) Prepare(string) (driver.Stmt, error) { return nil, nil }
func (boxNoopSQLConn) Close() error                        { return nil }
func (boxNoopSQLConn) Begin() (driver.Tx, error)           { return boxNoopSQLTx{}, nil }
func (boxNoopSQLConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

type boxNoopSQLTx struct{}

func (boxNoopSQLTx) Commit() error   { return nil }
func (boxNoopSQLTx) Rollback() error { return nil }

type boxFullFakeServer struct {
	server *httptest.Server
	token  string

	mu              sync.Mutex
	items           map[string]boxFakeItem
	children        map[string][]string
	sessions        map[string]*boxFakeSession
	abortedSessions []string
	requests        []string
	uploadPosts     int
	partSize        int64
	faultHook       func(r *http.Request) (status int, retryAfter, body string)
	nextID          int
}

type boxFakeFileVersion struct {
	Size              int64
	SHA1              string
	Content           []byte
	ModifiedAt        string
	ContentModifiedAt string
}

type boxFakeItem struct {
	ID                string
	Type              string
	Name              string
	ParentID          string
	Size              int64
	SHA1              string
	Content           []byte
	ModifiedAt        string
	ContentModifiedAt string
	Versions          []boxFakeFileVersion
}

type boxFakeSession struct {
	ID       string
	FileID   string
	FolderID string
	FileName string
	FileSize int64
	PartSize int64
	Parts    map[int64][]byte
}

func newBoxFullFakeServer(token string) *boxFullFakeServer {
	s := &boxFullFakeServer{
		token:    token,
		items:    make(map[string]boxFakeItem),
		children: make(map[string][]string),
		sessions: make(map[string]*boxFakeSession),
		partSize: 8 * 1024 * 1024,
		nextID:   100,
	}
	s.items["0"] = boxFakeItem{ID: "0", Type: "folder", Name: "All Files"}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *boxFullFakeServer) close() { s.server.Close() }

func (s *boxFullFakeServer) newProvider(t *testing.T) *storage.BoxProvider {
	t.Helper()
	p, err := storage.NewBoxTestProvider(s.token, s.server.URL+"/2.0", s.server.URL+"/api/2.0", s.server.Client())
	if err != nil {
		t.Fatalf("NewBoxTestProvider: %v", err)
	}
	return p
}

func (s *boxFullFakeServer) addFolder(parentID, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := strconv.Itoa(s.nextID)
	s.items[id] = boxFakeItem{ID: id, Type: "folder", Name: name, ParentID: parentID, ModifiedAt: "2026-01-01T00:00:00Z"}
	s.children[parentID] = append(s.children[parentID], id)
	return id
}

func (s *boxFullFakeServer) addFile(parentID, name string, content []byte, mtime string) string {
	sum := sha1.Sum(content)
	sha1Hex := hex.EncodeToString(sum[:])
	copied := append([]byte(nil), content...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := strconv.Itoa(s.nextID)
	s.items[id] = boxFakeItem{
		ID:                id,
		Type:              "file",
		Name:              name,
		ParentID:          parentID,
		Size:              int64(len(copied)),
		SHA1:              sha1Hex,
		Content:           copied,
		ModifiedAt:        "2026-01-01T00:00:00Z",
		ContentModifiedAt: mtime,
		Versions: []boxFakeFileVersion{{
			Size:              int64(len(copied)),
			SHA1:              sha1Hex,
			Content:           append([]byte(nil), copied...),
			ModifiedAt:        "2026-01-01T00:00:00Z",
			ContentModifiedAt: mtime,
		}},
	}
	s.children[parentID] = append(s.children[parentID], id)
	return id
}

func (s *boxFullFakeServer) handle(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/dl/") {
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/dl/")
		s.mu.Lock()
		it, ok := s.items[id]
		s.mu.Unlock()
		if !ok || it.Type != "file" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(it.Content)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
	if r.Method == http.MethodPost && r.URL.Path == "/api/2.0/files/content" {
		s.uploadPosts++
	}
	hook := s.faultHook
	s.mu.Unlock()
	if hook != nil {
		if status, retryAfter, body := hook(r); status != 0 {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.WriteHeader(status)
			if body != "" {
				_, _ = w.Write([]byte(body))
			}
			return
		}
	}

	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/folders/") && strings.HasSuffix(r.URL.Path, "/items"):
		folderID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2.0/folders/"), "/items")
		s.handleList(w, folderID)
	case r.Method == http.MethodPost && r.URL.Path == "/2.0/folders":
		s.handleCreateFolder(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/files/") && strings.HasSuffix(r.URL.Path, "/content"):
		fileID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2.0/files/"), "/content")
		w.Header().Set("Location", s.server.URL+"/dl/"+fileID)
		w.WriteHeader(http.StatusFound)
	case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/files/content":
		s.handleSingleUpload(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/2.0/files/") && strings.HasSuffix(r.URL.Path, "/content"):
		fileID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/2.0/files/"), "/content")
		s.handleSingleUploadVersion(w, r, fileID)
	case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/files/upload_sessions":
		s.handleCreateSession(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/2.0/files/") && strings.HasSuffix(r.URL.Path, "/upload_sessions"):
		fileID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/2.0/files/"), "/upload_sessions")
		s.handleCreateNewVersionSession(w, r, fileID)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/") && strings.HasSuffix(r.URL.Path, "/commit"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/"), "/commit")
		s.handleCommitSession(w, r, id)
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/")
		s.handleUploadPart(w, r, id)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/")
		s.handleAbortSession(w, id)
	default:
		http.NotFound(w, r)
	}
}

func (s *boxFullFakeServer) handleList(w http.ResponseWriter, folderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent, ok := s.items[folderID]
	if !ok || parent.Type != "folder" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	entries := make([]map[string]any, 0, len(s.children[folderID]))
	for _, cid := range s.children[folderID] {
		it := s.items[cid]
		entries = append(entries, map[string]any{
			"id":                  it.ID,
			"type":                it.Type,
			"name":                it.Name,
			"size":                it.Size,
			"sha1":                it.SHA1,
			"modified_at":         it.ModifiedAt,
			"content_modified_at": it.ContentModifiedAt,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries, "limit": 1000, "next_marker": ""})
}

func (s *boxFullFakeServer) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Parent struct {
			ID string `json:"id"`
		} `json:"parent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cid := range s.children[body.Parent.ID] {
		ex := s.items[cid]
		if strings.EqualFold(ex.Name, body.Name) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "error", "status": 409, "code": "item_name_in_use",
				"context_info": map[string]any{"conflicts": map[string]any{"type": ex.Type, "id": ex.ID, "name": ex.Name}},
			})
			return
		}
	}
	s.nextID++
	id := strconv.Itoa(s.nextID)
	it := boxFakeItem{ID: id, Type: "folder", Name: body.Name, ParentID: body.Parent.ID, ModifiedAt: "2026-02-01T00:00:00Z"}
	s.items[id] = it
	s.children[body.Parent.ID] = append(s.children[body.Parent.ID], id)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": it.ID, "type": it.Type, "name": it.Name})
}

func (s *boxFullFakeServer) parseSingleMultipart(w http.ResponseWriter, r *http.Request) (name, parentID, modAt, sha1Hex string, data []byte, ok bool) {
	mr, err := r.MultipartReader()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return "", "", "", "", nil, false
	}
	var attr struct {
		Name   string `json:"name"`
		Parent struct {
			ID string `json:"id"`
		} `json:"parent"`
		ContentModifiedAt string `json:"content_modified_at"`
	}
	gotAttr := false
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return "", "", "", "", nil, false
		}
		switch part.FormName() {
		case "attributes":
			if err := json.NewDecoder(part).Decode(&attr); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return "", "", "", "", nil, false
			}
			gotAttr = true
		case "file":
			if !gotAttr {
				w.WriteHeader(http.StatusBadRequest)
				return "", "", "", "", nil, false
			}
			data, _ = io.ReadAll(part)
		}
		part.Close()
	}
	sum := sha1.Sum(data)
	sha1Hex = hex.EncodeToString(sum[:])
	if md5Hdr := r.Header.Get("content-md5"); md5Hdr != "" && !strings.EqualFold(md5Hdr, sha1Hex) {
		w.WriteHeader(http.StatusBadRequest)
		return "", "", "", "", nil, false
	}
	return attr.Name, attr.Parent.ID, attr.ContentModifiedAt, sha1Hex, data, true
}

func (s *boxFullFakeServer) handleSingleUpload(w http.ResponseWriter, r *http.Request) {
	name, parentID, modAt, sha1Hex, data, ok := s.parseSingleMultipart(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cid := range s.children[parentID] {
		ex := s.items[cid]
		if strings.EqualFold(ex.Name, name) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "error", "status": 409, "code": "item_name_in_use",
				"context_info": map[string]any{"conflicts": map[string]any{"type": ex.Type, "id": ex.ID, "name": ex.Name}},
			})
			return
		}
	}
	s.nextID++
	id := strconv.Itoa(s.nextID)
	copied := append([]byte(nil), data...)
	it := boxFakeItem{
		ID: id, Type: "file", Name: name, ParentID: parentID,
		Size: int64(len(copied)), SHA1: sha1Hex, Content: copied,
		ModifiedAt: "2026-02-01T00:00:00Z", ContentModifiedAt: modAt,
		Versions: []boxFakeFileVersion{{
			Size: int64(len(copied)), SHA1: sha1Hex, Content: append([]byte(nil), copied...),
			ModifiedAt: "2026-02-01T00:00:00Z", ContentModifiedAt: modAt,
		}},
	}
	s.items[id] = it
	s.children[parentID] = append(s.children[parentID], id)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total_count": 1,
		"entries":     []map[string]any{{"id": it.ID, "type": "file", "name": it.Name, "size": it.Size, "sha1": it.SHA1}},
	})
}

func (s *boxFullFakeServer) handleSingleUploadVersion(w http.ResponseWriter, r *http.Request, fileID string) {
	name, _, modAt, sha1Hex, data, ok := s.parseSingleMultipart(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it, exists := s.items[fileID]
	if !exists || it.Type != "file" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	copied := append([]byte(nil), data...)
	if name != "" {
		it.Name = name
	}
	it.Size = int64(len(copied))
	it.SHA1 = sha1Hex
	it.Content = copied
	it.ModifiedAt = "2026-02-02T00:00:00Z"
	it.ContentModifiedAt = modAt
	it.Versions = append(it.Versions, boxFakeFileVersion{
		Size: int64(len(copied)), SHA1: sha1Hex, Content: append([]byte(nil), copied...),
		ModifiedAt: "2026-02-02T00:00:00Z", ContentModifiedAt: modAt,
	})
	s.items[fileID] = it
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total_count": 1,
		"entries":     []map[string]any{{"id": it.ID, "type": "file", "name": it.Name, "size": it.Size, "sha1": it.SHA1}},
	})
}

func (s *boxFullFakeServer) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FolderID string `json:"folder_id"`
		FileName string `json:"file_name"`
		FileSize int64  `json:"file_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cid := range s.children[body.FolderID] {
		ex := s.items[cid]
		if strings.EqualFold(ex.Name, body.FileName) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "error", "status": 409, "code": "item_name_in_use",
				"context_info": map[string]any{"conflicts": map[string]any{"type": ex.Type, "id": ex.ID, "name": ex.Name}},
			})
			return
		}
	}
	s.createSessionLocked(w, "", body.FolderID, body.FileName, body.FileSize)
}

func (s *boxFullFakeServer) handleCreateNewVersionSession(w http.ResponseWriter, r *http.Request, fileID string) {
	var body struct {
		FileName string `json:"file_name"`
		FileSize int64  `json:"file_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it, exists := s.items[fileID]
	if !exists || it.Type != "file" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	name := body.FileName
	if name == "" {
		name = it.Name
	}
	s.createSessionLocked(w, fileID, it.ParentID, name, body.FileSize)
}

func (s *boxFullFakeServer) createSessionLocked(w http.ResponseWriter, fileID, folderID, fileName string, fileSize int64) {
	s.nextID++
	sid := "sess-" + strconv.Itoa(s.nextID)
	s.sessions[sid] = &boxFakeSession{
		ID: sid, FileID: fileID, FolderID: folderID, FileName: fileName, FileSize: fileSize, PartSize: s.partSize, Parts: make(map[int64][]byte),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": sid, "type": "upload_session", "part_size": s.partSize,
		"session_endpoints": map[string]string{
			"upload_part": s.server.URL + "/api/2.0/files/upload_sessions/" + sid,
			"commit":      s.server.URL + "/api/2.0/files/upload_sessions/" + sid + "/commit",
			"abort":       s.server.URL + "/api/2.0/files/upload_sessions/" + sid,
		},
	})
}

func (s *boxFullFakeServer) handleUploadPart(w http.ResponseWriter, r *http.Request, sid string) {
	data, _ := io.ReadAll(r.Body)
	rangeHdr := strings.TrimPrefix(r.Header.Get("Content-Range"), "bytes ")
	startStr := strings.SplitN(strings.SplitN(rangeHdr, "/", 2)[0], "-", 2)[0]
	start, _ := strconv.ParseInt(startStr, 10, 64)
	sum := sha1.Sum(data)
	if r.Header.Get("Digest") != "sha="+base64.StdEncoding.EncodeToString(sum[:]) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sid]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	sess.Parts[start] = append([]byte(nil), data...)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"part": map[string]any{"part_id": "p" + startStr, "offset": start, "size": len(data), "sha1": hex.EncodeToString(sum[:])},
	})
}

func (s *boxFullFakeServer) handleCommitSession(w http.ResponseWriter, r *http.Request, sid string) {
	var body struct {
		Parts []struct {
			Offset int64 `json:"offset"`
			Size   int64 `json:"size"`
		} `json:"parts"`
		Attributes struct {
			ContentModifiedAt string `json:"content_modified_at"`
		} `json:"attributes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sid]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	assembled := make([]byte, 0, sess.FileSize)
	for _, p := range body.Parts {
		assembled = append(assembled, sess.Parts[p.Offset]...)
	}
	sum := sha1.Sum(assembled)
	if r.Header.Get("Digest") != "sha="+base64.StdEncoding.EncodeToString(sum[:]) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	sha1Hex := hex.EncodeToString(sum[:])
	copied := append([]byte(nil), assembled...)
	ver := boxFakeFileVersion{
		Size: sess.FileSize, SHA1: sha1Hex, Content: append([]byte(nil), copied...),
		ModifiedAt: "2026-02-02T00:00:00Z", ContentModifiedAt: body.Attributes.ContentModifiedAt,
	}
	var it boxFakeItem
	if sess.FileID != "" {
		existing, exists := s.items[sess.FileID]
		if !exists || existing.Type != "file" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		existing.Size = sess.FileSize
		existing.SHA1 = sha1Hex
		existing.Content = copied
		existing.ModifiedAt = "2026-02-02T00:00:00Z"
		existing.ContentModifiedAt = body.Attributes.ContentModifiedAt
		existing.Versions = append(existing.Versions, ver)
		s.items[sess.FileID] = existing
		it = existing
	} else {
		s.nextID++
		id := strconv.Itoa(s.nextID)
		it = boxFakeItem{
			ID: id, Type: "file", Name: sess.FileName, ParentID: sess.FolderID,
			Size: sess.FileSize, SHA1: sha1Hex, Content: copied,
			ModifiedAt: "2026-02-01T00:00:00Z", ContentModifiedAt: body.Attributes.ContentModifiedAt,
			Versions: []boxFakeFileVersion{ver},
		}
		s.items[id] = it
		s.children[sess.FolderID] = append(s.children[sess.FolderID], id)
	}
	delete(s.sessions, sid)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total_count": 1,
		"entries":     []map[string]any{{"id": it.ID, "type": "file", "name": it.Name, "size": it.Size, "sha1": it.SHA1}},
	})
}

func (s *boxFullFakeServer) handleAbortSession(w http.ResponseWriter, sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sid)
	s.abortedSessions = append(s.abortedSessions, sid)
	w.WriteHeader(http.StatusNoContent)
}

func TestMigrationIntoBoxFromDropboxGoogleDriveOneDrive(t *testing.T) {
	srv := newBoxFullFakeServer("box-target-token")
	defer srv.close()
	boxTarget := srv.newProvider(t)
	defer boxTarget.Close()

	payload := []byte("cross-provider migration into Box verified by SHA-1!")
	sha1Sum := sha1.Sum(payload)
	wantSHA1 := "SHA1:" + hex.EncodeToString(sha1Sum[:])

	dbxHasher := storage.NewDropboxHasher()
	_, _ = dbxHasher.Write(payload)
	dropboxHash := "DROPBOX:" + hex.EncodeToString(dbxHasher.Sum(nil))

	md5Sum := md5.Sum(payload)
	googleHash := "MD5:" + hex.EncodeToString(md5Sum[:])

	qxorHasher := storage.NewQuickXorHasher()
	_, _ = qxorHasher.Write(payload)
	onedriveHash := "QUICKXOR:" + base64.StdEncoding.EncodeToString(qxorHasher.Sum(nil))

	mtime := time.Date(2025, 8, 20, 9, 10, 11, 0, time.UTC)

	sources := []struct {
		provider   string
		sourceHash string
		targetPath string
	}{
		{"dropbox", dropboxHash, "/FromDropbox/doc.txt"},
		{"google", googleHash, "/FromGoogle/doc.txt"},
		{"onedrive", onedriveHash, "/FromOneDrive/doc.txt"},
	}

	ctx := context.Background()
	for _, sc := range sources {
		t.Run(sc.provider, func(t *testing.T) {
			src := &boxMigrationStubSource{
				files: map[string]storage.CloudResource{
					"/doc.txt": {Path: "/doc.txt", Name: "doc.txt", Size: int64(len(payload)), Hash: sc.sourceHash, LastModified: mtime},
				},
				data: map[string][]byte{"/doc.txt": payload},
			}
			transfer, err := runTransferCore(transferRequest{
				Context:          ctx,
				Source:           src,
				Target:           boxTarget,
				SourceProvider:   sc.provider,
				TargetProvider:   "box",
				ResourceType:     "files",
				SourcePath:       "/doc.txt",
				TargetPath:       sc.targetPath,
				VerificationPath: sc.targetPath,
				FileSize:         int64(len(payload)),
				SourceHash:       sc.sourceHash,
				UploadOptions:    storage.UploadOptions{Metadata: storage.FileMetadata{ModifiedTime: mtime}},
			})
			if err != nil {
				t.Fatalf("runTransferCore(%s -> box): %v", sc.provider, err)
			}
			if transfer.WorkerHash() != wantSHA1 {
				t.Fatalf("WorkerHash() = %q, want %q", transfer.WorkerHash(), wantSHA1)
			}
			task := &db.Task{
				ResourceType: "files",
				FilePath:     "/doc.txt",
				SourceHash:   sql.NullString{String: sc.sourceHash, Valid: true},
				WorkerHash:   sql.NullString{String: transfer.WorkerHash(), Valid: true},
			}
			if !tryInlineHashVerify(ctx, task, boxTarget, "files", sc.targetPath, transfer.UploadReceipt.TargetResourceID) {
				t.Fatalf("tryInlineHashVerify(%s -> box) = false, want true", sc.provider)
			}
			if !task.ChecksumVerified || task.TargetHash.String != wantSHA1 {
				t.Fatalf("task verification = (%v, %q), want (true, %q)", task.ChecksumVerified, task.TargetHash.String, wantSHA1)
			}
			inspected, err := boxTarget.InspectResource(ctx, "files", sc.targetPath)
			if err != nil {
				t.Fatalf("InspectResource(%s): %v", sc.targetPath, err)
			}
			if !inspected.LastModified.Equal(mtime) || inspected.Hash != wantSHA1 {
				t.Fatalf("inspected = %+v, want mtime=%v hash=%s", inspected, mtime, wantSHA1)
			}
		})
	}
}

func TestMigrationIntoBoxConflictSkipAndRename(t *testing.T) {
	srv := newBoxFullFakeServer("box-target-token")
	defer srv.close()
	docsID := srv.addFolder("0", "Docs")
	existingContent := []byte("existing-box-content")
	srv.addFile(docsID, "report.txt", existingContent, "2024-01-01T00:00:00Z")
	srv.addFile(docsID, "report_copy1.txt", []byte("copy1-content"), "2024-01-02T00:00:00Z")

	boxTarget := srv.newProvider(t)
	defer boxTarget.Close()

	newContent := []byte("incoming-new-report-content")
	newSum := sha1.Sum(newContent)
	wantNewSHA1 := "SHA1:" + hex.EncodeToString(newSum[:])
	newMtime := time.Date(2026, 3, 5, 16, 0, 0, 0, time.UTC)

	src := &boxMigrationStubSource{
		files: map[string]storage.CloudResource{
			"/Docs/report.txt": {Path: "/Docs/report.txt", Name: "report.txt", Size: int64(len(newContent)), Hash: wantNewSHA1, LastModified: newMtime},
		},
		data: map[string][]byte{"/Docs/report.txt": newContent},
	}

	fakeDB := sql.OpenDB(boxNoopSQLConnector{})
	defer fakeDB.Close()
	proc, err := NewProcessor(fakeDB, nil, Config{
		WorkerID:      "w-box-test",
		SecretKey:     "test-secret",
		MaxThreads:    1,
		ProviderSlots: make(chan struct{}, 1),
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}

	ctx := context.Background()

	// 1. SKIP must leave existing Box file untouched and mark task SKIPPED.
	skipMig := &db.Migration{ID: "mig-skip", SourceProvider: "nextcloud", TargetProvider: "box", TargetDir: "/", ConflictStrategy: "SKIP"}
	skipTask := &db.Task{ID: "task-skip-1", MigrationID: skipMig.ID, ResourceType: "files", FilePath: "/Docs/report.txt", FileSize: int64(len(newContent)), ClaimEpoch: 1}
	if err := proc.executeMigrationTransfer(ctx, func() {}, skipMig, skipTask, src, boxTarget, nil); err != nil {
		t.Fatalf("executeMigrationTransfer(SKIP): %v", err)
	}
	if skipTask.Status != "SKIPPED" {
		t.Fatalf("skipTask.Status = %q, want SKIPPED", skipTask.Status)
	}
	inspectedExisting, err := boxTarget.InspectResource(ctx, "files", "/Docs/report.txt")
	if err != nil || inspectedExisting.Size != int64(len(existingContent)) {
		t.Fatalf("existing file after SKIP = (%+v, %v), want untouched size %d", inspectedExisting, err, len(existingContent))
	}

	// 2. RENAME must produce report_copy2.txt (skipping existing report.txt and report_copy1.txt).
	renameMig := &db.Migration{ID: "mig-rename", SourceProvider: "nextcloud", TargetProvider: "box", TargetDir: "/", ConflictStrategy: "RENAME"}
	renameTask := &db.Task{
		ID:           "task-rename-1",
		MigrationID:  renameMig.ID,
		ResourceType: "files",
		FilePath:     "/Docs/report.txt",
		FileSize:     int64(len(newContent)),
		SourceHash:   sql.NullString{String: wantNewSHA1, Valid: true},
		ClaimEpoch:   1,
	}
	if err := proc.executeMigrationTransfer(ctx, func() {}, renameMig, renameTask, src, boxTarget, nil); err != nil {
		t.Fatalf("executeMigrationTransfer(RENAME): %v", err)
	}
	if renameTask.Status != "COMPLETED" || !renameTask.ChecksumVerified {
		t.Fatalf("renameTask = (status=%q, verified=%v), want (COMPLETED, true)", renameTask.Status, renameTask.ChecksumVerified)
	}
	if renameTask.ResolvedTargetPath != "/Docs/report_copy2.txt" {
		t.Fatalf("renameTask.ResolvedTargetPath = %q, want /Docs/report_copy2.txt", renameTask.ResolvedTargetPath)
	}
	renamedRes, err := boxTarget.InspectResource(ctx, "files", "/Docs/report_copy2.txt")
	if err != nil {
		t.Fatalf("InspectResource(report_copy2.txt): %v", err)
	}
	if renamedRes.Hash != wantNewSHA1 || !renamedRes.LastModified.Equal(newMtime) {
		t.Fatalf("renamedRes = %+v, want hash=%s mtime=%v", renamedRes, wantNewSHA1, newMtime)
	}
}

func TestMigrationIntoBoxChunkedFailureAbortsAndQueueRetrySucceeds(t *testing.T) {
	srv := newBoxFullFakeServer("box-target-token")
	defer srv.close()

	var putCalls int
	srv.faultHook = func(r *http.Request) (int, string, string) {
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/") {
			putCalls++
			if putCalls >= 2 {
				return http.StatusServiceUnavailable, "", `{"type":"error","status":503}`
			}
		}
		return 0, "", ""
	}

	boxTarget := srv.newProvider(t)
	defer boxTarget.Close()

	// > 50 MiB forces chunked upload through runTransferCore + BoxProvider.
	payload := bytes.Repeat([]byte("M"), 50*1024*1024+1)
	sum := sha1.Sum(payload)
	wantSHA1 := "SHA1:" + hex.EncodeToString(sum[:])
	mtime := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	src := &boxMigrationStubSource{
		files: map[string]storage.CloudResource{
			"/big.bin": {Path: "/big.bin", Name: "big.bin", Size: int64(len(payload)), Hash: wantSHA1, LastModified: mtime},
		},
		data: map[string][]byte{"/big.bin": payload},
	}

	fakeDB := sql.OpenDB(boxNoopSQLConnector{})
	defer fakeDB.Close()
	proc, err := NewProcessor(fakeDB, nil, Config{
		WorkerID:      "w-box-chunked",
		SecretKey:     "test-secret",
		MaxThreads:    1,
		ProviderSlots: make(chan struct{}, 1),
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}

	mig := &db.Migration{ID: "mig-chunked", SourceProvider: "nextcloud", TargetProvider: "box", TargetDir: "/Backups", ConflictStrategy: "SKIP"}
	task := &db.Task{
		ID:           "task-chunked-1",
		MigrationID:  mig.ID,
		ResourceType: "files",
		FilePath:     "/big.bin",
		FileSize:     int64(len(payload)),
		SourceHash:   sql.NullString{String: wantSHA1, Valid: true},
		ClaimEpoch:   1,
	}

	ctx := context.Background()
	if err := proc.executeMigrationTransfer(ctx, func() {}, mig, task, src, boxTarget, nil); err == nil {
		t.Fatal("expected first attempt to fail on part 2")
	}

	srv.mu.Lock()
	abortedCount := len(srv.abortedSessions)
	activeSessions := len(srv.sessions)
	srv.faultHook = nil
	srv.mu.Unlock()
	if abortedCount != 1 || activeSessions != 0 {
		t.Fatalf("after failed chunked attempt: aborted=%d active=%d, want (1, 0)", abortedCount, activeSessions)
	}
	if exists, _, _ := boxTarget.FileExists(ctx, "files", "/Backups/big.bin"); exists {
		t.Fatal("aborted chunked migration left a partial/committed file on Box")
	}

	// Queue retry restarts the task from the beginning with persisted ResolvedTargetPath.
	task.ClaimEpoch = 2
	if err := proc.executeMigrationTransfer(ctx, func() {}, mig, task, src, boxTarget, nil); err != nil {
		t.Fatalf("retry executeMigrationTransfer: %v", err)
	}
	if task.Status != "COMPLETED" || !task.ChecksumVerified {
		t.Fatalf("retried task = (status=%q, verified=%v), want (COMPLETED, true)", task.Status, task.ChecksumVerified)
	}
	inspected, err := boxTarget.InspectResource(ctx, "files", "/Backups/big.bin")
	if err != nil {
		t.Fatalf("InspectResource(/Backups/big.bin): %v", err)
	}
	if inspected.Size != int64(len(payload)) || inspected.Hash != wantSHA1 || !inspected.LastModified.Equal(mtime) {
		t.Fatalf("inspected = %+v, want size=%d hash=%s mtime=%v", inspected, len(payload), wantSHA1, mtime)
	}
}

func TestMigrationIntoBoxUpload429And5xxNotRetriedInternally(t *testing.T) {
	srv := newBoxFullFakeServer("box-target-token")
	defer srv.close()
	srv.faultHook = func(r *http.Request) (int, string, string) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/2.0/files/content" {
			return http.StatusTooManyRequests, "9", `{"type":"error","status":429}`
		}
		return 0, "", ""
	}

	boxTarget := srv.newProvider(t)
	defer boxTarget.Close()

	payload := []byte("rate-limited upload")
	src := &boxMigrationStubSource{
		files: map[string]storage.CloudResource{
			"/rl.txt": {Path: "/rl.txt", Name: "rl.txt", Size: int64(len(payload))},
		},
		data: map[string][]byte{"/rl.txt": payload},
	}

	_, err := runTransferCore(transferRequest{
		Context:        context.Background(),
		Source:         src,
		Target:         boxTarget,
		SourceProvider: "nextcloud",
		TargetProvider: "box",
		ResourceType:   "files",
		SourcePath:     "/rl.txt",
		TargetPath:     "/rl.txt",
		FileSize:       int64(len(payload)),
	})
	var retryErr *storage.RetryAfterError
	if !errors.As(err, &retryErr) || retryErr.After != 9*time.Second {
		t.Fatalf("runTransferCore on 429 = %v, want *storage.RetryAfterError(9s)", err)
	}
	srv.mu.Lock()
	posts := srv.uploadPosts
	srv.mu.Unlock()
	if posts != 1 {
		t.Fatalf("single upload POSTs = %d, want 1 (no internal retry)", posts)
	}
}

func assertNoBoxTempStaging(t *testing.T, srv *boxFullFakeServer) {
	t.Helper()
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for _, it := range srv.items {
		if strings.HasSuffix(it.Name, ".tmp") || strings.Contains(it.Name, ".bak-") {
			t.Fatalf("unexpected temp/backup item staged on Box: %+v", it)
		}
	}
	for _, r := range srv.requests {
		if strings.Contains(r, ".tmp") || strings.Contains(r, ".bak-") {
			t.Fatalf("unexpected temp/backup request sent to Box: %s", r)
		}
	}
}

func TestMigrationIntoBoxOverwriteSingleAndChunked(t *testing.T) {
	srv := newBoxFullFakeServer("box-target-token")
	defer srv.close()

	docsID := srv.addFolder("0", "Docs")
	v1Single := []byte("single-v1-original")
	v1SingleSum := sha1.Sum(v1Single)
	v1SingleHex := hex.EncodeToString(v1SingleSum[:])
	singleID := srv.addFile(docsID, "report.txt", v1Single, "2024-01-01T00:00:00Z")

	backupsID := srv.addFolder("0", "Backups")
	v1Chunked := bytes.Repeat([]byte("A"), 50*1024*1024+1)
	v1ChunkedSum := sha1.Sum(v1Chunked)
	v1ChunkedHex := hex.EncodeToString(v1ChunkedSum[:])
	chunkedID := srv.addFile(backupsID, "big.bin", v1Chunked, "2024-01-01T00:00:00Z")

	boxTarget := srv.newProvider(t)
	defer boxTarget.Close()
	if boxTarget.SupportsAtomicRename() {
		t.Fatal("BoxProvider.SupportsAtomicRename() = true, want false")
	}

	fakeDB := sql.OpenDB(boxNoopSQLConnector{})
	defer fakeDB.Close()
	proc, err := NewProcessor(fakeDB, nil, Config{
		WorkerID:      "w-box-overwrite",
		SecretKey:     "test-secret",
		MaxThreads:    1,
		ProviderSlots: make(chan struct{}, 1),
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	ctx := context.Background()

	// 1. Single-request OVERWRITE: failure leaves v1 untouched, success creates v2.
	v2Single := []byte("single-v2-replacement-content")
	v2SingleSum := sha1.Sum(v2Single)
	v2SingleHex := hex.EncodeToString(v2SingleSum[:])
	v2SingleSHA1 := "SHA1:" + v2SingleHex
	v2SingleMtime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	src := &boxMigrationStubSource{
		files: map[string]storage.CloudResource{
			"/Docs/report.txt": {Path: "/Docs/report.txt", Name: "report.txt", Size: int64(len(v2Single)), Hash: v2SingleSHA1, LastModified: v2SingleMtime},
		},
		data: map[string][]byte{"/Docs/report.txt": v2Single},
	}

	mig := &db.Migration{ID: "mig-ow-single", SourceProvider: "nextcloud", TargetProvider: "box", TargetDir: "/", ConflictStrategy: "OVERWRITE"}

	srv.faultHook = func(r *http.Request) (int, string, string) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/2.0/files/"+singleID+"/content" {
			return http.StatusServiceUnavailable, "", `{"type":"error","status":503}`
		}
		return 0, "", ""
	}
	failSingleTask := &db.Task{
		ID: "task-ow-single-fail", MigrationID: mig.ID, ResourceType: "files",
		FilePath: "/Docs/report.txt", FileSize: int64(len(v2Single)),
		SourceHash: sql.NullString{String: v2SingleSHA1, Valid: true}, ClaimEpoch: 1,
	}
	if err := proc.executeMigrationTransfer(ctx, func() {}, mig, failSingleTask, src, boxTarget, nil); err == nil {
		t.Fatal("expected single OVERWRITE failure when Box returns 503")
	}
	srv.mu.Lock()
	srv.faultHook = nil
	singleItemAfterFail := srv.items[singleID]
	srv.mu.Unlock()
	if len(singleItemAfterFail.Versions) != 1 || singleItemAfterFail.SHA1 != v1SingleHex || !bytes.Equal(singleItemAfterFail.Content, v1Single) {
		t.Fatalf("failed single OVERWRITE modified v1: versions=%d sha1=%s", len(singleItemAfterFail.Versions), singleItemAfterFail.SHA1)
	}

	singleTask := &db.Task{
		ID: "task-ow-single-ok", MigrationID: mig.ID, ResourceType: "files",
		FilePath: "/Docs/report.txt", FileSize: int64(len(v2Single)),
		SourceHash: sql.NullString{String: v2SingleSHA1, Valid: true}, ClaimEpoch: 1,
	}
	if err := proc.executeMigrationTransfer(ctx, func() {}, mig, singleTask, src, boxTarget, nil); err != nil {
		t.Fatalf("executeMigrationTransfer(single OVERWRITE): %v", err)
	}
	if singleTask.Status != "COMPLETED" || !singleTask.ChecksumVerified {
		t.Fatalf("singleTask = (status=%q, verified=%v), want (COMPLETED, true)", singleTask.Status, singleTask.ChecksumVerified)
	}

	srv.mu.Lock()
	singleItem := srv.items[singleID]
	srv.mu.Unlock()
	if len(singleItem.Versions) != 2 || singleItem.SHA1 != v2SingleHex || !bytes.Equal(singleItem.Content, v2Single) {
		t.Fatalf("single OVERWRITE versions=%d sha1=%s, want 2 and %s", len(singleItem.Versions), singleItem.SHA1, v2SingleHex)
	}
	inspectedSingle, err := boxTarget.InspectResource(ctx, "files", "/Docs/report.txt")
	if err != nil || inspectedSingle.Hash != v2SingleSHA1 || !inspectedSingle.LastModified.Equal(v2SingleMtime) {
		t.Fatalf("InspectResource(report.txt) = (%+v, %v), want hash=%s mtime=%v", inspectedSingle, err, v2SingleSHA1, v2SingleMtime)
	}

	// 2. Chunked OVERWRITE (> 50 MiB): failure aborts session and leaves v1 intact, retry creates v2.
	v2Chunked := bytes.Repeat([]byte("B"), 50*1024*1024+1)
	v2ChunkedSum := sha1.Sum(v2Chunked)
	v2ChunkedHex := hex.EncodeToString(v2ChunkedSum[:])
	v2ChunkedSHA1 := "SHA1:" + v2ChunkedHex
	v2ChunkedMtime := time.Date(2026, 6, 2, 11, 0, 0, 0, time.UTC)

	src.files["/Backups/big.bin"] = storage.CloudResource{
		Path: "/Backups/big.bin", Name: "big.bin", Size: int64(len(v2Chunked)), Hash: v2ChunkedSHA1, LastModified: v2ChunkedMtime,
	}
	src.data["/Backups/big.bin"] = v2Chunked

	var putCalls int
	srv.faultHook = func(r *http.Request) (int, string, string) {
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/") {
			putCalls++
			if putCalls >= 2 {
				return http.StatusServiceUnavailable, "", `{"type":"error","status":503}`
			}
		}
		return 0, "", ""
	}
	chunkedTask := &db.Task{
		ID: "task-ow-chunked-1", MigrationID: mig.ID, ResourceType: "files",
		FilePath: "/Backups/big.bin", FileSize: int64(len(v2Chunked)),
		SourceHash: sql.NullString{String: v2ChunkedSHA1, Valid: true}, ClaimEpoch: 1,
	}
	if err := proc.executeMigrationTransfer(ctx, func() {}, mig, chunkedTask, src, boxTarget, nil); err == nil {
		t.Fatal("expected chunked OVERWRITE failure on part 2")
	}

	srv.mu.Lock()
	srv.faultHook = nil
	abortedCount := len(srv.abortedSessions)
	activeCount := len(srv.sessions)
	chunkedAfterFail := srv.items[chunkedID]
	srv.mu.Unlock()
	if abortedCount != 1 || activeCount != 0 {
		t.Fatalf("after failed chunked OVERWRITE: aborted=%d active=%d, want (1, 0)", abortedCount, activeCount)
	}
	if len(chunkedAfterFail.Versions) != 1 || chunkedAfterFail.SHA1 != v1ChunkedHex || !bytes.Equal(chunkedAfterFail.Content, v1Chunked) {
		t.Fatalf("failed chunked OVERWRITE modified v1: versions=%d sha1=%s", len(chunkedAfterFail.Versions), chunkedAfterFail.SHA1)
	}

	chunkedTask.ClaimEpoch = 2
	if err := proc.executeMigrationTransfer(ctx, func() {}, mig, chunkedTask, src, boxTarget, nil); err != nil {
		t.Fatalf("retry executeMigrationTransfer(chunked OVERWRITE): %v", err)
	}
	if chunkedTask.Status != "COMPLETED" || !chunkedTask.ChecksumVerified {
		t.Fatalf("chunkedTask = (status=%q, verified=%v), want (COMPLETED, true)", chunkedTask.Status, chunkedTask.ChecksumVerified)
	}

	srv.mu.Lock()
	chunkedItem := srv.items[chunkedID]
	srv.mu.Unlock()
	if len(chunkedItem.Versions) != 2 || chunkedItem.SHA1 != v2ChunkedHex || !bytes.Equal(chunkedItem.Content, v2Chunked) {
		t.Fatalf("chunked OVERWRITE versions=%d sha1=%s, want 2 and %s", len(chunkedItem.Versions), chunkedItem.SHA1, v2ChunkedHex)
	}
	inspectedChunked, err := boxTarget.InspectResource(ctx, "files", "/Backups/big.bin")
	if err != nil || inspectedChunked.Hash != v2ChunkedSHA1 || !inspectedChunked.LastModified.Equal(v2ChunkedMtime) {
		t.Fatalf("InspectResource(big.bin) = (%+v, %v), want hash=%s mtime=%v", inspectedChunked, err, v2ChunkedSHA1, v2ChunkedMtime)
	}

	assertNoBoxTempStaging(t, srv)
}

func TestExecuteSyncTransferIntoBoxOverwriteSingleAndChunked(t *testing.T) {
	srv := newBoxFullFakeServer("box-sync-token")
	defer srv.close()

	syncDirID := srv.addFolder("0", "Sync")
	v1Single := []byte("sync-single-v1")
	v1SingleSum := sha1.Sum(v1Single)
	v1SingleHex := hex.EncodeToString(v1SingleSum[:])
	singleID := srv.addFile(syncDirID, "notes.txt", v1Single, "2024-02-01T00:00:00Z")

	v1Chunked := bytes.Repeat([]byte("S"), 50*1024*1024+1)
	v1ChunkedSum := sha1.Sum(v1Chunked)
	v1ChunkedHex := hex.EncodeToString(v1ChunkedSum[:])
	chunkedID := srv.addFile(syncDirID, "bundle.bin", v1Chunked, "2024-02-01T00:00:00Z")

	boxTarget := srv.newProvider(t)
	defer boxTarget.Close()
	if boxTarget.SupportsAtomicRename() {
		t.Fatal("BoxProvider.SupportsAtomicRename() = true, want false")
	}

	ctx := context.Background()

	// 1. Single sync overwrite: failure keeps v1, success creates v2 and sets verified SHA-1.
	v2Single := []byte("sync-single-v2-updated")
	v2SingleSum := sha1.Sum(v2Single)
	v2SingleHex := hex.EncodeToString(v2SingleSum[:])
	v2SingleSHA1 := "SHA1:" + v2SingleHex
	v2SingleMtime := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)

	src := &boxMigrationStubSource{
		files: map[string]storage.CloudResource{
			"/Sync/notes.txt": {Path: "/Sync/notes.txt", Name: "notes.txt", Size: int64(len(v2Single)), Hash: v2SingleSHA1, LastModified: v2SingleMtime},
		},
		data: map[string][]byte{"/Sync/notes.txt": v2Single},
	}

	srv.faultHook = func(r *http.Request) (int, string, string) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/2.0/files/"+singleID+"/content" {
			return http.StatusServiceUnavailable, "", `{"type":"error","status":503}`
		}
		return 0, "", ""
	}
	failTask := &db.Task{ID: "sync-single-fail", ResourceType: "files", FilePath: "/Sync/notes.txt", FileSize: int64(len(v2Single))}
	if err := executeSyncTransfer(ctx, syncTransfer{
		Task: failTask, Source: src, Target: boxTarget,
		SourceProvider: "nextcloud", TargetProvider: "box",
		SourcePath: "/Sync/notes.txt", TargetPath: "/Sync/notes.txt",
		Throttler: throttle.NewMigrationThrottler(0), Progress: make(chan int64, 64),
	}); err == nil {
		t.Fatal("expected sync single overwrite failure on 503")
	}
	srv.mu.Lock()
	srv.faultHook = nil
	singleAfterFail := srv.items[singleID]
	srv.mu.Unlock()
	if len(singleAfterFail.Versions) != 1 || singleAfterFail.SHA1 != v1SingleHex {
		t.Fatalf("failed sync single overwrite mutated v1: versions=%d sha1=%s", len(singleAfterFail.Versions), singleAfterFail.SHA1)
	}

	okTask := &db.Task{
		ID: "sync-single-ok", ResourceType: "files", FilePath: "/Sync/notes.txt",
		FileSize: int64(len(v2Single)), SourceHash: sql.NullString{String: v2SingleSHA1, Valid: true},
	}
	if err := executeSyncTransfer(ctx, syncTransfer{
		Task: okTask, Source: src, Target: boxTarget,
		SourceProvider: "nextcloud", TargetProvider: "box",
		SourcePath: "/Sync/notes.txt", TargetPath: "/Sync/notes.txt",
		Throttler: throttle.NewMigrationThrottler(0), Progress: make(chan int64, 64),
	}); err != nil {
		t.Fatalf("executeSyncTransfer(single Box overwrite): %v", err)
	}
	if !okTask.ChecksumVerified || okTask.TargetHash.String != v2SingleSHA1 {
		t.Fatalf("okTask verification = (%v, %q), want (true, %q)", okTask.ChecksumVerified, okTask.TargetHash.String, v2SingleSHA1)
	}
	dlSingle, err := boxTarget.StreamDownload(ctx, "files", "/Sync/notes.txt")
	if err != nil {
		t.Fatalf("StreamDownload(/Sync/notes.txt): %v", err)
	}
	gotSingle, _ := io.ReadAll(dlSingle)
	dlSingle.Close()
	if !bytes.Equal(gotSingle, v2Single) {
		t.Fatalf("downloaded single sync content = %q, want %q", gotSingle, v2Single)
	}

	// 2. Chunked sync overwrite (> 50 MiB): failure aborts and keeps v1, retry creates v2.
	v2Chunked := bytes.Repeat([]byte("T"), 50*1024*1024+1)
	v2ChunkedSum := sha1.Sum(v2Chunked)
	v2ChunkedHex := hex.EncodeToString(v2ChunkedSum[:])
	v2ChunkedSHA1 := "SHA1:" + v2ChunkedHex
	v2ChunkedMtime := time.Date(2026, 9, 2, 9, 45, 0, 0, time.UTC)

	src.files["/Sync/bundle.bin"] = storage.CloudResource{
		Path: "/Sync/bundle.bin", Name: "bundle.bin", Size: int64(len(v2Chunked)), Hash: v2ChunkedSHA1, LastModified: v2ChunkedMtime,
	}
	src.data["/Sync/bundle.bin"] = v2Chunked

	var syncPutCalls int
	srv.faultHook = func(r *http.Request) (int, string, string) {
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/") {
			syncPutCalls++
			if syncPutCalls >= 2 {
				return http.StatusServiceUnavailable, "", `{"type":"error","status":503}`
			}
		}
		return 0, "", ""
	}
	chunkedSyncTask := &db.Task{
		ID: "sync-chunked-1", ResourceType: "files", FilePath: "/Sync/bundle.bin",
		FileSize: int64(len(v2Chunked)), SourceHash: sql.NullString{String: v2ChunkedSHA1, Valid: true},
	}
	if err := executeSyncTransfer(ctx, syncTransfer{
		Task: chunkedSyncTask, Source: src, Target: boxTarget,
		SourceProvider: "nextcloud", TargetProvider: "box",
		SourcePath: "/Sync/bundle.bin", TargetPath: "/Sync/bundle.bin",
		Throttler: throttle.NewMigrationThrottler(0), Progress: make(chan int64, 64),
	}); err == nil {
		t.Fatal("expected sync chunked overwrite failure on part 2")
	}

	srv.mu.Lock()
	srv.faultHook = nil
	abortedCount := len(srv.abortedSessions)
	activeCount := len(srv.sessions)
	chunkedAfterFail := srv.items[chunkedID]
	srv.mu.Unlock()
	if abortedCount != 1 || activeCount != 0 {
		t.Fatalf("after failed sync chunked overwrite: aborted=%d active=%d, want (1, 0)", abortedCount, activeCount)
	}
	if len(chunkedAfterFail.Versions) != 1 || chunkedAfterFail.SHA1 != v1ChunkedHex {
		t.Fatalf("failed sync chunked overwrite mutated v1: versions=%d sha1=%s", len(chunkedAfterFail.Versions), chunkedAfterFail.SHA1)
	}

	if err := executeSyncTransfer(ctx, syncTransfer{
		Task: chunkedSyncTask, Source: src, Target: boxTarget,
		SourceProvider: "nextcloud", TargetProvider: "box",
		SourcePath: "/Sync/bundle.bin", TargetPath: "/Sync/bundle.bin",
		Throttler: throttle.NewMigrationThrottler(0), Progress: make(chan int64, 64),
	}); err != nil {
		t.Fatalf("retry executeSyncTransfer(chunked Box overwrite): %v", err)
	}
	if !chunkedSyncTask.ChecksumVerified || chunkedSyncTask.TargetHash.String != v2ChunkedSHA1 {
		t.Fatalf("chunkedSyncTask verification = (%v, %q), want (true, %q)", chunkedSyncTask.ChecksumVerified, chunkedSyncTask.TargetHash.String, v2ChunkedSHA1)
	}
	srv.mu.Lock()
	chunkedItem := srv.items[chunkedID]
	srv.mu.Unlock()
	if len(chunkedItem.Versions) != 2 || chunkedItem.SHA1 != v2ChunkedHex {
		t.Fatalf("chunked sync overwrite versions=%d sha1=%s, want 2 and %s", len(chunkedItem.Versions), chunkedItem.SHA1, v2ChunkedHex)
	}
	inspected, err := boxTarget.InspectResource(ctx, "files", "/Sync/bundle.bin")
	if err != nil || !inspected.LastModified.Equal(v2ChunkedMtime) {
		t.Fatalf("InspectResource(/Sync/bundle.bin) = (%+v, %v), want mtime=%v", inspected, err, v2ChunkedMtime)
	}

	assertNoBoxTempStaging(t, srv)
}

func TestExecuteSyncTransferFromBoxToOtherProvider(t *testing.T) {
	srv := newBoxFullFakeServer("box-sync-src-token")
	defer srv.close()

	syncDirID := srv.addFolder("0", "Sync")
	payload := []byte("box-to-peer-sync-payload-v1")
	sum := sha1.Sum(payload)
	wantSHA1 := "SHA1:" + hex.EncodeToString(sum[:])
	const mtimeStr = "2026-08-15T14:30:00Z"
	srv.addFile(syncDirID, "report.txt", payload, mtimeStr)

	boxSource := srv.newProvider(t)
	defer boxSource.Close()

	target := newBoxSHA1TargetProvider()
	task := &db.Task{
		ID:           "sync-from-box-1",
		ResourceType: "files",
		FilePath:     "/Sync/report.txt",
		FileSize:     int64(len(payload)),
		SourceHash:   sql.NullString{String: wantSHA1, Valid: true},
	}

	if err := executeSyncTransfer(context.Background(), syncTransfer{
		Task:           task,
		Source:         boxSource,
		Target:         target,
		SourceProvider: "box",
		TargetProvider: "nextcloud",
		SourcePath:     "/Sync/report.txt",
		TargetPath:     "/Target/Sync/report.txt",
		Throttler:      throttle.NewMigrationThrottler(0),
		Progress:       make(chan int64, 64),
	}); err != nil {
		t.Fatalf("executeSyncTransfer(Box -> nextcloud): %v", err)
	}

	if !task.ChecksumVerified {
		t.Fatal("ChecksumVerified = false, want true")
	}
	if task.WorkerHash.String != wantSHA1 || task.TargetHash.String != wantSHA1 {
		t.Fatalf("worker/target hash = (%q, %q), want %q", task.WorkerHash.String, task.TargetHash.String, wantSHA1)
	}
	if !bytes.Equal(target.files["/Target/Sync/report.txt"], payload) {
		t.Fatalf("target content = %q, want %q", target.files["/Target/Sync/report.txt"], payload)
	}
	if len(target.uploadOpts) != 1 || target.uploadOpts[0].Checksum != wantSHA1 {
		t.Fatalf("uploadOpts = %+v, want forwarded SHA1 checksum %q", target.uploadOpts, wantSHA1)
	}
	wantMtime, _ := time.Parse(time.RFC3339, mtimeStr)
	if !target.uploadOpts[0].Metadata.ModifiedTime.Equal(wantMtime) {
		t.Fatalf("uploadOpts[0].Metadata.ModifiedTime = %v, want %v", target.uploadOpts[0].Metadata.ModifiedTime, wantMtime)
	}
}

func TestHandleTaskFailureProgressCounting(t *testing.T) {
	state := &boxMigTestState{
		migID:  "mig-failure-counting",
		status: "RUNNING",
		tasks: []*boxMigTestTask{
			{
				id:           "task-retry",
				migrationID:  "mig-failure-counting",
				resourceType: "files",
				filePath:     "/retry.txt",
				fileSize:     100,
				status:       "RUNNING",
				claimEpoch:   1,
			},
			{
				id:           "task-perm",
				migrationID:  "mig-failure-counting",
				resourceType: "files",
				filePath:     "/perm.txt",
				fileSize:     100,
				status:       "RUNNING",
				claimEpoch:   1,
			},
		},
	}
	database := newBoxMigTestDB(t, state)
	proc, err := NewProcessor(database, nil, Config{
		WorkerID:      "failure-worker",
		SecretKey:     "01234567890123456789012345678901",
		MaxThreads:    1,
		ProviderSlots: make(chan struct{}, 1),
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}

	// Retryable failure must schedule retry without advancing processed/failed counters.
	proc.handleTaskFailure(context.Background(), &queue.Payload{
		TaskID:      "task-retry",
		MigrationID: state.migID,
		ClaimEpoch:  1,
	}, errors.New("transient backend error"))

	state.mu.Lock()
	retryTask := state.tasks[0]
	if retryTask.status != "FAILED" || retryTask.attempts != 1 || !retryTask.hasNextRetry {
		state.mu.Unlock()
		t.Fatalf("retryable task = %+v, want FAILED with attempts=1 and next_retry_at", retryTask)
	}
	if state.processedFiles != 0 || state.failedFiles != 0 {
		state.mu.Unlock()
		t.Fatalf("counters after retryable failure = (processed=%d, failed=%d), want (0, 0)", state.processedFiles, state.failedFiles)
	}
	state.mu.Unlock()

	// Permanent failure must commit terminal failure counters without scheduling retry.
	proc.handleTaskFailure(context.Background(), &queue.Payload{
		TaskID:      "task-perm",
		MigrationID: state.migID,
		ClaimEpoch:  1,
	}, fmt.Errorf("blocked: %w", storage.ErrPermanentTransfer))

	state.mu.Lock()
	defer state.mu.Unlock()
	permTask := state.tasks[1]
	if permTask.status != "FAILED" || permTask.attempts != 1 || permTask.hasNextRetry {
		t.Fatalf("permanent task = %+v, want FAILED with attempts=1 and nil next_retry_at", permTask)
	}
	if state.processedFiles != 1 || state.failedFiles != 1 {
		t.Fatalf("counters after permanent failure = (processed=%d, failed=%d), want (1, 1)", state.processedFiles, state.failedFiles)
	}
}
