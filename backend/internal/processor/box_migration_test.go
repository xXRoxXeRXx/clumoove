package processor

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
