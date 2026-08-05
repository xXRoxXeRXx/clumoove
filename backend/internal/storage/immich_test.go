package storage

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNewImmichProviderNormalizesAPIBase(t *testing.T) {
	p, err := NewImmichProvider("https://photos.example.test/immich/api/", "key")
	if err != nil {
		t.Fatalf("NewImmichProvider() error = %v", err)
	}
	defer p.Close()
	if p.BaseURL != "https://photos.example.test/immich/api" {
		t.Errorf("BaseURL = %q", p.BaseURL)
	}
	if !p.UsesNativeDuplicateDetection() || p.SupportsAtomicRename() || p.VerificationMode() != VerificationCryptographicHash {
		t.Error("Immich capabilities are incorrect")
	}
}

func TestImmichUnsupportedOperations(t *testing.T) {
	p, err := NewImmichProvider("https://photos.example.test", "key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.GetFileHash(context.Background(), "files", "/asset"); !errors.Is(err, ErrChecksumNotAvailable) {
		t.Errorf("GetFileHash() error = %v, want ErrChecksumNotAvailable", err)
	}
	if err := p.DeleteFile(context.Background(), "files", "/asset"); err == nil {
		t.Error("DeleteFile() unexpectedly succeeded")
	}
}

func TestSetImmichTargetAssetIDPreservesSourceMetadata(t *testing.T) {
	meta := FileMetadata{CustomProps: map[string]string{
		"immich_asset_id": "source-asset",
		"immich_filename": "photo.jpg",
	}}
	SetImmichTargetAssetID(&meta, "target-asset")
	if meta.CustomProps["immich_asset_id"] != "source-asset" || meta.CustomProps["immich_filename"] != "photo.jpg" {
		t.Fatalf("source metadata was changed: %#v", meta.CustomProps)
	}
	if meta.CustomProps["immich_target_asset_id"] != "target-asset" {
		t.Fatalf("target asset ID = %q", meta.CustomProps["immich_target_asset_id"])
	}
}

func TestImmichVerificationUsesPersistedTargetAssetID(t *testing.T) {
	checksum := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xab}, sha1.Size))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/assets/target-asset" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		w.Header().Set("ETag", `"not-a-checksum"`)
		_, _ = w.Write([]byte(`{"id":"target-asset","checksum":"` + checksum + `","exifInfo":{"fileSizeInByte":123}}`))
	}))
	defer server.Close()

	p := &ImmichProvider{BaseURL: server.URL + "/api", APIKey: "test-key", HTTPClient: server.Client()}
	ctx := WithTargetResourceID(context.Background(), "target-asset")
	hash, err := p.GetFileHash(ctx, "files", "/album/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if want := "SHA1:" + strings.Repeat("ab", sha1.Size); hash != want {
		t.Fatalf("GetFileHash() = %q, want %q", hash, want)
	}
	exists, size, err := p.FileExists(ctx, "files", "/album/photo.jpg")
	if err != nil || !exists || size != 123 {
		t.Fatalf("FileExists() = (%v, %d, %v)", exists, size, err)
	}
}

func TestImmichVerificationAssetLookupEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		name       string
		response   string
		status     int
		wantExists bool
		wantErr    error
	}{
		{name: "missing asset", status: http.StatusNotFound, wantExists: false},
		{name: "invalid checksum", status: http.StatusOK, response: `{"id":"asset","checksum":"invalid","exifInfo":{"fileSizeInByte":1}}`, wantExists: true, wantErr: ErrChecksumNotAvailable},
		{name: "trashed asset", status: http.StatusOK, response: `{"id":"asset","isTrashed":true,"checksum":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, sha1.Size)) + `","exifInfo":{"fileSizeInByte":1}}`, wantExists: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			p := &ImmichProvider{BaseURL: server.URL, HTTPClient: server.Client()}
			ctx := WithTargetResourceID(context.Background(), "asset")
			exists, _, err := p.FileExists(ctx, "files", "/ignored")
			if err != nil || exists != tc.wantExists {
				t.Fatalf("FileExists() = (%v, _, %v), want (%v, _, nil)", exists, err, tc.wantExists)
			}
			_, err = p.GetFileHash(ctx, "files", "/ignored")
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("GetFileHash() error = %v, want %v", err, tc.wantErr)
			}
			if tc.status == http.StatusNotFound && !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetFileHash() error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestImmichStreamDownloadReadsOriginal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/assets/asset-id/original" || r.URL.Query().Get("edited") != "false" {
			t.Errorf("unexpected request %s", r.URL.String())
		}
		_, _ = w.Write([]byte("asset bytes"))
	}))
	defer server.Close()

	p := &ImmichProvider{BaseURL: server.URL, HTTPClient: server.Client()}
	stream, err := p.StreamDownload(context.Background(), "files", "/Timeline/asset-id")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	body, err := io.ReadAll(stream)
	if err != nil || string(body) != "asset bytes" {
		t.Fatalf("body = %q, err = %v", body, err)
	}
}

func TestImmichStreamUploadChunkedLeavesProgressChannelOpen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/assets" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm() error = %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"asset-id","status":"created"}`))
	}))
	defer server.Close()

	p := &ImmichProvider{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	progress := make(chan int64, 16)
	const largeFileSize = 50*1024*1024 + 1 // processor's chunked-upload threshold plus one byte
	receipt := &UploadReceipt{}
	ctx := WithUploadReceipt(context.Background(), receipt)
	if err := p.StreamUploadChunked(ctx, "files", "/large.jpg", bytes.NewReader([]byte("asset bytes")), largeFileSize, progress); err != nil {
		t.Fatalf("StreamUploadChunked() error = %v", err)
	}
	if receipt.TargetResourceID != "asset-id" {
		t.Fatalf("receipt target resource ID = %q", receipt.TargetResourceID)
	}

	// Progress channel lifecycle belongs to the caller. This would panic if the
	// provider closed it after completing the chunked upload.
	close(progress)
	var reported int64
	for n := range progress {
		reported += n
	}
	if reported != int64(len("asset bytes")) {
		t.Errorf("reported progress = %d, want %d", reported, len("asset bytes"))
	}
}

func TestImmichStreamUploadDuplicateDoesNotPopulateReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"existing-asset","status":"duplicate"}`))
	}))
	defer server.Close()
	p := &ImmichProvider{BaseURL: server.URL, HTTPClient: server.Client()}
	receipt := &UploadReceipt{}
	err := p.StreamUpload(WithUploadReceipt(context.Background(), receipt), "files", "/photo.jpg", bytes.NewReader([]byte("bytes")), 5)
	if !errors.Is(err, ErrNativeDuplicate) {
		t.Fatalf("StreamUpload() error = %v, want ErrNativeDuplicate", err)
	}
	if receipt.TargetResourceID != "" {
		t.Fatalf("duplicate upload populated receipt with %q", receipt.TargetResourceID)
	}
}

func TestImmichStreamUploadChunkedDuplicateDoesNotPopulateReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"existing-asset","status":"duplicate"}`))
	}))
	defer server.Close()
	p := &ImmichProvider{BaseURL: server.URL, HTTPClient: server.Client()}
	receipt := &UploadReceipt{}
	progress := make(chan int64, 1)
	err := p.StreamUploadChunked(WithUploadReceipt(context.Background(), receipt), "files", "/photo.jpg", bytes.NewReader([]byte("bytes")), 5, progress)
	if !errors.Is(err, ErrNativeDuplicate) {
		t.Fatalf("StreamUploadChunked() error = %v, want ErrNativeDuplicate", err)
	}
	if receipt.TargetResourceID != "" {
		t.Fatalf("duplicate upload populated receipt with %q", receipt.TargetResourceID)
	}
}

func TestImmichSearchReturnsFlatLibrary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/search/metadata":
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("search Content-Type = %q", got)
			}
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request["withExif"] != true {
				t.Error("search request did not include withExif: true")
			}
			if _, ok := request["albumIds"]; ok {
				t.Error("search unexpectedly sent albumIds")
			}
			_, _ = w.Write([]byte(`{"assets":{"items":[{"id":"asset-1","originalFileName":"one.jpg"}],"nextPage":null}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	p := &ImmichProvider{BaseURL: server.URL + "/api", APIKey: "key", HTTPClient: server.Client()}
	items, err := p.GetDirectoryListing(context.Background(), "files", "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Path != "/asset-1" || items[0].Name != "one.jpg" {
		t.Errorf("items = %#v", items)
	}
}

func TestImmichNonRootListingResolvesSingleAsset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/search/metadata":
			_, _ = w.Write([]byte(`{"assets":{"items":[{"id":"asset-1","originalFileName":"one.jpg"},{"id":"asset-2","originalFileName":"two.jpg"}],"nextPage":null}}`))
		case "/api/assets/asset-1":
			_, _ = w.Write([]byte(`{"id":"asset-1","originalFileName":"one.jpg","exifInfo":{"fileSizeInByte":1}}`))
		case "/api/assets/asset-2":
			_, _ = w.Write([]byte(`{"id":"asset-2","originalFileName":"two.jpg","exifInfo":{"fileSizeInByte":2}}`))
		case "/api/assets/nonexistent":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	p := &ImmichProvider{BaseURL: server.URL + "/api", APIKey: "key", HTTPClient: server.Client()}

	t.Run("non-root path returns single matching asset", func(t *testing.T) {
		items, err := p.GetDirectoryListing(context.Background(), "files", "/asset-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].Path != "/asset-1" || items[0].Name != "one.jpg" {
			t.Errorf("items = %#v", items)
		}
	})

	t.Run("non-root path returns error for missing asset", func(t *testing.T) {
		if _, err := p.GetDirectoryListing(context.Background(), "files", "/nonexistent"); err == nil {
			t.Fatal("expected error for missing asset")
		}
	})

	t.Run("inspect resource is direct O(1) lookup", func(t *testing.T) {
		res, err := p.InspectResource(context.Background(), "files", "/asset-2")
		if err != nil {
			t.Fatal(err)
		}
		if res.Path != "/asset-2" || res.Name != "two.jpg" || res.Size != 2 {
			t.Errorf("res = %#v", res)
		}
	})
}

func TestImmichSearchAcceptsStringNextPage(t *testing.T) {
	var pages int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search/metadata" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			return
		}
		atomic.AddInt32(&pages, 1)
		var request struct {
			Page int `json:"page"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Page == 1 {
			_, _ = w.Write([]byte(`{"assets":{"items":[{"id":"asset-1","originalFileName":"one.jpg"}],"nextPage":"2"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"assets":{"items":[{"id":"asset-2","originalFileName":"two.jpg"}],"nextPage":null}}`))
	}))
	defer server.Close()
	p := &ImmichProvider{BaseURL: server.URL + "/api", APIKey: "key", HTTPClient: server.Client()}
	assets, err := p.search(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 2 {
		t.Errorf("assets = %d, want 2", len(assets))
	}
	if got := atomic.LoadInt32(&pages); got != 2 {
		t.Errorf("search requests = %d, want 2", got)
	}
}

func TestImmichRootListingSearchPayloadAndSizeMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if _, ok := request["albumIds"]; ok {
			t.Error("library search unexpectedly sent albumIds")
		}
		if request["withArchived"] != false || request["withDeleted"] != false {
			t.Errorf("archive filters = withArchived:%v withDeleted:%v, want false", request["withArchived"], request["withDeleted"])
		}
		if request["withExif"] != true {
			t.Errorf("withExif = %v, want true", request["withExif"])
		}
		_, _ = w.Write([]byte(`{"assets":{"items":[{"id":"asset-id","originalFileName":"photo.jpg","exifInfo":{"fileSizeInByte":12345}}]}}`))
	}))
	defer server.Close()
	p := &ImmichProvider{BaseURL: server.URL, HTTPClient: server.Client()}
	items, err := p.GetDirectoryListing(context.Background(), "files", "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Path != "/asset-id" || items[0].Name != "photo.jpg" || items[0].Size != 12345 {
		t.Errorf("items = %#v", items)
	}
}
