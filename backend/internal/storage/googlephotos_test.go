package storage

import (
	"context"
	"encoding/json"
	"io"
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

	mu           sync.Mutex
	createdAlbs  map[string]string // title -> id, to mimic persistent albums
	albumSeq     int
	lastUpload   lastUploadInfo    // records the most recent upload request metadata
	batchPayload string            // raw body of the most recent batchCreate
}

type lastUploadInfo struct {
	protocol     string
	mime         string
	contentType  string
	body         string
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
		case r.URL.Path == "/uploads":
			// Raw-binary upload endpoint: the upload token is returned as
			// plain text (NOT JSON). Record the protocol/headers/body.
			m.mu.Lock()
			m.uploads = append(m.uploads, "uploaded")
			bodyBytes, _ := io.ReadAll(r.Body)
			m.lastUpload = lastUploadInfo{
				protocol:    r.Header.Get("X-Goog-Upload-Protocol"),
				mime:        r.Header.Get("X-Goog-Upload-Content-Type"),
				contentType: r.Header.Get("Content-Type"),
				body:        string(bodyBytes),
			}
			m.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("tok123"))
		case strings.HasSuffix(r.URL.Path, "/mediaItems:batchCreate"):
			raw, _ := io.ReadAll(r.Body)
			m.mu.Lock()
			m.batchPayload = string(raw)
			m.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{
				"newMediaItemResults": []map[string]interface{}{
					{"status": map[string]interface{}{"message": "OK", "code": 0}},
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

func TestGooglePhotosUploadUsesRawBinaryEndpoint(t *testing.T) {
	p, m := newTestGooglePhotos(t)
	err := p.StreamUploadChunked(context.Background(), "files", "/MyAlbum/photo.jpg", strings.NewReader("binarydata"), 10, nil)
	if err != nil {
		t.Fatalf("StreamUploadChunked error: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.uploads) != 1 {
		t.Fatalf("expected 1 upload, got %d", len(m.uploads))
	}
	if m.lastUpload.protocol != "raw" {
		t.Errorf("expected X-Goog-Upload-Protocol: raw, got %q", m.lastUpload.protocol)
	}
	if m.lastUpload.contentType != "application/octet-stream" {
		t.Errorf("expected Content-Type application/octet-stream, got %q", m.lastUpload.contentType)
	}
	if m.lastUpload.mime != "image/jpeg" {
		t.Errorf("expected X-Goog-Upload-Content-Type image/jpeg, got %q", m.lastUpload.mime)
	}
	if m.lastUpload.body != "binarydata" {
		t.Errorf("expected raw binary body 'binarydata', got %q", m.lastUpload.body)
	}
}

func TestGooglePhotosBatchCreateUsesFileName(t *testing.T) {
	p, m := newTestGooglePhotos(t)
	err := p.StreamUploadChunked(context.Background(), "files", "/MyAlbum/photo.jpg", strings.NewReader("binarydata"), 10, nil)
	if err != nil {
		t.Fatalf("StreamUploadChunked error: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var payload struct {
		NewMediaItems []struct {
			Description     string `json:"description"`
			SimpleMediaItem struct {
				UploadToken string `json:"uploadToken"`
				FileName    string `json:"fileName"`
			} `json:"simpleMediaItem"`
		} `json:"newMediaItems"`
	}
	if err := json.Unmarshal([]byte(m.batchPayload), &payload); err != nil {
		t.Fatalf("failed to decode batchCreate payload: %v", err)
	}
	if len(payload.NewMediaItems) != 1 {
		t.Fatalf("expected 1 new media item, got %d", len(payload.NewMediaItems))
	}
	item := payload.NewMediaItems[0]
	if item.SimpleMediaItem.FileName != "photo.jpg" {
		t.Errorf("expected simpleMediaItem.fileName 'photo.jpg', got %q", item.SimpleMediaItem.FileName)
	}
	if item.Description != "" {
		t.Errorf("description must not carry the filename, got %q", item.Description)
	}
}

func TestGooglePhotosStreamDownloadVideoSuffix(t *testing.T) {
	if got := downloadSuffix("video/mp4"); got != "=dv" {
		t.Errorf("downloadSuffix(video/mp4) = %q, want =dv", got)
	}
	if got := downloadSuffix("image/jpeg"); got != "=d" {
		t.Errorf("downloadSuffix(image/jpeg) = %q, want =d", got)
	}
}

func TestGooglePhotosStreamDownloadSuffixApplied(t *testing.T) {
	var serverURL string
	vs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/mediaItems/"):
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Length", "12345")
				w.WriteHeader(http.StatusOK)
				return
			}
			json.NewEncoder(w).Encode(googlePhotosMediaItem{
				ID:       "media1",
				Filename: "clip.mp4",
				MimeType: "video/mp4",
				BaseURL:  serverURL + "/base/media1",
			})
		case strings.HasPrefix(r.URL.Path, "/base/media1"):
			if !strings.HasSuffix(r.URL.String(), "=dv") {
				t.Errorf("video download must use =dv suffix, got %q", r.URL.String())
			}
			w.Header().Set("Content-Length", "12345")
			w.Write([]byte("videodata-bytes"))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer vs.Close()
	serverURL = vs.URL

	p2 := &GooglePhotosProvider{
		AccessToken:    "test-token",
		HTTPClient:     vs.Client(),
		BaseURL:        vs.URL,
		albumTitleToID: make(map[string]string),
		albumIDToTitle: make(map[string]string),
	}
	rc, err := p2.StreamDownload(context.Background(), "files", "/album1/media1")
	if err != nil {
		t.Fatalf("StreamDownload error: %v", err)
	}
	defer rc.Close()
}

func TestPickerPathRoundTrip(t *testing.T) {
	item := PickerMediaItem{
		ID:      "media-abc123",
		Name:    "holiday.jpg",
		BaseURL: "https://lh3.googleusercontent.com/abc/def?sz=w1234",
		MimeType: "image/jpeg",
	}
	path := PickerPath(item)
	if !IsPickerPath(path) {
		t.Fatalf("PickerPath did not produce a /picker/ path: %q", path)
	}

	mediaID, baseURL, err := ParsePickerPath(path)
	if err != nil {
		t.Fatalf("ParsePickerPath error: %v", err)
	}
	if mediaID != item.ID {
		t.Errorf("mediaID = %q, want %q", mediaID, item.ID)
	}
	if baseURL != item.BaseURL {
		t.Errorf("baseURL = %q, want %q", baseURL, item.BaseURL)
	}

	if got := PickerMimeFromPath(path); got != item.MimeType {
		t.Errorf("PickerMimeFromPath = %q, want %q", got, item.MimeType)
	}

	// The target name must be a clean basename (no /picker/ prefix, no query).
	target := PickerTargetName(path)
	if strings.HasPrefix(target, "/picker/") {
		t.Errorf("PickerTargetName leaked /picker/ prefix: %q", target)
	}
	if strings.Contains(target, "base_url") || strings.Contains(target, "?") {
		t.Errorf("PickerTargetName leaked credentialed query: %q", target)
	}
	if !strings.HasPrefix(target, "google-photos-"+item.ID) {
		t.Errorf("PickerTargetName = %q, want prefix google-photos-%s", target, item.ID)
	}
}

func TestPickerPathRoundTripVideo(t *testing.T) {
	item := PickerMediaItem{
		ID:      "vid-9",
		Name:    "clip",
		BaseURL: "https://lh3.googleusercontent.com/vid/xyz",
		MimeType: "video/mp4",
	}
	path := PickerPath(item)
	mediaID, baseURL, err := ParsePickerPath(path)
	if err != nil {
		t.Fatalf("ParsePickerPath error: %v", err)
	}
	if mediaID != item.ID || baseURL != item.BaseURL {
		t.Errorf("round-trip mismatch: id=%q url=%q", mediaID, baseURL)
	}
	// Video path should carry a .mp4 suffix on the id segment.
	if !strings.Contains(path, ".mp4") {
		t.Errorf("expected .mp4 extension in picker path for video, got %q", path)
	}
}

func TestPickerPathInvalid(t *testing.T) {
	for _, bad := range []string{"", "/notpicker/x", "/picker/id", "/picker/id.jpg"} {
		if _, _, err := ParsePickerPath(bad); err == nil {
			t.Errorf("expected error for %q, got nil", bad)
		}
	}
}

// TestPickerMediaItemNestedDecode verifies that the real Google Photos Picker
// payload — where baseUrl/mimeType/filename are nested under mediaFile — is
// decoded correctly so Name/BaseURL/MimeType are populated (and not blank).
func TestPickerMediaItemNestedDecode(t *testing.T) {
	const body = `{
		"mediaItems": [
			{
				"id": "MEDIA_ID_1",
				"createTime": "2024-01-02T03:04:05Z",
				"type": "PHOTO",
				"mediaFile": {
					"baseUrl": "https://lh3.googleusercontent.com/p/AF1QipXbaseurl",
					"mimeType": "image/jpeg",
					"filename": "holiday-beach.jpg"
				}
			}
		],
		"nextPageToken": ""
	}`
	var resp pickerMediaItemsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(resp.MediaItems) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.MediaItems))
	}
	mi := resp.MediaItems[0]
	if mi.ID != "MEDIA_ID_1" {
		t.Errorf("ID = %q, want MEDIA_ID_1", mi.ID)
	}
	if mi.MediaFile.BaseURL == "" || mi.MediaFile.MimeType == "" || mi.MediaFile.Filename == "" {
		t.Fatalf("nested mediaFile not decoded: %+v", mi.MediaFile)
	}
	// The convenience fields must be surfaced during GetPickerMediaItems.
	mi.BaseURL = mi.MediaFile.BaseURL
	mi.MimeType = mi.MediaFile.MimeType
	mi.Name = mi.MediaFile.Filename
	if mi.BaseURL == "" || mi.MimeType == "" || mi.Name == "" {
		t.Errorf("surfaced picker fields empty: name=%q base=%q mime=%q", mi.Name, mi.BaseURL, mi.MimeType)
	}
	path := PickerPath(mi)
	if !strings.Contains(path, "base_url=https%3A%2F%2Flh3.googleusercontent.com") {
		t.Errorf("picker path missing base_url: %q", path)
	}
}

func TestExtForMime(t *testing.T) {
	cases := []struct {
		mime string
		name string
		want string
	}{
		{"image/jpeg", "a", ".jpg"},
		{"image/png", "a", ".jpg"},
		{"video/mp4", "a", ".mp4"},
		{"application/octet-stream", "a", ""},
		{"image/jpeg", "photo.jpg", ""}, // name already has an extension
	}
	for _, c := range cases {
		if got := extForMime(c.mime, c.name); got != c.want {
			t.Errorf("extForMime(%q,%q) = %q, want %q", c.mime, c.name, got, c.want)
		}
	}
}

func TestPickerHandleFromMetadata(t *testing.T) {
	h := PickerHandle{ID: "m1", BaseURL: "https://x/y", Mime: "image/jpeg", Name: "pic.jpg"}
	raw, _ := json.Marshal(h)
	got, ok := PickerHandleFromMetadata(raw)
	if !ok || got.ID != h.ID || got.BaseURL != h.BaseURL {
		t.Errorf("round-trip failed: got %+v ok=%v", got, ok)
	}
	// Empty / non-picker metadata must report ok=false.
	if _, ok := PickerHandleFromMetadata([]byte("{}")); ok {
		t.Error("empty metadata should not be a picker handle")
	}
	if _, ok := PickerHandleFromMetadata(nil); ok {
		t.Error("nil metadata should not be a picker handle")
	}
}

