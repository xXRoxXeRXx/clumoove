package sync

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
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

type boxSyncFakeVersion struct {
	Size              int64
	SHA1              string
	Content           []byte
	ContentModifiedAt string
}

type boxSyncFakeItem struct {
	ID                string
	Type              string // "folder", "file", "web_link"
	Name              string
	ParentID          string
	Size              int64
	SHA1              string
	Content           []byte
	ModifiedAt        string
	ContentModifiedAt string
	URL               string
	Versions          []boxSyncFakeVersion
}

type boxSyncFakeServer struct {
	server *httptest.Server
	token  string

	mu       sync.Mutex
	items    map[string]boxSyncFakeItem
	children map[string][]string
	requests []string
	nextID   int
}

func newBoxSyncFakeServer(token string) *boxSyncFakeServer {
	s := &boxSyncFakeServer{
		token:    token,
		items:    make(map[string]boxSyncFakeItem),
		children: make(map[string][]string),
		nextID:   100,
	}
	s.items["0"] = boxSyncFakeItem{
		ID:         "0",
		Type:       "folder",
		Name:       "All Files",
		ModifiedAt: "2026-01-01T00:00:00Z",
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *boxSyncFakeServer) close() { s.server.Close() }

func (s *boxSyncFakeServer) newProvider(t *testing.T) *storage.BoxProvider {
	t.Helper()
	p, err := storage.NewBoxTestProvider(s.token, s.server.URL+"/2.0", s.server.URL+"/api/2.0", s.server.Client())
	if err != nil {
		t.Fatalf("NewBoxTestProvider: %v", err)
	}
	return p
}

func (s *boxSyncFakeServer) addFolder(parentID, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := strconv.Itoa(s.nextID)
	s.items[id] = boxSyncFakeItem{
		ID:         id,
		Type:       "folder",
		Name:       name,
		ParentID:   parentID,
		ModifiedAt: "2026-01-01T00:00:00Z",
	}
	s.children[parentID] = append(s.children[parentID], id)
	return id
}

func (s *boxSyncFakeServer) addFile(parentID, name string, content []byte, mtime string) string {
	sum := sha1.Sum(content)
	sha1Hex := hex.EncodeToString(sum[:])
	copied := append([]byte(nil), content...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := strconv.Itoa(s.nextID)
	s.items[id] = boxSyncFakeItem{
		ID:                id,
		Type:              "file",
		Name:              name,
		ParentID:          parentID,
		Size:              int64(len(copied)),
		SHA1:              sha1Hex,
		Content:           copied,
		ModifiedAt:        mtime,
		ContentModifiedAt: mtime,
		Versions: []boxSyncFakeVersion{{
			Size:              int64(len(copied)),
			SHA1:              sha1Hex,
			Content:           append([]byte(nil), copied...),
			ContentModifiedAt: mtime,
		}},
	}
	s.children[parentID] = append(s.children[parentID], id)
	return id
}

func (s *boxSyncFakeServer) addWebLink(parentID, name, rawURL string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := strconv.Itoa(s.nextID)
	s.items[id] = boxSyncFakeItem{
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

func (s *boxSyncFakeServer) handle(w http.ResponseWriter, r *http.Request) {
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
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	s.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/2.0/users/me":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","type":"user","name":"Box Sync User","login":"sync@example.test"}`))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/folders/") && strings.HasSuffix(r.URL.Path, "/items"):
		folderID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2.0/folders/"), "/items")
		s.handleList(w, folderID)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/folders/"):
		folderID := strings.TrimPrefix(r.URL.Path, "/2.0/folders/")
		s.handleGetFolder(w, folderID)
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
	default:
		http.NotFound(w, r)
	}
}

func (s *boxSyncFakeServer) handleGetFolder(w http.ResponseWriter, folderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[folderID]
	if !ok || it.Type != "folder" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":          it.ID,
		"type":        it.Type,
		"name":        it.Name,
		"modified_at": it.ModifiedAt,
	})
}

func (s *boxSyncFakeServer) handleList(w http.ResponseWriter, folderID string) {
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
			"url":                 it.URL,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries, "limit": 1000, "next_marker": ""})
}

func (s *boxSyncFakeServer) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Parent struct {
			ID string `json:"id"`
		} `json:"parent"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
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
	it := boxSyncFakeItem{
		ID:         id,
		Type:       "folder",
		Name:       body.Name,
		ParentID:   body.Parent.ID,
		ModifiedAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.items[id] = it
	s.children[body.Parent.ID] = append(s.children[body.Parent.ID], id)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": it.ID, "type": it.Type, "name": it.Name})
}

func (s *boxSyncFakeServer) parseSingleMultipart(w http.ResponseWriter, r *http.Request) (name, parentID, modAt, sha1Hex string, data []byte, ok bool) {
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

func (s *boxSyncFakeServer) handleSingleUpload(w http.ResponseWriter, r *http.Request) {
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
	it := boxSyncFakeItem{
		ID: id, Type: "file", Name: name, ParentID: parentID,
		Size: int64(len(copied)), SHA1: sha1Hex, Content: copied,
		ModifiedAt: "2026-03-01T00:00:00Z", ContentModifiedAt: modAt,
		Versions: []boxSyncFakeVersion{{
			Size: int64(len(copied)), SHA1: sha1Hex, Content: append([]byte(nil), copied...), ContentModifiedAt: modAt,
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

func (s *boxSyncFakeServer) handleSingleUploadVersion(w http.ResponseWriter, r *http.Request, fileID string) {
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
	it.ModifiedAt = "2026-03-02T00:00:00Z"
	it.ContentModifiedAt = modAt
	it.Versions = append(it.Versions, boxSyncFakeVersion{
		Size: int64(len(copied)), SHA1: sha1Hex, Content: append([]byte(nil), copied...), ContentModifiedAt: modAt,
	})
	s.items[fileID] = it
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total_count": 1,
		"entries":     []map[string]any{{"id": it.ID, "type": "file", "name": it.Name, "size": it.Size, "sha1": it.SHA1}},
	})
}

func (s *boxSyncFakeServer) updateFile(id string, content []byte, mtime string) {
	sum := sha1.Sum(content)
	sha1Hex := hex.EncodeToString(sum[:])
	copied := append([]byte(nil), content...)
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[id]
	it.Size = int64(len(copied))
	it.SHA1 = sha1Hex
	it.Content = copied
	it.ModifiedAt = mtime
	it.ContentModifiedAt = mtime
	it.Versions = append(it.Versions, boxSyncFakeVersion{
		Size: int64(len(copied)), SHA1: sha1Hex, Content: append([]byte(nil), copied...), ContentModifiedAt: mtime,
	})
	s.items[id] = it
}

type peerSyncFile struct {
	data  []byte
	mtime time.Time
}

type peerSyncEndpoint struct {
	mu    sync.Mutex
	dirs  map[string]bool
	files map[string]peerSyncFile
}

func newPeerSyncEndpoint() *peerSyncEndpoint {
	return &peerSyncEndpoint{
		dirs:  map[string]bool{"/": true},
		files: make(map[string]peerSyncFile),
	}
}

func (p *peerSyncEndpoint) putFile(filePath string, data []byte, mtime time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	clean := cleanRelPath(filePath)
	for dir := path.Dir(clean); ; dir = path.Dir(dir) {
		p.dirs[dir] = true
		if dir == "/" {
			break
		}
	}
	p.files[clean] = peerSyncFile{data: append([]byte(nil), data...), mtime: mtime}
}

func (p *peerSyncEndpoint) InspectResource(_ context.Context, _, resourcePath string) (storage.CloudResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	clean := cleanRelPath(resourcePath)
	if p.dirs[clean] {
		return storage.CloudResource{Path: clean, Name: path.Base(clean), IsDir: true}, nil
	}
	if f, ok := p.files[clean]; ok {
		sum := sha1.Sum(f.data)
		return storage.CloudResource{
			Path:         clean,
			Name:         path.Base(clean),
			Size:         int64(len(f.data)),
			Hash:         "SHA1:" + hex.EncodeToString(sum[:]),
			LastModified: f.mtime,
		}, nil
	}
	return storage.CloudResource{}, storage.ErrNotFound
}

func (p *peerSyncEndpoint) GetDirectoryListing(_ context.Context, _, dirPath string) ([]storage.CloudResource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cleanDir := cleanRelPath(dirPath)
	if !p.dirs[cleanDir] {
		return nil, storage.ErrNotFound
	}
	var out []storage.CloudResource
	for d := range p.dirs {
		if d != cleanDir && path.Dir(d) == cleanDir {
			out = append(out, storage.CloudResource{Path: d, Name: path.Base(d), IsDir: true})
		}
	}
	for fp, f := range p.files {
		if path.Dir(fp) == cleanDir {
			sum := sha1.Sum(f.data)
			out = append(out, storage.CloudResource{
				Path:         fp,
				Name:         path.Base(fp),
				Size:         int64(len(f.data)),
				Hash:         "SHA1:" + hex.EncodeToString(sum[:]),
				LastModified: f.mtime,
			})
		}
	}
	return out, nil
}

func TestListFilesSkipsBoxNonTransferableItems(t *testing.T) {
	srv := newBoxSyncFakeServer("box-sync-token")
	defer srv.close()

	docsID := srv.addFolder("0", "Docs")
	srv.addFile(docsID, "readme.txt", []byte("hello"), "2026-03-01T00:00:00Z")
	srv.addFile(docsID, "Meeting.boxnote", []byte(`{"doc":"note"}`), "2026-03-01T00:00:00Z")
	srv.addFile(docsID, "Diagram.boxcanvas", []byte(`{"doc":"canvas"}`), "2026-03-01T00:00:00Z")
	srv.addWebLink(docsID, "Portal", "https://example.test")

	boxClient := srv.newProvider(t)
	defer boxClient.Close()

	engine := NewEngine(nil, nil, "secret")
	files, dirs, _, errs, err := engine.listFiles(context.Background(), boxClient, []string{"/"}, nil, nil)
	if err != nil {
		t.Fatalf("listFiles() error = %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("listFiles() traversal errors = %+v, want none", errs)
	}
	if !dirs["/Docs"] {
		t.Fatalf("dirs = %+v, want /Docs", dirs)
	}
	if len(files) != 1 {
		t.Fatalf("listed files = %+v, want only /Docs/readme.txt (skipping .boxnote, .boxcanvas, web_link)", files)
	}
	if _, ok := files["/Docs/readme.txt"]; !ok {
		t.Fatalf("expected /Docs/readme.txt in listed files, got %+v", files)
	}
}

func TestIsFileMatchingTargetCrossAlgorithmVsSHA1(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	// Matching SHA-1 overrides timestamp skew.
	if !isFileMatchingTarget(
		fileState{Path: "/a.txt", Size: 10, LastModified: t0, Hash: "SHA1:2aae6c35c94fcfb415dbe95f408b9ce91ee846ed"},
		fileState{Path: "/a.txt", Size: 10, LastModified: t0.Add(10 * time.Minute), Hash: "SHA1:2aae6c35c94fcfb415dbe95f408b9ce91ee846ed"},
	) {
		t.Fatal("matching SHA-1 with skewed timestamps returned false, want true")
	}

	// Mismatched SHA-1 detects change even when size and timestamp match.
	if isFileMatchingTarget(
		fileState{Path: "/a.txt", Size: 10, LastModified: t0, Hash: "SHA1:2aae6c35c94fcfb415dbe95f408b9ce91ee846ed"},
		fileState{Path: "/a.txt", Size: 10, LastModified: t0, Hash: "SHA1:0000000000000000000000000000000000000000"},
	) {
		t.Fatal("mismatched SHA-1 with matching size and mtime returned true, want false")
	}

	// Cross-algorithm hashes (Box SHA-1 vs Google MD5) are not directly comparable
	// and fall back to size + mtime tolerance instead of always mismatching.
	if !isFileMatchingTarget(
		fileState{Path: "/a.txt", Size: 10, LastModified: t0, Hash: "SHA1:2aae6c35c94fcfb415dbe95f408b9ce91ee846ed"},
		fileState{Path: "/a.txt", Size: 10, LastModified: t0.Add(time.Second), Hash: "MD5:5eb63bbbe01eeed093cb22bb8f5acdc3"},
	) {
		t.Fatal("Box SHA-1 vs Google MD5 with matching size and mtime returned false, want true")
	}

	if isFileMatchingTarget(
		fileState{Path: "/a.txt", Size: 10, LastModified: t0, Hash: "SHA1:2aae6c35c94fcfb415dbe95f408b9ce91ee846ed"},
		fileState{Path: "/a.txt", Size: 10, LastModified: t0.Add(10 * time.Second), Hash: "MD5:5eb63bbbe01eeed093cb22bb8f5acdc3"},
	) {
		t.Fatal("Box SHA-1 vs Google MD5 with stale mtime returned true, want false")
	}
}

func TestBoxBidirectionalSyncPassesWithSHA1(t *testing.T) {
	ctx := context.Background()
	srv := newBoxSyncFakeServer("box-sync-token")
	defer srv.close()

	boxDocsID := srv.addFolder("0", "Docs")
	// Identical content on both sides with skewed mtimes: SHA-1 match prevents transfer.
	sharedBytes := []byte("shared-unchanged-content")
	srv.addFile(boxDocsID, "shared.txt", sharedBytes, "2026-01-10T00:00:00Z")
	// Present only on Box initially (source -> peer).
	fromBoxID := srv.addFile(boxDocsID, "from-box.txt", []byte("box-v1"), "2026-03-01T00:00:00Z")

	peer := newPeerSyncEndpoint()
	peer.putFile("/Docs/shared.txt", sharedBytes, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	peer.putFile("/Docs/from-peer.txt", []byte("peer-v1"), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))

	boxClient := srv.newProvider(t)
	defer boxClient.Close()
	if boxClient.SupportsAtomicRename() {
		t.Fatal("BoxProvider.SupportsAtomicRename() = true, want false")
	}

	engine := NewEngine(nil, nil, "secret")
	var prevSource map[string]db.SyncState
	var prevTarget map[string]db.SyncState
	var prevSourceDirs, prevTargetDirs map[string]bool
	var prevSourceFiles, prevTargetFiles map[string]fileState
	var prevSourceDirETags, prevTargetDirETags map[string]string

	runPass := func() ([]taskToCreate, []taskToCreate) {
		t.Helper()
		sourceMap, sourceDirMap, sourceDirETags, srcErrs, err := engine.listFiles(ctx, peer, []string{"/"}, prevSourceDirETags, prevSourceFiles)
		if err != nil || len(srcErrs) != 0 {
			t.Fatalf("listFiles(peer) = (errs=%+v, err=%v)", srcErrs, err)
		}
		targetMap, targetDirMap, targetDirETags, tgtErrs, err := engine.listFiles(ctx, boxClient, []string{"/"}, prevTargetDirETags, prevTargetFiles)
		if err != nil || len(tgtErrs) != 0 {
			t.Fatalf("listFiles(box) = (errs=%+v, err=%v)", tgtErrs, err)
		}

		renameTasks, fileTasks := calculateFileDelta("sync-box", "two_way", "OVERWRITE", false, sourceMap, targetMap, prevSource, prevTarget)
		dirTasks, _ := calculateDirectoryDelta("two_way", false, sourceDirMap, targetDirMap, prevSourceDirs, prevTargetDirs)

		var completed []completedTaskOperation
		for _, tc := range fileTasks {
			switch tc.action {
			case "upload":
				// Peer -> Box: direct upload without .tmp staging, Overwrite: true.
				f := peer.files[tc.filePath]
				sum := sha1.Sum(f.data)
				_, err := boxClient.StreamUploadWithOptions(ctx, "files", tc.filePath, bytes.NewReader(f.data), int64(len(f.data)), storage.UploadOptions{
					Metadata:  storage.FileMetadata{ModifiedTime: f.mtime},
					Checksum:  "SHA1:" + hex.EncodeToString(sum[:]),
					Overwrite: true,
				})
				if err != nil {
					t.Fatalf("upload to Box %s: %v", tc.filePath, err)
				}
				completed = append(completed, completedTaskOperation{filePath: tc.filePath, action: "upload"})
			case "download":
				// Box -> Peer: StreamDownload from Box.
				rc, err := boxClient.StreamDownload(ctx, "files", tc.filePath)
				if err != nil {
					t.Fatalf("download from Box %s: %v", tc.filePath, err)
				}
				data, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					t.Fatalf("read Box download %s: %v", tc.filePath, err)
				}
				res, err := boxClient.InspectResource(ctx, "files", tc.filePath)
				if err != nil {
					t.Fatalf("inspect Box %s: %v", tc.filePath, err)
				}
				peer.putFile(tc.filePath, data, res.LastModified)
				completed = append(completed, completedTaskOperation{filePath: tc.filePath, action: "download"})
			}
		}

		reconcileSuccessfulOperations(ctx, "/", peer, boxClient,
			sourceMap, targetMap, sourceDirMap, targetDirMap,
			sourceDirETags, targetDirETags, completed, map[string]bool{})

		upserts, _ := syncStateChanges("sync-box", sourceMap, targetMap, prevSource, prevTarget,
			sourceDirETags, targetDirETags, sourceDirMap, targetDirMap, prevSourceDirs, prevTargetDirs, nil)

		prevSource = make(map[string]db.SyncState)
		prevTarget = make(map[string]db.SyncState)
		prevSourceFiles = make(map[string]fileState)
		prevTargetFiles = make(map[string]fileState)
		prevSourceDirs = make(map[string]bool)
		prevTargetDirs = make(map[string]bool)
		prevSourceDirETags = make(map[string]string)
		prevTargetDirETags = make(map[string]string)
		for _, s := range upserts {
			if s.Size == -1 {
				if s.Side == "source" {
					prevSourceDirs[s.RelPath] = true
					if s.ETag != "" {
						prevSourceDirETags[s.RelPath] = s.ETag
					}
				} else {
					prevTargetDirs[s.RelPath] = true
					if s.ETag != "" {
						prevTargetDirETags[s.RelPath] = s.ETag
					}
				}
				continue
			}
			h := s.SourceHash
			if s.Side == "target" {
				h = s.TargetHash
			}
			fs := fileState{Path: s.RelPath, Size: s.Size, LastModified: s.Mtime.Time, Hash: h, ETag: s.ETag}
			if s.Side == "source" {
				prevSource[s.RelPath] = *s
				prevSourceFiles[s.RelPath] = fs
			} else {
				prevTarget[s.RelPath] = *s
				prevTargetFiles[s.RelPath] = fs
			}
		}

		if len(renameTasks) != 0 {
			t.Fatalf("unexpected renameTasks: %+v", renameTasks)
		}
		return fileTasks, dirTasks
	}

	// Pass 1: transfers only /Docs/from-peer.txt (upload -> Box) and /Docs/from-box.txt (download <- Box).
	// /Docs/shared.txt matches by SHA-1 despite skewed mtime and is not transferred.
	pass1Files, pass1Dirs := runPass()
	if len(pass1Dirs) != 0 {
		t.Fatalf("pass 1 dirTasks = %+v, want none", pass1Dirs)
	}
	if len(pass1Files) != 2 {
		t.Fatalf("pass 1 fileTasks = %+v, want 2 (from-peer upload, from-box download)", pass1Files)
	}
	for _, ft := range pass1Files {
		if ft.filePath == "/Docs/shared.txt" {
			t.Fatal("pass 1 transferred /Docs/shared.txt despite matching SHA-1")
		}
	}

	// Pass 2: unchanged tree transfers nothing.
	pass2Files, pass2Dirs := runPass()
	if len(pass2Files) != 0 || len(pass2Dirs) != 0 {
		t.Fatalf("pass 2 unchanged tree created tasks: files=%+v dirs=%+v", pass2Files, pass2Dirs)
	}

	// Pass 3: change one file on Peer (same length + same mtime, different SHA-1)
	// and one file on Box (same length + same mtime, different SHA-1).
	peer.putFile("/Docs/from-peer.txt", []byte("peer-v2"), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	srv.updateFile(fromBoxID, []byte("box-v2"), "2026-03-01T00:00:00Z")

	pass3Files, pass3Dirs := runPass()
	if len(pass3Dirs) != 0 || len(pass3Files) != 2 {
		t.Fatalf("pass 3 tasks = (files=%+v, dirs=%+v), want 2 file tasks only", pass3Files, pass3Dirs)
	}
	actions := map[string]string{}
	for _, ft := range pass3Files {
		actions[ft.filePath] = ft.action
	}
	if actions["/Docs/from-peer.txt"] != "upload" || actions["/Docs/from-box.txt"] != "download" {
		t.Fatalf("pass 3 actions = %+v, want upload for from-peer.txt and download for from-box.txt", actions)
	}

	// Verify Box wrote a new version without .tmp staging.
	srv.mu.Lock()
	var fromPeerBoxID string
	for _, cid := range srv.children[boxDocsID] {
		it := srv.items[cid]
		if strings.HasSuffix(it.Name, ".tmp") {
			srv.mu.Unlock()
			t.Fatalf("unexpected .tmp staging item on Box: %s", it.Name)
		}
		if it.Name == "from-peer.txt" {
			fromPeerBoxID = cid
		}
	}
	fromPeerBox := srv.items[fromPeerBoxID]
	srv.mu.Unlock()
	if len(fromPeerBox.Versions) != 2 || string(fromPeerBox.Content) != "peer-v2" {
		t.Fatalf("Box from-peer.txt versions=%d content=%q, want 2 versions and %q", len(fromPeerBox.Versions), fromPeerBox.Content, "peer-v2")
	}
	if got := string(peer.files["/Docs/from-box.txt"].data); got != "box-v2" {
		t.Fatalf("peer /Docs/from-box.txt = %q, want %q", got, "box-v2")
	}

	// Pass 4: unchanged tree transfers nothing after updates.
	pass4Files, pass4Dirs := runPass()
	if len(pass4Files) != 0 || len(pass4Dirs) != 0 {
		t.Fatalf("pass 4 unchanged tree created tasks: files=%+v dirs=%+v", pass4Files, pass4Dirs)
	}
}

