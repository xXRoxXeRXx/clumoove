package backup

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/db"
	"backend/internal/storage"
)

type boxTestItem struct {
	ID         string
	Type       string // "folder", "file", or "web_link"
	Name       string
	ParentID   string
	Content    []byte
	SHA1       string
	ModifiedAt string
	URL        string
}

type boxTestServer struct {
	apiServer      *httptest.Server
	downloadServer *httptest.Server
	token          string

	mu                   sync.Mutex
	items                map[string]boxTestItem
	children             map[string][]string
	nextID               int
	overrideDownloadBody map[string][]byte
}

func newBoxTestServer(token string) *boxTestServer {
	s := &boxTestServer{
		token:    token,
		items:    make(map[string]boxTestItem),
		children: make(map[string][]string),
		nextID:   100,
	}
	s.items["0"] = boxTestItem{ID: "0", Type: "folder", Name: "All Files", ModifiedAt: "2026-01-01T00:00:00Z"}
	s.downloadServer = httptest.NewServer(http.HandlerFunc(s.handleDownload))
	s.apiServer = httptest.NewServer(http.HandlerFunc(s.handleAPI))
	return s
}

func (s *boxTestServer) close() {
	s.apiServer.Close()
	s.downloadServer.Close()
}

func (s *boxTestServer) newProvider(t *testing.T) *storage.BoxProvider {
	t.Helper()
	p, err := storage.NewBoxTestProvider(s.token, s.apiServer.URL+"/2.0", s.apiServer.URL+"/api/2.0", s.apiServer.Client())
	if err != nil {
		t.Fatalf("NewBoxTestProvider: %v", err)
	}
	return p
}

func (s *boxTestServer) addFolder(parentID, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := strconv.Itoa(s.nextID)
	s.items[id] = boxTestItem{
		ID:         id,
		Type:       "folder",
		Name:       name,
		ParentID:   parentID,
		ModifiedAt: "2026-01-01T00:00:00Z",
	}
	s.children[parentID] = append(s.children[parentID], id)
	return id
}

func (s *boxTestServer) addFile(parentID, name string, content []byte, modAt string) string {
	sum := sha1.Sum(content)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := strconv.Itoa(s.nextID)
	s.items[id] = boxTestItem{
		ID:         id,
		Type:       "file",
		Name:       name,
		ParentID:   parentID,
		Content:    append([]byte(nil), content...),
		SHA1:       hex.EncodeToString(sum[:]),
		ModifiedAt: modAt,
	}
	s.children[parentID] = append(s.children[parentID], id)
	return id
}

func (s *boxTestServer) addWebLink(parentID, name, rawURL string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := strconv.Itoa(s.nextID)
	s.items[id] = boxTestItem{
		ID:         id,
		Type:       "web_link",
		Name:       name,
		ParentID:   parentID,
		URL:        rawURL,
		ModifiedAt: "2026-01-01T00:00:00Z",
	}
	s.children[parentID] = append(s.children[parentID], id)
	return id
}

func (s *boxTestServer) handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/2.0/users/me":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","type":"user","name":"Box User","login":"box@example.test"}`))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/folders/") && strings.HasSuffix(r.URL.Path, "/items"):
		folderID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2.0/folders/"), "/items")
		s.handleListFolderItems(w, folderID)
	case r.Method == http.MethodPost && r.URL.Path == "/2.0/folders":
		s.handleCreateFolder(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/folders/"):
		folderID := strings.TrimPrefix(r.URL.Path, "/2.0/folders/")
		s.handleGetItem(w, folderID, "folder")
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/files/") && strings.HasSuffix(r.URL.Path, "/content"):
		fileID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2.0/files/"), "/content")
		s.mu.Lock()
		item, ok := s.items[fileID]
		s.mu.Unlock()
		if !ok || item.Type != "file" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Location", s.downloadServer.URL+"/dl/"+fileID)
		w.WriteHeader(http.StatusFound)
	case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/files/content":
		s.handleUpload(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/2.0/files/"):
		fileID := strings.TrimPrefix(r.URL.Path, "/2.0/files/")
		s.handleDeleteFile(w, fileID)
	default:
		http.NotFound(w, r)
	}
}

func (s *boxTestServer) handleDeleteFile(w http.ResponseWriter, fileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[fileID]
	if !ok || item.Type != "file" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	children := s.children[item.ParentID]
	filtered := make([]string, 0, len(children))
	for _, cid := range children {
		if cid != fileID {
			filtered = append(filtered, cid)
		}
	}
	s.children[item.ParentID] = filtered
	delete(s.items, fileID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *boxTestServer) handleGetItem(w http.ResponseWriter, id, wantType string) {
	s.mu.Lock()
	item, ok := s.items[id]
	s.mu.Unlock()
	if !ok || item.Type != wantType {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":                  item.ID,
		"type":                item.Type,
		"name":                item.Name,
		"size":                int64(len(item.Content)),
		"sha1":                item.SHA1,
		"modified_at":         item.ModifiedAt,
		"content_modified_at": item.ModifiedAt,
	})
}

func (s *boxTestServer) handleListFolderItems(w http.ResponseWriter, folderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	folder, ok := s.items[folderID]
	if !ok || folder.Type != "folder" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	entries := make([]map[string]any, 0, len(s.children[folderID]))
	for _, cid := range s.children[folderID] {
		item := s.items[cid]
		entry := map[string]any{
			"id":                  item.ID,
			"type":                item.Type,
			"name":                item.Name,
			"size":                int64(len(item.Content)),
			"sha1":                item.SHA1,
			"modified_at":         item.ModifiedAt,
			"content_modified_at": item.ModifiedAt,
		}
		if item.URL != "" {
			entry["url"] = item.URL
		}
		entries = append(entries, entry)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries, "next_marker": ""})
}

func (s *boxTestServer) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Parent struct {
			ID string `json:"id"`
		} `json:"parent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.Parent.ID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parent, ok := s.items[body.Parent.ID]
	if !ok || parent.Type != "folder" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
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
	s.items[id] = boxTestItem{ID: id, Type: "folder", Name: body.Name, ParentID: body.Parent.ID, ModifiedAt: "2026-01-01T00:00:00Z"}
	s.children[body.Parent.ID] = append(s.children[body.Parent.ID], id)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "type": "folder", "name": body.Name})
}

func (s *boxTestServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var attr struct {
		Name   string `json:"name"`
		Parent struct {
			ID string `json:"id"`
		} `json:"parent"`
		ContentModifiedAt string `json:"content_modified_at"`
	}
	var data []byte
	gotAttrs := false
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch part.FormName() {
		case "attributes":
			if err := json.NewDecoder(part).Decode(&attr); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			gotAttrs = true
		case "file":
			if !gotAttrs {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			data, err = io.ReadAll(part)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		part.Close()
	}
	sum := sha1.Sum(data)
	sha1Hex := hex.EncodeToString(sum[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cid := range s.children[attr.Parent.ID] {
		ex := s.items[cid]
		if strings.EqualFold(ex.Name, attr.Name) {
			w.WriteHeader(http.StatusConflict)
			return
		}
	}
	s.nextID++
	id := strconv.Itoa(s.nextID)
	modAt := attr.ContentModifiedAt
	if modAt == "" {
		modAt = "2026-02-01T00:00:00Z"
	}
	s.items[id] = boxTestItem{
		ID:         id,
		Type:       "file",
		Name:       attr.Name,
		ParentID:   attr.Parent.ID,
		Content:    append([]byte(nil), data...),
		SHA1:       sha1Hex,
		ModifiedAt: modAt,
	}
	s.children[attr.Parent.ID] = append(s.children[attr.Parent.ID], id)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"entries": []map[string]any{{"id": id, "type": "file", "name": attr.Name, "size": int64(len(data)), "sha1": sha1Hex, "modified_at": modAt}},
	})
}

func (s *boxTestServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	fileID := strings.TrimPrefix(r.URL.Path, "/dl/")
	s.mu.Lock()
	item, ok := s.items[fileID]
	body := item.Content
	if override, hasOverride := s.overrideDownloadBody[fileID]; hasOverride {
		body = override
	}
	s.mu.Unlock()
	if !ok || item.Type != "file" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

type noopBoxBackupSQLConnector struct{}

func (noopBoxBackupSQLConnector) Connect(context.Context) (driver.Conn, error) {
	return noopBoxBackupSQLConn{}, nil
}
func (noopBoxBackupSQLConnector) Driver() driver.Driver { return nil }

type noopBoxBackupSQLConn struct{}

func (noopBoxBackupSQLConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (noopBoxBackupSQLConn) Close() error                        { return nil }
func (noopBoxBackupSQLConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (noopBoxBackupSQLConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

type inMemoryBoxBackupStore struct {
	db.BackupStore
	mu          sync.Mutex
	job         *db.BackupJob
	run         *db.BackupRun
	runClaimed  bool
	snapshots   map[string]*db.BackupSnapshot
	items       map[string][]db.BatchSnapshotItem
	packs       map[string]db.BackupPack
	blocks      map[string]string
	checkedPack []string
	reclaimable []db.BackupPack
	deletedPack []string
	nextSeq     int
}

func newInMemoryBoxBackupStore(job *db.BackupJob, run *db.BackupRun) *inMemoryBoxBackupStore {
	return &inMemoryBoxBackupStore{
		job:       job,
		run:       run,
		snapshots: make(map[string]*db.BackupSnapshot),
		items:     make(map[string][]db.BatchSnapshotItem),
		packs:     make(map[string]db.BackupPack),
		blocks:    make(map[string]string),
	}
}

func (s *inMemoryBoxBackupStore) RecoverStaleBackupRunsContext(context.Context) (int, error) {
	return 0, nil
}
func (s *inMemoryBoxBackupStore) ClaimNextQueuedBackupRunContext(context.Context) (*db.BackupJob, *db.BackupRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runClaimed {
		return nil, nil, nil
	}
	s.runClaimed = true
	return s.job, s.run, nil
}
func (s *inMemoryBoxBackupStore) ClaimNextBackupMaintenanceForWorkerContext(context.Context, string) (*db.BackupMaintenance, error) {
	return nil, nil
}
func (s *inMemoryBoxBackupStore) TouchBackupRunContext(context.Context, string, int, string) error {
	return nil
}
func (s *inMemoryBoxBackupStore) BackupRunActiveContext(context.Context, string, int, string) (bool, error) {
	return true, nil
}
func (s *inMemoryBoxBackupStore) BackupProfilesSameOAuthAccountContext(context.Context, sql.NullString, sql.NullString) (bool, error) {
	return false, nil
}
func (s *inMemoryBoxBackupStore) TransitionBackupRunContext(_ context.Context, _ string, _ int, _, _, nextState string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run.State = nextState
	return true, nil
}
func (s *inMemoryBoxBackupStore) CreateBackupSnapshotDraftContext(_ context.Context, _, _ string, _ db.StringArray) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSeq++
	id := "snap-" + strconv.Itoa(s.nextSeq)
	s.snapshots[id] = &db.BackupSnapshot{
		ID:             id,
		State:          "DRAFT",
		CreatedAt:      time.Now().UTC(),
		IntegrityState: "VERIFIED",
	}
	return id, nil
}
func (s *inMemoryBoxBackupStore) GetLatestValidBackupSnapshotIDContext(context.Context, string) (string, error) {
	return "", nil
}
func (s *inMemoryBoxBackupStore) FindBackupBlockContext(_ context.Context, _ string, hash []byte) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.blocks[string(hash)]
	return id, ok, nil
}
func (s *inMemoryBoxBackupStore) RecordBackupPackAndBlocksContext(_ context.Context, jobID, remotePath string, packHash []byte, packSize int64, blocks []db.BackupPackBlock) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSeq++
	packID := "pack-" + strconv.Itoa(s.nextSeq)
	s.packs[packID] = db.BackupPack{
		ID:         packID,
		RemotePath: remotePath,
		SHA256:     append([]byte(nil), packHash...),
		SizeBytes:  packSize,
		State:      "READY",
	}
	ids := make(map[string]string, len(blocks))
	for _, blk := range blocks {
		s.nextSeq++
		bid := "blk-" + strconv.Itoa(s.nextSeq)
		s.blocks[string(blk.Hash)] = bid
		ids[string(blk.Hash)] = bid
	}
	return ids, nil
}
func (s *inMemoryBoxBackupStore) BatchCreateBackupSnapshotItemsAndBlocksContext(_ context.Context, snapshotID string, items []db.BatchSnapshotItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[snapshotID] = append(s.items[snapshotID], items...)
	return nil
}
func (s *inMemoryBoxBackupStore) ListBackupSnapshotPacksContext(context.Context, string, string) ([]db.BackupPack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]db.BackupPack, 0, len(s.packs))
	for _, p := range s.packs {
		out = append(out, p)
	}
	return out, nil
}
func (s *inMemoryBoxBackupStore) MarkBackupPacksCheckedContext(_ context.Context, _ string, packIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkedPack = append(s.checkedPack, packIDs...)
	return nil
}
func (s *inMemoryBoxBackupStore) PublishBackupSnapshotAndFinalizeContext(_ context.Context, _ string, _ int, _, snapshotID, snapshotState, runState string, totalFiles, totalDirs int, totalBytes int64, processedFiles int, processedBytes, deduplicatedBytes int64, unstableFiles, failedFiles int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap, ok := s.snapshots[snapshotID]; ok {
		snap.State = snapshotState
		snap.TotalFiles = totalFiles
		snap.TotalDirs = totalDirs
		snap.TotalBytes = totalBytes
	}
	s.run.State = runState
	s.run.ProcessedFiles = processedFiles
	s.run.ProcessedBytes = processedBytes
	return true, nil
}
func (s *inMemoryBoxBackupStore) FailBackupRunContext(_ context.Context, _ string, _ int, _, _, errorCode string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run.State = "FAILED"
	s.run.ErrorCode = sql.NullString{String: errorCode, Valid: true}
	return true, nil
}
func (s *inMemoryBoxBackupStore) ExpireBackupSnapshotsContext(context.Context, string, int) error {
	return nil
}
func (s *inMemoryBoxBackupStore) ListBackupReclaimablePacksContext(context.Context, string, int) ([]db.BackupPack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]db.BackupPack(nil), s.reclaimable...), nil
}
func (s *inMemoryBoxBackupStore) MarkBackupPackDeletedContext(_ context.Context, _ string, packID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedPack = append(s.deletedPack, packID)
	delete(s.packs, packID)
	return nil
}
func (s *inMemoryBoxBackupStore) FindBackupCompactionCandidateContext(context.Context, string) (*db.BackupPack, error) {
	return nil, nil
}
func (s *inMemoryBoxBackupStore) listVisibleSnapshots() []*db.BackupSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*db.BackupSnapshot
	for _, snap := range s.snapshots {
		if snap.State == "READY" || snap.State == "PARTIAL" {
			out = append(out, snap)
		}
	}
	return out
}

func TestBackupIntoBoxCompletesAndSnapshotCanBeListed(t *testing.T) {
	boxSrv := newBoxTestServer("box-backup-token")
	defer boxSrv.close()
	boxSrv.addFolder("0", "backups")

	source := newMockTrackedSource()
	mtime := time.Date(2026, 4, 10, 9, 30, 0, 0, time.UTC)
	source.files["/docs"] = mockFileRecord{isDir: true, mtime: mtime}
	source.files["/docs/alpha.txt"] = mockFileRecord{data: []byte("alpha backup payload"), mtime: mtime}
	source.files["/docs/beta.bin"] = mockFileRecord{data: bytes.Repeat([]byte("B"), 1024), mtime: mtime}

	boxTarget := boxSrv.newProvider(t)
	defer boxTarget.Close()

	const repoID = "11111111-2222-3333-4444-555555555555"
	job := &db.BackupJob{
		ID:             "bjob-1",
		UserID:         "user-1",
		SourceProvider: "nextcloud",
		TargetProvider: "box",
		SelectedPaths:  db.StringArray{"/docs"},
		TargetDir:      "/backups",
		RepositoryID:   repoID,
		RepositoryRoot: path.Join("/backups", ".clumoove-backup", repoID),
		RetentionCount: 7,
		LockID:         42,
	}
	run := &db.BackupRun{
		ID:         "brun-1",
		Generation: 1,
		Trigger:    "MANUAL",
		State:      "SCANNING",
	}

	noopDB := sql.OpenDB(noopBoxBackupSQLConnector{})
	defer noopDB.Close()
	store := newInMemoryBoxBackupStore(job, run)
	coord := &Coordinator{
		db:              noopDB,
		store:           store,
		packWriterSlots: make(chan struct{}, 2),
		workerID:        "backup-box-test",
	}
	coord.SetProviderFactory(func(context.Context, *db.BackupJob) (SourceProvider, TargetProvider, error) {
		return source, boxTarget, nil
	})

	worked, err := coord.PollOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("PollOnce() = (%v, %v), want (true, nil)", worked, err)
	}
	if run.State != "COMPLETED" {
		t.Fatalf("run.State = %q (error_code=%q), want COMPLETED", run.State, run.ErrorCode.String)
	}
	if run.ProcessedFiles != 2 || run.ProcessedBytes != int64(20+1024) {
		t.Fatalf("run processed = (%d files, %d bytes), want (2, 1044)", run.ProcessedFiles, run.ProcessedBytes)
	}

	snapshots := store.listVisibleSnapshots()
	if len(snapshots) != 1 || snapshots[0].State != "READY" || snapshots[0].TotalFiles != 2 {
		t.Fatalf("listed snapshots = %+v, want 1 READY snapshot with 2 files", snapshots)
	}

	// Repository format file and at least one verified pack must exist in Box.
	formatExists, _, err := boxTarget.FileExists(context.Background(), "files", path.Join(job.RepositoryRoot, "format-v1.json"))
	if err != nil || !formatExists {
		t.Fatalf("format-v1.json FileExists = (%v, %v), want (true, nil)", formatExists, err)
	}
	if len(store.packs) != 1 || len(store.checkedPack) != 1 {
		t.Fatalf("packs=%d checked=%d, want (1, 1)", len(store.packs), len(store.checkedPack))
	}
	for _, p := range store.packs {
		exists, size, err := boxTarget.FileExists(context.Background(), "files", p.RemotePath)
		if err != nil || !exists || size != p.SizeBytes {
			t.Fatalf("remote pack %s FileExists = (%v, %d, %v), want (true, %d, nil)", p.RemotePath, exists, size, err, p.SizeBytes)
		}
	}
}

func TestBackupFromBoxVerifiesSHA1AndSkipsNonTransferableItems(t *testing.T) {
	// 1. Box source with valid file + non-transferable .boxnote & web_link skips special items and succeeds.
	srcSrv := newBoxTestServer("box-src-token")
	defer srcSrv.close()
	tgtSrv := newBoxTestServer("box-tgt-token")
	defer tgtSrv.close()
	tgtSrv.addFolder("0", "backups")

	docsID := srcSrv.addFolder("0", "Docs")
	srcSrv.addFile(docsID, "good.txt", []byte("box-source-valid-content"), "2026-04-10T10:00:00Z")
	srcSrv.addFile(docsID, "Meeting.boxnote", []byte(`{"doc":"note"}`), "2026-04-10T10:00:00Z")
	srcSrv.addWebLink(docsID, "Portal", "https://example.test")

	boxSrc := srcSrv.newProvider(t)
	defer boxSrc.Close()
	boxTgt := tgtSrv.newProvider(t)
	defer boxTgt.Close()

	const repoID = "22222222-3333-4444-5555-666666666666"
	job := &db.BackupJob{
		ID:             "bjob-src",
		UserID:         "user-1",
		SourceProvider: "box",
		TargetProvider: "box",
		SelectedPaths:  db.StringArray{"/Docs"},
		TargetDir:      "/backups",
		RepositoryID:   repoID,
		RepositoryRoot: path.Join("/backups", ".clumoove-backup", repoID),
		RetentionCount: 7,
		LockID:         43,
	}
	run := &db.BackupRun{ID: "brun-src-1", Generation: 1, Trigger: "MANUAL", State: "SCANNING"}

	noopDB := sql.OpenDB(noopBoxBackupSQLConnector{})
	defer noopDB.Close()
	store := newInMemoryBoxBackupStore(job, run)
	coord := &Coordinator{
		db:              noopDB,
		store:           store,
		packWriterSlots: make(chan struct{}, 2),
		workerID:        "backup-from-box-test",
	}
	coord.SetProviderFactory(func(context.Context, *db.BackupJob) (SourceProvider, TargetProvider, error) {
		return boxSrc, boxTgt, nil
	})

	if worked, err := coord.PollOnce(context.Background()); err != nil || !worked {
		t.Fatalf("PollOnce(valid Box source) = (%v, %v), want (true, nil)", worked, err)
	}
	if run.State != "COMPLETED" || run.ProcessedFiles != 1 {
		t.Fatalf("run = (state=%q, files=%d, err=%q), want (COMPLETED, 1)", run.State, run.ProcessedFiles, run.ErrorCode.String)
	}

	// 2. Box source file whose downloaded bytes do not match Box's SHA-1 fails the backup run.
	badSrcSrv := newBoxTestServer("box-src-bad-token")
	defer badSrcSrv.close()
	badDocsID := badSrcSrv.addFolder("0", "Docs")
	badFileID := badSrcSrv.addFile(badDocsID, "tampered.txt", []byte("original"), "2026-04-10T10:00:00Z")
	badSrcSrv.overrideDownloadBody = map[string][]byte{badFileID: []byte("corrupt!")}

	badBoxSrc := badSrcSrv.newProvider(t)
	defer badBoxSrc.Close()

	badJob := *job
	badJob.ID = "bjob-src-bad"
	badJob.TargetDir = "/backups"
	badJob.RepositoryRoot = path.Join("/backups", ".clumoove-backup", "33333333-4444-5555-6666-777777777777")
	badRun := &db.BackupRun{ID: "brun-src-bad", Generation: 1, Trigger: "MANUAL", State: "SCANNING"}
	badTgtSrv := newBoxTestServer("box-tgt-bad-token")
	defer badTgtSrv.close()
	badTgtSrv.addFolder("0", "backups")
	badBoxTgt := badTgtSrv.newProvider(t)
	defer badBoxTgt.Close()

	badStore := newInMemoryBoxBackupStore(&badJob, badRun)
	badCoord := &Coordinator{
		db:              noopDB,
		store:           badStore,
		packWriterSlots: make(chan struct{}, 2),
		workerID:        "backup-from-box-bad-test",
	}
	badCoord.SetProviderFactory(func(context.Context, *db.BackupJob) (SourceProvider, TargetProvider, error) {
		return badBoxSrc, badBoxTgt, nil
	})
	if _, err := badCoord.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce(corrupt Box source) err = %v", err)
	}
	if badRun.State != "FAILED" {
		t.Fatalf("badRun.State = %q, want FAILED when Box source SHA-1 mismatches download bytes", badRun.State)
	}
}

func TestBackupIntoBoxRetentionDeletesExpiredPacks(t *testing.T) {
	boxSrv := newBoxTestServer("box-retention-token")
	defer boxSrv.close()
	backupsID := boxSrv.addFolder("0", "backups")
	clumooveID := boxSrv.addFolder(backupsID, ".clumoove-backup")
	repoDirID := boxSrv.addFolder(clumooveID, "repo-ret")
	packsID := boxSrv.addFolder(repoDirID, "packs")
	boxSrv.addFile(packsID, "expired.pack", []byte("stale-pack-bytes"), "2026-01-01T00:00:00Z")

	boxTarget := boxSrv.newProvider(t)
	defer boxTarget.Close()

	packPath := "/backups/.clumoove-backup/repo-ret/packs/expired.pack"
	job := &db.BackupJob{ID: "bjob-ret", RetentionCount: 1}
	store := newInMemoryBoxBackupStore(job, &db.BackupRun{ID: "brun-ret"})
	store.reclaimable = []db.BackupPack{{ID: "pack-expired", RemotePath: packPath}}

	coord := &Coordinator{store: store}
	if err := coord.applyRetention(context.Background(), job, boxTarget); err != nil {
		t.Fatalf("applyRetention(Box): %v", err)
	}
	if exists, _, err := boxTarget.FileExists(context.Background(), "files", packPath); err != nil || exists {
		t.Fatalf("expired pack FileExists = (%v, %v), want (false, nil)", exists, err)
	}
	if len(store.deletedPack) != 1 || store.deletedPack[0] != "pack-expired" {
		t.Fatalf("deletedPack = %+v, want [pack-expired]", store.deletedPack)
	}
}
