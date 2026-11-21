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
	"testing"
	"time"

	"backend/internal/db"
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
