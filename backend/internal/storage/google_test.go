package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

func TestNewGoogleProviderRequiresToken(t *testing.T) {
	_, err := NewGoogleProvider(context.Background(), "")
	if err == nil {
		t.Error("expected error when token is empty, got nil")
	}
}

func TestNewGoogleProviderValidToken(t *testing.T) {
	p, err := NewGoogleProvider(context.Background(), "mock-oauth-token")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil GoogleProvider")
	}
	if !p.SupportsAtomicRename() {
		t.Error("expected SupportsAtomicRename() = true")
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestIsGoogleAuthError(t *testing.T) {
	gAuthErr := &googleapi.Error{Code: http.StatusUnauthorized, Message: "Invalid Credentials"}
	if !isGoogleAuthError(gAuthErr) {
		t.Errorf("expected isGoogleAuthError(gAuthErr) = true")
	}

	gForbiddenErr := &googleapi.Error{Code: http.StatusForbidden, Message: "Access Denied"}
	if !isGoogleAuthError(gForbiddenErr) {
		t.Errorf("expected isGoogleAuthError(gForbiddenErr) = true")
	}

	rawAuthErr := errors.New("oauth2: cannot fetch token: 401 Unauthorized")
	if !isGoogleAuthError(rawAuthErr) {
		t.Errorf("expected isGoogleAuthError(rawAuthErr) = true")
	}

	gNotFoundErr := &googleapi.Error{Code: http.StatusNotFound, Message: "Not Found"}
	if isGoogleAuthError(gNotFoundErr) {
		t.Errorf("expected isGoogleAuthError(gNotFoundErr) = false")
	}
}

func TestGoogleProviderNonFilesFileExists(t *testing.T) {
	p, err := NewGoogleProvider(context.Background(), "mock-token")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	ctx := context.Background()
	_, _, err = p.FileExists(ctx, "invalid_type", "/test")
	if err == nil {
		t.Error("FileExists: expected error for invalid resourceType, got nil")
	}
}

func TestGoogleDocsExtension(t *testing.T) {
	mime, ext := googleDocsExtension("application/vnd.google-apps.document")
	if ext != ".docx" || mime == "" {
		t.Errorf("expected .docx, got mime=%s ext=%s", mime, ext)
	}
	mime, ext = googleDocsExtension("application/vnd.google-apps.spreadsheet")
	if ext != ".xlsx" || mime == "" {
		t.Errorf("expected .xlsx, got mime=%s ext=%s", mime, ext)
	}
	mime, ext = googleDocsExtension("unknown/mime")
	if ext != "" || mime != "" {
		t.Errorf("expected empty for unknown mime, got mime=%s ext=%s", mime, ext)
	}
}

func TestGoogleProviderUploadCreatesMissingParentDirectories(t *testing.T) {
	folders := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/files":
			q := r.URL.Query().Get("q")
			id := folders[q]
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"files": func() []map[string]string {
				if id == "" {
					return []map[string]string{}
				}
				return []map[string]string{{"id": id}}
			}()})
		case r.Method == http.MethodPost && (r.URL.Path == "/files" || r.URL.Path == "/upload/files" || r.URL.Path == "/upload/drive/v3/files"):
			// Directory creates are multipart requests. The final upload is also
			// accepted here; its exact body is not relevant to this regression.
			if r.URL.Query().Get("uploadType") == "" {
				var body struct {
					Name    string   `json:"name"`
					Parents []string `json:"parents"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("decode directory create: %v", err)
				}
				parent := "root"
				if len(body.Parents) > 0 {
					parent = body.Parents[0]
				}
				id := body.Name + "-id"
				folders[driveFolderQuery(parent, body.Name)] = id
				_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "file-id"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	p, err := NewGoogleProvider(context.Background(), "mock-token")
	if err != nil {
		t.Fatalf("NewGoogleProvider: %v", err)
	}
	defer p.Close()
	p.driveService, err = drive.NewService(context.Background(), option.WithHTTPClient(p.httpClient), option.WithEndpoint(server.URL+"/"))
	if err != nil {
		t.Fatalf("create test Drive service: %v", err)
	}

	if err := p.StreamUpload(context.Background(), "files", "/parent/child/file.txt", bytes.NewBufferString("body"), 4); err != nil {
		t.Fatalf("StreamUpload: %v", err)
	}
	if _, ok := folders[driveFolderQuery("root", "parent")]; !ok {
		t.Fatal("parent directory was not created")
	}
}
