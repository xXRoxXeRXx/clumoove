package storage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	if !p.UsesNativeDuplicateDetection() || p.SupportsAtomicRename() {
		t.Error("Immich capabilities are incorrect")
	}
}

func TestImmichUnsupportedOperations(t *testing.T) {
	p, err := NewImmichProvider("https://photos.example.test", "key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.GetFileHash(context.Background(), "files", "/asset"); !errors.Is(err, ErrHashNotSupported) {
		t.Errorf("GetFileHash() error = %v, want ErrHashNotSupported", err)
	}
	if err := p.DeleteFile(context.Background(), "files", "/asset"); err == nil {
		t.Error("DeleteFile() unexpectedly succeeded")
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
	stream, err := p.StreamDownload(context.Background(), "files", "/All Assets/asset-id")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	body, err := io.ReadAll(stream)
	if err != nil || string(body) != "asset bytes" {
		t.Fatalf("body = %q, err = %v", body, err)
	}
}

func TestImmichJSONRequestsAndAlbumCache(t *testing.T) {
	var albumLists int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/search/metadata":
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("search Content-Type = %q", got)
			}
			var request struct {
				WithExif bool `json:"withExif"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if !request.WithExif {
				t.Error("search request did not include withExif: true")
			}
			_, _ = w.Write([]byte(`{"assets":{"items":[]}}`))
		case "/api/albums":
			if r.Method == http.MethodGet {
				atomic.AddInt32(&albumLists, 1)
				_, _ = w.Write([]byte(`[{"id":"album-1","albumName":"Target"}]`))
				return
			}
			t.Errorf("unexpected album method %s", r.Method)
		case "/api/albums/album-1/assets":
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("assignment Content-Type = %q", got)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	p := &ImmichProvider{BaseURL: server.URL + "/api", APIKey: "key", HTTPClient: server.Client(), albums: map[string]string{}, albumIDs: map[string]string{}}
	if _, err := p.search(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if err := p.assignTargetAlbum(context.Background(), "/Target", "asset-1"); err != nil {
		t.Fatal(err)
	}
	if err := p.assignTargetAlbum(context.Background(), "/Target", "asset-2"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&albumLists); got != 1 {
		t.Errorf("album list requests = %d, want 1", got)
	}
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
			Page    int    `json:"page"`
			AlbumID string `json:"albumId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Page == 1 {
			_, _ = w.Write([]byte(`{"assets":{"items":[{"id":"asset-1","originalFileName":"one.jpg"}],"nextPage":"2"}}`))
			return
		}
		if request.AlbumID != "album-id" {
			t.Errorf("albumId = %q, want album-id", request.AlbumID)
		}
		_, _ = w.Write([]byte(`{"assets":{"items":[{"id":"asset-2","originalFileName":"two.jpg"}],"nextPage":null}}`))
	}))
	defer server.Close()
	p := &ImmichProvider{BaseURL: server.URL + "/api", APIKey: "key", HTTPClient: server.Client(), albums: map[string]string{}, albumIDs: map[string]string{}}
	assets, err := p.search(context.Background(), "album-id")
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

func TestImmichAllAssetsSearchPayloadAndSizeMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if _, ok := request["albumId"]; ok {
			t.Error("all-assets search unexpectedly sent albumId")
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
	p := &ImmichProvider{BaseURL: server.URL, HTTPClient: server.Client(), albums: map[string]string{}, albumIDs: map[string]string{}}
	items, err := p.GetDirectoryListing(context.Background(), "files", "/All Assets")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Path != "/All Assets/asset-id" || items[0].Name != "photo.jpg" || items[0].Size != 12345 {
		t.Errorf("items = %#v", items)
	}
}

func TestImmichAssetVirtualPathIsNotDirectory(t *testing.T) {
	p := &ImmichProvider{albums: map[string]string{}, albumIDs: map[string]string{}}
	if _, err := p.GetDirectoryListing(context.Background(), "files", "/All Assets/asset-1"); err == nil {
		t.Error("GetDirectoryListing() unexpectedly accepted an All Assets asset path")
	}
	if _, err := p.GetDirectoryListing(context.Background(), "files", "/Albums/album-1/asset-1"); err == nil {
		t.Error("GetDirectoryListing() unexpectedly accepted an album asset path")
	}
}

func TestImmichAlbumListingPreservesAssetMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search/metadata" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			return
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q, want test-key", got)
		}
		_, _ = w.Write([]byte(`{"assets":{"items":[{"id":"asset-id","originalFileName":"photo.jpg","originalMimeType":"image/jpeg","exifInfo":{"fileSizeInByte":12345}}]}}`))
	}))
	defer server.Close()
	p := &ImmichProvider{
		BaseURL: server.URL + "/api", APIKey: "test-key", HTTPClient: server.Client(),
		albums: map[string]string{"album-id": "Holiday"}, albumIDs: map[string]string{"Holiday": "album-id"},
	}

	items, err := p.GetDirectoryListing(context.Background(), "files", "/Albums/album-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := items[0]
	if item.Path != "/Albums/album-id/asset-id" || item.Name != "photo.jpg" || item.Size != 12345 {
		t.Errorf("item = %#v", item)
	}
	if item.Metadata.CustomProps["immich_filename"] != "photo.jpg" {
		t.Errorf("immich_filename = %q", item.Metadata.CustomProps["immich_filename"])
	}
}
