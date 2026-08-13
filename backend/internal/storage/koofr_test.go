package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestKoofrProvider(server *httptest.Server) *KoofrProvider {
	p, err := NewKoofrProvider("user@example.com", "application-password")
	if err != nil {
		panic(err)
	}
	p.BaseURL = server.URL
	p.HTTPClient = &http.Client{Transport: server.Client().Transport, CheckRedirect: rejectEgressRedirect}
	return p
}

func TestKoofrProviderConnectSelectsPrimaryMountAndAuthenticates(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "user@example.com" || password != "application-password" {
			t.Fatal("missing or invalid Basic authentication")
		}
		if r.URL.Path != "/api/v2/mounts" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"other","isPrimary":false},{"id":"primary","isPrimary":true}]`))
	}))
	defer server.Close()

	p := newTestKoofrProvider(server)
	ok, err := p.Connect(context.Background())
	if err != nil || !ok {
		t.Fatalf("Connect() = %t, %v", ok, err)
	}
	if got := p.connectedMountID(); got != "primary" {
		t.Fatalf("primary mount = %q, want primary", got)
	}
}

func TestKoofrProviderConnectErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: ErrAuth},
		{name: "forbidden", status: http.StatusForbidden, want: ErrAuth},
		{name: "no primary mount", status: http.StatusOK, body: `[{"id":"other","isPrimary":false}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			ok, err := newTestKoofrProvider(server).Connect(context.Background())
			if ok || err == nil {
				t.Fatalf("Connect() = %t, %v, want error", ok, err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("Connect() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestKoofrProviderListingAndHash(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/mounts":
			_, _ = w.Write([]byte(`[{"id":"primary","isPrimary":true}]`))
		case "/api/v2/mounts/primary/files/list":
			if got := r.URL.Query().Get("path"); got != "/folder name" {
				t.Fatalf("list path = %q", got)
			}
			_, _ = w.Write([]byte(`{"files":[{"name":"a file.txt","type":"file","size":7,"modified":1710000000123,"hash":"ABCDEF"},{"name":"folder","type":"dir","modified":1710000000000}]}`))
		case "/api/v2/mounts/primary/files/info":
			if got := r.URL.Query().Get("path"); got != "/folder name/a file.txt" {
				t.Fatalf("info path = %q", got)
			}
			_, _ = w.Write([]byte(`{"name":"a file.txt","type":"file","size":7,"modified":1710000000123,"hash":"ABCDEF"}`))
		default:
			t.Fatalf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	p := newTestKoofrProvider(server)
	if ok, err := p.Connect(context.Background()); !ok || err != nil {
		t.Fatal(err)
	}

	resources, err := p.GetDirectoryListing(context.Background(), "files", "/folder name")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 || resources[0].Path != "/folder name/a file.txt" || resources[0].Hash != "MD5:abcdef" {
		t.Fatalf("listing = %+v", resources)
	}
	if resources[0].LastModified != time.Unix(1710000000, 123000000).UTC() || resources[1].Hash != "" {
		t.Fatalf("unexpected metadata: %+v", resources)
	}
	hash, err := p.GetFileHash(context.Background(), "files", "/folder name/a file.txt")
	if err != nil || hash != "MD5:abcdef" {
		t.Fatalf("GetFileHash() = %q, %v", hash, err)
	}
}

func TestKoofrProviderStreamsUploadAndDownload(t *testing.T) {
	data := []byte("streamed without buffering")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/mounts":
			_, _ = w.Write([]byte(`[{"id":"primary","isPrimary":true}]`))
		case "/content/api/v2/mounts/primary/files/put":
			if got := r.URL.Query(); got.Get("path") != "/folder" || got.Get("filename") != "file name.txt" || got.Get("overwrite") != "true" || got.Get("autorename") != "false" || !got.Has("overwriteIgnoreNonexisting") {
				t.Errorf("upload query = %s", got.Encode())
			}
			if r.ContentLength != int64(len(data))+int64(len("--clumoove-koofr-upload\r\nContent-Disposition: form-data; name=\"file\"; filename=\"file\"\r\nContent-Type: application/octet-stream\r\n\r\n")+len("\r\n--clumoove-koofr-upload--\r\n")) {
				t.Errorf("content length = %d", r.ContentLength)
			}
			if len(r.TransferEncoding) != 0 {
				t.Errorf("transfer encoding = %v, want no chunked encoding", r.TransferEncoding)
			}
			reader, err := r.MultipartReader()
			if err != nil {
				t.Errorf("MultipartReader() error = %v", err)
				return
			}
			part, err := reader.NextPart()
			if err != nil {
				t.Errorf("NextPart() error = %v", err)
				return
			}
			body, err := io.ReadAll(part)
			if err != nil || !bytes.Equal(body, data) || part.FormName() != "file" {
				t.Errorf("upload part = %q, form %q, err = %v", body, part.FormName(), err)
			}
			_, _ = w.Write([]byte(`{"name":"file name.txt"}`))
		case "/content/api/v2/mounts/primary/files/get":
			if got := r.URL.Query().Get("path"); got != "/folder/file name.txt" {
				t.Fatalf("download path = %q", got)
			}
			_, _ = w.Write(data)
		default:
			t.Fatalf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	p := newTestKoofrProvider(server)
	if ok, err := p.Connect(context.Background()); !ok || err != nil {
		t.Fatal(err)
	}
	progress := make(chan int64, 1)
	if err := p.StreamUploadChunked(context.Background(), "files", "/folder/file name.txt", bytes.NewReader(data), int64(len(data)), progress); err != nil {
		t.Fatal(err)
	}
	if got := <-progress; got != int64(len(data)) {
		t.Fatalf("progress = %d", got)
	}
	if err := p.StreamUpload(context.Background(), "files", "/folder/file name.txt", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	reader, err := p.StreamDownload(context.Background(), "files", "/folder/file name.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("download = %q, %v", got, err)
	}
}

func TestKoofrProviderRequiresConnection(t *testing.T) {
	p, err := NewKoofrProvider("user", "password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.GetDirectoryListing(context.Background(), "files", "/"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("GetDirectoryListing() error = %v", err)
	}
}

func TestKoofrProviderRejectsTraversalAndClassifiesNotFound(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/mounts" {
			_, _ = w.Write([]byte(`[{"id":"primary","isPrimary":true}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	p := newTestKoofrProvider(server)
	if ok, err := p.Connect(context.Background()); !ok || err != nil {
		t.Fatal(err)
	}
	if _, err := p.InspectResource(context.Background(), "files", "/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("InspectResource() error = %v", err)
	}
	if _, err := p.InspectResource(context.Background(), "files", "/safe/../escape"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("traversal error = %v", err)
	}
	if _, err := p.GetFileHash(context.Background(), "calendars", "/file"); !errors.Is(err, ErrUnsupportedResourceType) {
		t.Fatalf("resource type error = %v", err)
	}
}

func TestKoofrProviderRejectsCancelledContextAndUnavailableHash(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/mounts":
			_, _ = w.Write([]byte(`[{"id":"primary","isPrimary":true}]`))
		case "/api/v2/mounts/primary/files/info":
			_, _ = w.Write([]byte(`{"name":"folder","type":"dir","size":0,"modified":0}`))
		default:
			t.Fatalf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	p := newTestKoofrProvider(server)
	if ok, err := p.Connect(context.Background()); !ok || err != nil {
		t.Fatal(err)
	}

	if _, err := p.GetFileHash(context.Background(), "files", "/folder"); !errors.Is(err, ErrChecksumNotAvailable) {
		t.Fatalf("directory hash error = %v", err)
	}
	if exists, size, err := p.FileExists(context.Background(), "files", "/folder"); err != nil || !exists || size != 0 {
		t.Fatalf("FileExists() = %t, %d, %v, want true, 0, nil", exists, size, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.InspectResource(ctx, "files", "/cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request error = %v", err)
	}
}

func TestKoofrProviderCreatesParentsAndMovesFiles(t *testing.T) {
	var folders []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/mounts":
			_, _ = w.Write([]byte(`[{"id":"primary","isPrimary":true}]`))
		case "/api/v2/mounts/primary/files/folder":
			var request struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			folders = append(folders, r.URL.Query().Get("path")+"/"+request.Name)
			w.WriteHeader(http.StatusCreated)
		case "/api/v2/mounts/primary/files/info":
			w.WriteHeader(http.StatusNotFound)
		case "/api/v2/mounts/primary/files/move":
			var request koofrMoveRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if r.URL.Query().Get("path") != "/old file" || request.ToMountID != "primary" || request.ToPath != "/new folder/new file" {
				t.Fatalf("move = path %q body %+v", r.URL.Query().Get("path"), request)
			}
		default:
			t.Fatalf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	p := newTestKoofrProvider(server)
	if ok, err := p.Connect(context.Background()); !ok || err != nil {
		t.Fatal(err)
	}
	if err := p.CreateParentDirectories(context.Background(), "files", "/one/two/file"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(folders, ",") != "//one,/one/two" {
		t.Fatalf("folders = %v", folders)
	}
	if err := p.RenameFile(context.Background(), "files", "/old file", "/new folder/new file"); err != nil {
		t.Fatal(err)
	}
	if err := p.CreateParentDirectories(context.Background(), "files", "/one/two/second-file"); err != nil {
		t.Fatal(err)
	}
	if len(folders) != 2 {
		t.Fatalf("cached parent creation made %d folder requests, want 2", len(folders))
	}
}

func TestKoofrProviderCreateFolderAcceptsExistingDirectory(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/mounts":
			_, _ = w.Write([]byte(`[{"id":"primary","isPrimary":true}]`))
		case "/api/v2/mounts/primary/files/info":
			if r.URL.Query().Get("path") == "/existing" {
				_, _ = w.Write([]byte(`{"name":"existing","type":"dir"}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	p := newTestKoofrProvider(server)
	if ok, err := p.Connect(context.Background()); !ok || err != nil {
		t.Fatal(err)
	}
	if err := p.CreateDirectory(context.Background(), "files", "/existing"); err != nil {
		t.Fatalf("CreateDirectory() = %v", err)
	}
}

func TestKoofrProviderCreateFolderAcceptsConflictAfterRace(t *testing.T) {
	infoRequests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/mounts":
			_, _ = w.Write([]byte(`[{"id":"primary","isPrimary":true}]`))
		case "/api/v2/mounts/primary/files/info":
			infoRequests++
			if infoRequests == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"name":"raced","type":"dir"}`))
		case "/api/v2/mounts/primary/files/folder":
			w.WriteHeader(http.StatusConflict)
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	p := newTestKoofrProvider(server)
	if ok, err := p.Connect(context.Background()); !ok || err != nil {
		t.Fatal(err)
	}
	if err := p.CreateDirectory(context.Background(), "files", "/raced"); err != nil {
		t.Fatalf("CreateDirectory() = %v", err)
	}
}

func TestKoofrProviderPreservesBackslashesAndUploadMetadata(t *testing.T) {
	modified := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/mounts":
			_, _ = w.Write([]byte(`[{"id":"primary","isPrimary":true}]`))
		case "/api/v2/mounts/primary/files/info":
			if got := r.URL.Query().Get("path"); got != `/folder/a\b.txt` {
				t.Errorf("path = %q", got)
			}
			_, _ = w.Write([]byte(`{"name":"a\\b.txt","type":"file"}`))
		case "/content/api/v2/mounts/primary/files/put":
			if got := r.URL.Query().Get("modified"); got != "1786622400000" {
				t.Errorf("modified = %q", got)
			}
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(`{"name":"file.txt","type":"file"}`))
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	p := newTestKoofrProvider(server)
	if ok, err := p.Connect(context.Background()); !ok || err != nil {
		t.Fatal(err)
	}
	if _, err := p.InspectResource(context.Background(), "files", `/folder/a\b.txt`); err != nil {
		t.Fatal(err)
	}
	ctx := WithTransferMetadata(context.Background(), FileMetadata{ModifiedTime: modified})
	if err := p.StreamUpload(ctx, "files", "/file.txt", strings.NewReader("x"), 1); err != nil {
		t.Fatal(err)
	}
}

func TestKoofrProviderRetriesRateLimitedReads(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/mounts":
			requests++
			if requests == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write([]byte(`[{"id":"primary","isPrimary":true}]`))
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	p := newTestKoofrProvider(server)
	if ok, err := p.Connect(context.Background()); !ok || err != nil {
		t.Fatalf("Connect() = %t, %v", ok, err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestNewKoofrProviderUsesPinnedStreamingClient(t *testing.T) {
	p, err := NewKoofrProvider("user", "password")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.HTTPClient.Timeout != 0 || p.HTTPClient.CheckRedirect == nil {
		t.Fatalf("client timeout = %s, redirect policy configured = %t", p.HTTPClient.Timeout, p.HTTPClient.CheckRedirect != nil)
	}
	if err := p.HTTPClient.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v", err)
	}
	logging, ok := p.HTTPClient.Transport.(*loggingTransport)
	if !ok {
		t.Fatalf("transport = %T, want *loggingTransport", p.HTTPClient.Transport)
	}
	transport, ok := logging.base.(*http.Transport)
	if !ok || transport.DialContext == nil || transport.ResponseHeaderTimeout != 5*time.Minute {
		t.Fatalf("transport configuration = %#v", logging.base)
	}
}

func TestKoofrProviderRejectsRedirects(t *testing.T) {
	redirectTarget := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("redirect target must not receive a request")
	}))
	defer redirectTarget.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusFound)
	}))
	defer server.Close()

	p := newTestKoofrProvider(server)
	_, err := p.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("Connect() redirect error = %v", err)
	}
}
