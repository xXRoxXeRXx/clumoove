package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// mockGooglePhotos spins up an httptest server that mimics the Google Photos
// Library API endpoints used by GooglePhotosProvider. It returns canned albums
// and media items, accepts uploads, and reports a content length on HEAD so the
// size-lookup path can be exercised. The handler records incoming requests.
type mockGooglePhotos struct {
	server  *httptest.Server
	uploads []string

	mu          sync.Mutex
	createdAlbs map[string]string // title -> id, to mimic persistent albums
	albumSeq    int
}

func newMockGooglePhotos(t *testing.T) *mockGooglePhotos {
	m := &mockGooglePhotos{createdAlbs: map[string]string{}}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/albums"):
			switch r.Method {
			case http.MethodGet:
				m.mu.Lock()
				albums := make([]googlePhotosAlbum, 0, len(m.createdAlbs)+2)
				// seed two pre-existing albums
				albums = append(albums,
					googlePhotosAlbum{ID: "album1", Title: "Holidays"},
					googlePhotosAlbum{ID: "album2", Title: "Family"},
				)
				for title, id := range m.createdAlbs {
					albums = append(albums, googlePhotosAlbum{ID: id, Title: title})
				}
				m.mu.Unlock()
				json.NewEncoder(w).Encode(googlePhotosAlbumsResponse{Albums: albums})
			case http.MethodPost:
				var body struct {
					Album googlePhotosAlbum `json:"album"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				m.mu.Lock()
				m.albumSeq++
				id := body.Album.Title // deterministic id for the mock
				m.createdAlbs[body.Album.Title] = id
				m.mu.Unlock()
				json.NewEncoder(w).Encode(googlePhotosAlbum{ID: id, Title: body.Album.Title})
			}
		case strings.HasSuffix(r.URL.Path, "/mediaItems:search"):
			json.NewEncoder(w).Encode(googlePhotosMediaItemsResponse{
				MediaItems: []googlePhotosMediaItem{
					{
						ID:       "media1",
						Filename: "photo.jpg",
						MimeType: "image/jpeg",
						MediaMetadata: struct {
							CreationTime string                 `json:"creationTime"`
							Width        string                 `json:"width"`
							Height       string                 `json:"height"`
							Photo        map[string]interface{} `json:"photo"`
							Video        map[string]interface{} `json:"video"`
						}{CreationTime: "2023-01-01T10:00:00Z"},
					},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/mediaItems:upload"):
			m.mu.Lock()
			m.uploads = append(m.uploads, "uploaded")
			m.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]string{"uploadToken": "tok123"})
		case strings.HasSuffix(r.URL.Path, "/mediaItems:batchCreate"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"newMediaItemResults": []map[string]interface{}{
					{"status": map[string]interface{}{"message": "OK"}},
				},
			})
		case r.URL.Path == "/mediaItems":
			// List endpoint (used by Connect probe).
			json.NewEncoder(w).Encode(googlePhotosMediaItemsResponse{})
		case strings.HasPrefix(r.URL.Path, "/mediaItems/"):
			// GET (or HEAD) for a single media item: returns the item with a
			// baseUrl; HEAD is used by fetchMediaSize to read Content-Length.
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Length", "12345")
				w.WriteHeader(http.StatusOK)
				return
			}
			json.NewEncoder(w).Encode(googlePhotosMediaItem{
				ID:       "media1",
				Filename: "photo.jpg",
				MimeType: "image/jpeg",
				BaseURL:  m.server.URL + "/base/media1",
			})
		case r.URL.Path == "/base/media1" && r.Method == http.MethodHead:
			// HEAD on the download URL reports the real content length.
			w.Header().Set("Content-Length", "12345")
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/base/media1":
			w.Header().Set("Content-Length", "12345")
			w.Write([]byte("binarydata-bytes"))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

func newTestGooglePhotos(t *testing.T) (*GooglePhotosProvider, *mockGooglePhotos) {
	m := newMockGooglePhotos(t)
	p := &GooglePhotosProvider{
		AccessToken:    "test-token",
		HTTPClient:     m.server.Client(),
		BaseURL:        m.server.URL,
		albumTitleToID: make(map[string]string),
		albumIDToTitle: make(map[string]string),
	}
	return p, m
}

func TestGooglePhotosConnect(t *testing.T) {
	p, _ := newTestGooglePhotos(t)
	ok, err := p.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	if !ok {
		t.Errorf("Connect returned false, want true")
	}
}

func TestGooglePhotosListAlbums(t *testing.T) {
	p, _ := newTestGooglePhotos(t)
	resources, err := p.GetDirectoryListing(context.Background(), "files", "/")
	if err != nil {
		t.Fatalf("GetDirectoryListing error: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("expected 2 albums, got %d", len(resources))
	}
	if !resources[0].IsDir {
		t.Errorf("album should be a directory")
	}
	if resources[0].Path != "/album1" {
		t.Errorf("unexpected album path: %s", resources[0].Path)
	}
}

func TestGooglePhotosListAlbumMedia(t *testing.T) {
	p, _ := newTestGooglePhotos(t)
	resources, err := p.GetDirectoryListing(context.Background(), "files", "/album1")
	if err != nil {
		t.Fatalf("GetDirectoryListing error: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected 1 media item, got %d", len(resources))
	}
	if resources[0].IsDir {
		t.Errorf("media item should not be a directory")
	}
	if resources[0].Path != "/album1/media1" {
		t.Errorf("unexpected media path: %s", resources[0].Path)
	}
}

func TestGooglePhotosInspectPopulatesSize(t *testing.T) {
	p, _ := newTestGooglePhotos(t)
	res, err := p.InspectResource(context.Background(), "files", "/album1/media1")
	if err != nil {
		t.Fatalf("InspectResource error: %v", err)
	}
	if res.Size != 12345 {
		t.Errorf("expected size 12345 from HEAD on baseUrl, got %d", res.Size)
	}
}

func TestGooglePhotosUnsupportedResourceType(t *testing.T) {
	p, _ := newTestGooglePhotos(t)
	resources, err := p.GetDirectoryListing(context.Background(), "calendars", "/")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if resources != nil {
		t.Errorf("expected nil for unsupported resource type, got %v", resources)
	}
}

func TestGooglePhotosUploadStripsTmpSuffix(t *testing.T) {
	p, m := newTestGooglePhotos(t)
	// The processor may append ".tmp" for its atomic-rename pattern; Photos
	// has no rename, so the album segment and filename must be cleaned.
	err := p.StreamUploadChunked(context.Background(), "files", "/MyAlbum/photo.jpg.tmp", strings.NewReader("binarydata"), 10, nil)
	if err != nil {
		t.Fatalf("StreamUploadChunked error: %v", err)
	}
	if len(m.uploads) != 1 {
		t.Errorf("expected 1 upload, got %d", len(m.uploads))
	}
}

func TestGooglePhotosAlbumDedupByTitle(t *testing.T) {
	p, m := newTestGooglePhotos(t)
	// First upload into "Holidays" creates the album.
	if err := p.StreamUploadChunked(context.Background(), "files", "/Holidays/photo.jpg", strings.NewReader("binarydata"), 10, nil); err != nil {
		t.Fatalf("first upload error: %v", err)
	}
	// Second upload into the same album title must reuse it, not create a new one.
	if err := p.StreamUploadChunked(context.Background(), "files", "/Holidays/photo2.jpg", strings.NewReader("binarydata"), 10, nil); err != nil {
		t.Fatalf("second upload error: %v", err)
	}
	m.mu.Lock()
	count := 0
	for title := range m.createdAlbs {
		if title == "Holidays" {
			count++
		}
	}
	m.mu.Unlock()
	if count != 1 {
		t.Errorf("expected 1 'Holidays' album to be created (deduped), got %d", count)
	}
}

func TestGooglePhotosPhotosToPhotosResolvesByID(t *testing.T) {
	p, m := newTestGooglePhotos(t)
	// A Photos->Photos upload carries the source album id ("album1"). It should
	// map onto the already-existing target album instead of creating a new one
	// literally named "album1".
	err := p.StreamUploadChunked(context.Background(), "files", "/album1/photo.jpg", strings.NewReader("binarydata"), 10, nil)
	if err != nil {
		t.Fatalf("StreamUploadChunked error: %v", err)
	}
	m.mu.Lock()
	_, createdByID := m.createdAlbs["album1"]
	m.mu.Unlock()
	if createdByID {
		t.Errorf("Photos->Photos upload should reuse the existing album id, not create a new 'album1' album")
	}
}

func TestGooglePhotosCreateDirectory(t *testing.T) {
	p, _ := newTestGooglePhotos(t)
	err := p.CreateDirectory(context.Background(), "files", "/NewAlbum")
	if err != nil {
		t.Fatalf("CreateDirectory error: %v", err)
	}
}

func TestGooglePhotosNotSupported(t *testing.T) {
	p, _ := newTestGooglePhotos(t)
	if err := p.DeleteFile(context.Background(), "files", "/a/b"); err == nil {
		t.Errorf("DeleteFile should return not-supported error")
	}
	if err := p.RenameFile(context.Background(), "files", "/a", "/b"); err == nil {
		t.Errorf("RenameFile should return not-supported error")
	}
	if h, err := p.GetFileHash(context.Background(), "files", "/a/b"); err != nil || h != "" {
		t.Errorf("GetFileHash should return empty with no error, got %q %v", h, err)
	}
}
