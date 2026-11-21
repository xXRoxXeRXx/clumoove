package restore

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/backuprepo"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/storage"
)

type boxRestoreItemRecord struct {
	ID         string
	Type       string // "folder" or "file"
	Name       string
	ParentID   string
	Content    []byte
	SHA1       string
	ModifiedAt string
}

type boxRestoreFakeServer struct {
	apiServer      *httptest.Server
	downloadServer *httptest.Server
	token          string

	mu                  sync.Mutex
	items               map[string]boxRestoreItemRecord
	children            map[string][]string
	nextID              int
	downloadRanges      []string
	fullDownloads       int
	downloadAuthHeaders []string
	corruptReportedSHA1 bool
}

func newBoxRestoreFakeServer(token string) *boxRestoreFakeServer {
	s := &boxRestoreFakeServer{
		token:    token,
		items:    make(map[string]boxRestoreItemRecord),
		children: make(map[string][]string),
		nextID:   100,
	}
	s.items["0"] = boxRestoreItemRecord{ID: "0", Type: "folder", Name: "All Files", ModifiedAt: "2026-01-01T00:00:00Z"}
	s.downloadServer = httptest.NewServer(http.HandlerFunc(s.handleDownload))
	s.apiServer = httptest.NewServer(http.HandlerFunc(s.handleAPI))
	return s
}

func (s *boxRestoreFakeServer) close() {
	s.apiServer.Close()
	s.downloadServer.Close()
}

func (s *boxRestoreFakeServer) newProvider(t *testing.T) *storage.BoxProvider {
	t.Helper()
	p, err := storage.NewBoxTestProvider(s.token, s.apiServer.URL+"/2.0", s.apiServer.URL+"/api/2.0", s.apiServer.Client())
	if err != nil {
		t.Fatalf("NewBoxTestProvider: %v", err)
	}
	return p
}

func (s *boxRestoreFakeServer) addFolder(parentID, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := strconv.Itoa(s.nextID)
	s.items[id] = boxRestoreItemRecord{ID: id, Type: "folder", Name: name, ParentID: parentID, ModifiedAt: "2026-01-01T00:00:00Z"}
	s.children[parentID] = append(s.children[parentID], id)
	return id
}

func (s *boxRestoreFakeServer) addFile(parentID, name string, content []byte) string {
	sum := sha1.Sum(content)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := strconv.Itoa(s.nextID)
	s.items[id] = boxRestoreItemRecord{
		ID:         id,
		Type:       "file",
		Name:       name,
		ParentID:   parentID,
		Content:    append([]byte(nil), content...),
		SHA1:       hex.EncodeToString(sum[:]),
		ModifiedAt: "2026-01-01T00:00:00Z",
	}
	s.children[parentID] = append(s.children[parentID], id)
	return id
}

func (s *boxRestoreFakeServer) getFileContent(id string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[id]
	if !ok || it.Type != "file" {
		return nil, false
	}
	return append([]byte(nil), it.Content...), true
}

func (s *boxRestoreFakeServer) handleAPI(w http.ResponseWriter, r *http.Request) {
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
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/files/"):
		fileID := strings.TrimPrefix(r.URL.Path, "/2.0/files/")
		s.handleGetItem(w, fileID, "file")
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/2.0/files/"):
		fileID := strings.TrimPrefix(r.URL.Path, "/2.0/files/")
		s.handleDeleteFile(w, fileID)
	case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/files/content":
		s.handleUpload(w, r, "")
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/2.0/files/") && strings.HasSuffix(r.URL.Path, "/content"):
		fileID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/2.0/files/"), "/content")
		s.handleUpload(w, r, fileID)
	default:
		http.NotFound(w, r)
	}
}

func (s *boxRestoreFakeServer) reportedSHA1(item boxRestoreItemRecord) string {
	if s.corruptReportedSHA1 && item.Type == "file" {
		return strings.Repeat("0", 40)
	}
	return item.SHA1
}

func (s *boxRestoreFakeServer) handleDeleteFile(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok || item.Type != "file" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	children := s.children[item.ParentID]
	filtered := make([]string, 0, len(children))
	for _, cid := range children {
		if cid != id {
			filtered = append(filtered, cid)
		}
	}
	s.children[item.ParentID] = filtered
	delete(s.items, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *boxRestoreFakeServer) handleGetItem(w http.ResponseWriter, id, wantType string) {
	s.mu.Lock()
	item, ok := s.items[id]
	sha := s.reportedSHA1(item)
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
		"sha1":                sha,
		"modified_at":         item.ModifiedAt,
		"content_modified_at": item.ModifiedAt,
	})
}

func (s *boxRestoreFakeServer) handleListFolderItems(w http.ResponseWriter, folderID string) {
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
		entries = append(entries, map[string]any{
			"id":                  item.ID,
			"type":                item.Type,
			"name":                item.Name,
			"size":                int64(len(item.Content)),
			"sha1":                s.reportedSHA1(item),
			"modified_at":         item.ModifiedAt,
			"content_modified_at": item.ModifiedAt,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries, "next_marker": ""})
}

func (s *boxRestoreFakeServer) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
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
	s.items[id] = boxRestoreItemRecord{ID: id, Type: "folder", Name: body.Name, ParentID: body.Parent.ID, ModifiedAt: "2026-01-01T00:00:00Z"}
	s.children[body.Parent.ID] = append(s.children[body.Parent.ID], id)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "type": "folder", "name": body.Name})
}

func (s *boxRestoreFakeServer) handleUpload(w http.ResponseWriter, r *http.Request, targetFileID string) {
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
	modAt := attr.ContentModifiedAt
	if modAt == "" {
		modAt = "2026-02-01T00:00:00Z"
	}
	var item boxRestoreItemRecord
	if targetFileID != "" {
		ex, ok := s.items[targetFileID]
		if !ok || ex.Type != "file" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		ex.Content = append([]byte(nil), data...)
		ex.SHA1 = sha1Hex
		ex.ModifiedAt = modAt
		s.items[targetFileID] = ex
		item = ex
	} else {
		for _, cid := range s.children[attr.Parent.ID] {
			ex := s.items[cid]
			if strings.EqualFold(ex.Name, attr.Name) {
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
		item = boxRestoreItemRecord{
			ID:         id,
			Type:       "file",
			Name:       attr.Name,
			ParentID:   attr.Parent.ID,
			Content:    append([]byte(nil), data...),
			SHA1:       sha1Hex,
			ModifiedAt: modAt,
		}
		s.items[id] = item
		s.children[attr.Parent.ID] = append(s.children[attr.Parent.ID], id)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"entries": []map[string]any{{"id": item.ID, "type": "file", "name": item.Name, "size": int64(len(data)), "sha1": sha1Hex, "modified_at": modAt}},
	})
}

func (s *boxRestoreFakeServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimPrefix(r.URL.Path, "/dl/")
	rangeHdr := r.Header.Get("Range")

	s.mu.Lock()
	s.downloadAuthHeaders = append(s.downloadAuthHeaders, r.Header.Get("Authorization"))
	if rangeHdr != "" {
		s.downloadRanges = append(s.downloadRanges, rangeHdr)
	} else {
		s.fullDownloads++
	}
	item, ok := s.items[fileID]
	s.mu.Unlock()

	if r.Header.Get("Authorization") != "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !ok || item.Type != "file" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if rangeHdr == "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(item.Content)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(item.Content)
		return
	}
	spec := strings.TrimPrefix(rangeHdr, "bytes=")
	bounds := strings.SplitN(spec, "-", 2)
	if len(bounds) != 2 {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	start, err1 := strconv.ParseInt(bounds[0], 10, 64)
	end, err2 := strconv.ParseInt(bounds[1], 10, 64)
	if err1 != nil || err2 != nil || start < 0 || end < start || end >= int64(len(item.Content)) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	slice := item.Content[start : end+1]
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(item.Content)))
	w.Header().Set("Content-Length", strconv.Itoa(len(slice)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(slice)
}

type inMemoryBoxRestoreStore struct {
	db.RestoreStore
	mu              sync.Mutex
	repoConn        *db.BackupRepositoryConnection
	runTarget       *db.RestoreRunTargetConnection
	item            *db.RestoreItem
	itemClaimed     bool
	blocks          []db.RestoreItemBlock
	completed       bool
	verification    string
	completedBytes  int64
	retriedCode     string
	failedCode      string
	warnedCode      string
}

func (s *inMemoryBoxRestoreStore) RecoverStaleRestoreItemsContext(context.Context) (int64, error) {
	return 0, nil
}
func (s *inMemoryBoxRestoreStore) ExpireRestorePreviewsContext(context.Context) error { return nil }
func (s *inMemoryBoxRestoreStore) RecoverStaleRestorePlanningContext(context.Context) error {
	return nil
}
func (s *inMemoryBoxRestoreStore) ClaimNextRestorePreviewForWorkerContext(context.Context, string) (*db.RestorePreview, error) {
	return nil, nil
}
func (s *inMemoryBoxRestoreStore) ClaimNextQueuedRestoreRunForWorkerContext(context.Context, string) (*db.RestoreJob, *db.RestoreRun, error) {
	return nil, nil, nil
}
func (s *inMemoryBoxRestoreStore) SkipBlockedRestoreItemsContext(context.Context) (int64, error) {
	return 0, nil
}
func (s *inMemoryBoxRestoreStore) ClaimNextRestoreItemForWorkerContext(context.Context, string) (*db.RestoreItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.itemClaimed {
		return nil, nil
	}
	s.itemClaimed = true
	return s.item, nil
}
func (s *inMemoryBoxRestoreStore) FinalizeCompletedRestoreRunsContext(context.Context) (int, error) {
	return 0, nil
}
func (s *inMemoryBoxRestoreStore) FinalizeCancellingRestoreRunsContext(context.Context) (int, error) {
	return 0, nil
}
func (s *inMemoryBoxRestoreStore) HeartbeatRestoreItemContext(context.Context, db.Lease, string, string) error {
	return nil
}
func (s *inMemoryBoxRestoreStore) GetRestoreRunTargetConnectionContext(context.Context, string) (*db.RestoreRunTargetConnection, error) {
	return s.runTarget, nil
}
func (s *inMemoryBoxRestoreStore) GetBackupRepositoryConnectionContext(context.Context, string) (*db.BackupRepositoryConnection, error) {
	return s.repoConn, nil
}
func (s *inMemoryBoxRestoreStore) ReserveRestoreTargetPathContext(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (s *inMemoryBoxRestoreStore) ListRestoreItemBlocksContext(context.Context, string, string) ([]db.RestoreItemBlock, error) {
	return s.blocks, nil
}
func (s *inMemoryBoxRestoreStore) CompleteRestoreItemContext(_ context.Context, _ db.Lease, _ string, sizeBytes int64, verificationKind string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = true
	s.completedBytes = sizeBytes
	s.verification = verificationKind
	return true, nil
}
func (s *inMemoryBoxRestoreStore) CompleteRestoreDirectoryContext(_ context.Context, _ db.Lease, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = true
	return nil
}
func (s *inMemoryBoxRestoreStore) WarnRestoreItemContext(_ context.Context, _ db.Lease, _ string, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warnedCode = code
	return nil
}
func (s *inMemoryBoxRestoreStore) RetryRestoreItemContext(_ context.Context, _ db.Lease, _ string, code string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retriedCode = code
	return true, nil
}
func (s *inMemoryBoxRestoreStore) FailRestoreItemContext(_ context.Context, _ db.Lease, _ string, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failedCode = code
	return nil
}

func buildTestPackWithTwoBlocks(t *testing.T, otherData, targetData []byte) (packBytes []byte, packSHA256 [sha256.Size]byte, targetOffset int64) {
	t.Helper()
	entries := []backuprepo.Entry{
		{Hash: sha256.Sum256(otherData), Data: otherData},
		{Hash: sha256.Sum256(targetData), Data: targetData},
	}
	var buf bytes.Buffer
	pack, err := backuprepo.EncodePack(&buf, entries)
	if err != nil {
		t.Fatalf("EncodePack: %v", err)
	}
	targetHash := entries[1].Hash
	_, err = backuprepo.ValidatePack(bytes.NewReader(buf.Bytes()), pack.ID, func(offset int64, entry backuprepo.Entry) error {
		if entry.Hash == targetHash {
			targetOffset = offset
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ValidatePack: %v", err)
	}
	return buf.Bytes(), pack.ID, targetOffset
}

func newBoxRestoreTestSetup(t *testing.T, repoSrv, targetSrv *boxRestoreFakeServer, targetPayload []byte, targetPath, conflictStrategy string) (*Coordinator, *inMemoryBoxRestoreStore) {
	t.Helper()
	const encKey = "01234567890123456789012345678901"

	repoPassEnc, err := crypto.EncryptWithDomain("box-repo-token", encKey, crypto.ConnectionCredentialDomain(true))
	if err != nil {
		t.Fatal(err)
	}
	targetPassEnc, err := crypto.EncryptWithDomain("box-target-token", encKey, crypto.ConnectionCredentialDomain(true))
	if err != nil {
		t.Fatal(err)
	}

	repoRefreshEnc, err := crypto.EncryptWithDomain("box-repo-refresh", encKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	targetRefreshEnc, err := crypto.EncryptWithDomain("box-target-refresh", encKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		t.Fatal(err)
	}

	otherData := bytes.Repeat([]byte("X"), 4096)
	packBytes, packID, targetOffset := buildTestPackWithTwoBlocks(t, otherData, targetPayload)
	packHex := hex.EncodeToString(packID[:])
	packRemotePath := "/repo/.clumoove-backup/r1/packs/" + packHex + ".pack"

	repoRoot := repoSrv.addFolder("0", "repo")
	repoClumoove := repoSrv.addFolder(repoRoot, ".clumoove-backup")
	repoR1 := repoSrv.addFolder(repoClumoove, "r1")
	repoPacks := repoSrv.addFolder(repoR1, "packs")
	repoSrv.addFile(repoPacks, packHex+".pack", packBytes)

	targetHash := sha256.Sum256(targetPayload)
	blockHash := sha256.Sum256(targetPayload)

	store := &inMemoryBoxRestoreStore{
		repoConn: &db.BackupRepositoryConnection{
			BackupJobID:           "bjob-1",
			UserID:                "user-1",
			Provider:              "box",
			PasswordEncrypted:     repoPassEnc,
			RefreshTokenEncrypted: sql.NullString{String: repoRefreshEnc, Valid: true},
			TokenExpiresAt:        sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true},
		},
		runTarget: &db.RestoreRunTargetConnection{
			BackupJobID:             "bjob-1",
			TargetProvider:          "box",
			ConflictStrategy:        conflictStrategy,
			TargetPasswordEncrypted: targetPassEnc,
			TargetRefreshEncrypted:  sql.NullString{String: targetRefreshEnc, Valid: true},
			TargetTokenExpiresAt:    sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true},
		},
		item: &db.RestoreItem{
			ID:                   "ritem-1",
			RestoreRunID:         "rrun-1",
			SnapshotRelativePath: "docs/target.txt",
			SizeBytes:            int64(len(targetPayload)),
			FileSHA256:           targetHash[:],
			TargetPath:           targetPath,
			ClaimEpoch:           1,
		},
		blocks: []db.RestoreItemBlock{
			{
				Ordinal:       0,
				PackPath:      packRemotePath,
				PackSHA256:    packID[:],
				PackSizeBytes: int64(len(packBytes)),
				PayloadOffset: targetOffset,
				PayloadLength: len(targetPayload),
				BlockSHA256:   blockHash[:],
				PlaintextSize: len(targetPayload),
			},
		},
	}

	packReaders, err := NewPackReaderLimiter(2)
	if err != nil {
		t.Fatal(err)
	}
	coord := &Coordinator{
		store:         store,
		encryptionKey: encKey,
		packReaders:   packReaders,
		workerID:      "restore-box-test",
	}
	coord.SetRepositorySourceFactory(func(context.Context, string, string, string, string, ...storage.ProviderOption) (RepositorySource, error) {
		return repoSrv.newProvider(t), nil
	})
	coord.SetRestoreTargetFactory(func(context.Context, string, string, string, string, ...storage.ProviderOption) (RestoreTarget, error) {
		return targetSrv.newProvider(t), nil
	})
	return coord, store
}

func TestRestoreFromBoxUsesRangeReadsAndVerifiesSHA1(t *testing.T) {
	repoSrv := newBoxRestoreFakeServer("box-repo-token")
	defer repoSrv.close()
	targetSrv := newBoxRestoreFakeServer("box-target-token")
	defer targetSrv.close()

	payload := []byte("restored-from-box-ranged-and-sha1-verified")
	coord, store := newBoxRestoreTestSetup(t, repoSrv, targetSrv, payload, "/restored/target.txt", "RENAME")

	worked, err := coord.PollOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("PollOnce() = (%v, %v), want (true, nil)", worked, err)
	}
	if !store.completed {
		t.Fatalf("item not completed: retried=%q failed=%q", store.retriedCode, store.failedCode)
	}
	if store.verification != "HASH_VERIFIED" {
		t.Fatalf("verificationKind = %q, want HASH_VERIFIED (via Box SHA-1)", store.verification)
	}

	repoSrv.mu.Lock()
	ranges := append([]string(nil), repoSrv.downloadRanges...)
	fulls := repoSrv.fullDownloads
	auths := append([]string(nil), repoSrv.downloadAuthHeaders...)
	repoSrv.mu.Unlock()

	if fulls != 0 || len(ranges) != 1 {
		t.Fatalf("repository pack downloads: full=%d ranges=%v, want full=0 and exactly 1 byte-range request", fulls, ranges)
	}
	wantRange := fmt.Sprintf("bytes=%d-%d", store.blocks[0].PayloadOffset, store.blocks[0].PayloadOffset+int64(len(payload))-1)
	if ranges[0] != wantRange {
		t.Fatalf("Range header = %q, want %q", ranges[0], wantRange)
	}
	for _, auth := range auths {
		if auth != "" {
			t.Fatalf("download redirect leaked Authorization header: %q", auth)
		}
	}

	boxTarget := targetSrv.newProvider(t)
	defer boxTarget.Close()
	dl, err := boxTarget.StreamDownload(context.Background(), "files", "/restored/target.txt")
	if err != nil {
		t.Fatalf("StreamDownload(restored target): %v", err)
	}
	got, _ := io.ReadAll(dl)
	dl.Close()
	if !bytes.Equal(got, payload) {
		t.Fatalf("restored content = %q, want %q", got, payload)
	}
}

func TestRestoreIntoBoxRejectsSHA1Mismatch(t *testing.T) {
	repoSrv := newBoxRestoreFakeServer("box-repo-token")
	defer repoSrv.close()
	targetSrv := newBoxRestoreFakeServer("box-target-token")
	defer targetSrv.close()
	targetSrv.corruptReportedSHA1 = true

	payload := []byte("payload-whose-target-sha1-corrupts")
	coord, store := newBoxRestoreTestSetup(t, repoSrv, targetSrv, payload, "/restored/corrupt.txt", "RENAME")

	err := coord.restoreItem(context.Background(), store.item)
	if err == nil || !strings.Contains(err.Error(), "restore target hash verification failed") {
		t.Fatalf("restoreItem() err = %v, want 'restore target hash verification failed'", err)
	}
	if store.completed {
		t.Fatal("restore item completed despite target Box SHA-1 mismatch")
	}
}

func TestRestoreIntoBoxOverwriteExistingFile(t *testing.T) {
	repoSrv := newBoxRestoreFakeServer("box-repo-token")
	defer repoSrv.close()
	targetSrv := newBoxRestoreFakeServer("box-target-token")
	defer targetSrv.close()

	restoredDirID := targetSrv.addFolder("0", "restored")
	existingFileID := targetSrv.addFile(restoredDirID, "report.txt", []byte("stale-box-version"))

	newPayload := []byte("fresh-restored-version-content")
	coord, store := newBoxRestoreTestSetup(t, repoSrv, targetSrv, newPayload, "/restored/report.txt", "OVERWRITE")

	worked, err := coord.PollOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("PollOnce() = (%v, %v), want (true, nil)", worked, err)
	}
	if !store.completed {
		t.Fatalf("overwrite restore not completed: retried=%q failed=%q", store.retriedCode, store.failedCode)
	}
	if store.verification != "HASH_VERIFIED" {
		t.Fatalf("verificationKind = %q, want HASH_VERIFIED", store.verification)
	}

	got, ok := targetSrv.getFileContent(existingFileID)
	if !ok || !bytes.Equal(got, newPayload) {
		t.Fatalf("existing file ID %s content = (%q, %v), want %q", existingFileID, got, ok, newPayload)
	}
}

func TestRestoreIntoBoxDirectoryOverwriteDeletesBlockingFile(t *testing.T) {
	targetSrv := newBoxRestoreFakeServer("box-target-token")
	defer targetSrv.close()

	restoredID := targetSrv.addFolder("0", "restored")
	targetSrv.addFile(restoredID, "docs", []byte("blocking-file"))

	boxTarget := targetSrv.newProvider(t)
	defer boxTarget.Close()

	store := &inMemoryBoxRestoreStore{}
	coord := &Coordinator{store: store}
	item := &db.RestoreItem{
		ID:           "ritem-dir-1",
		RestoreRunID: "rrun-dir-1",
		IsDir:        true,
		TargetPath:   "/restored/docs",
		ClaimEpoch:   1,
	}

	if err := coord.restoreDirectory(context.Background(), boxTarget, item, "OVERWRITE"); err != nil {
		t.Fatalf("restoreDirectory(OVERWRITE blocking file): %v", err)
	}
	if !store.completed {
		t.Fatal("restoreDirectory did not mark directory item completed")
	}
	res, err := boxTarget.InspectResource(context.Background(), "files", "/restored/docs")
	if err != nil || !res.IsDir {
		t.Fatalf("InspectResource(/restored/docs) = (%+v, %v), want IsDir=true", res, err)
	}
}
