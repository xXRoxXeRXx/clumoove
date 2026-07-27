package storage

import (
	"context"
	"errors"
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

func TestImmichJSONRequestsAndAlbumCache(t *testing.T) {
	var albumLists int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/search/metadata":
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("search Content-Type = %q", got)
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
