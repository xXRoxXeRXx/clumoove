package processor

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/db"
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
