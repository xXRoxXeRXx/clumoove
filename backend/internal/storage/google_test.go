package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/people/v1"
)

type googleAuthOperation struct {
	name string
	run  func() error
}

func newGoogleTestProvider(t *testing.T, endpoint string) *GoogleProvider {
	t.Helper()

	p, err := NewGoogleProvider(context.Background(), "mock-token")
	if err != nil {
		t.Fatalf("NewGoogleProvider: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	p.driveService, err = drive.NewService(context.Background(), option.WithHTTPClient(p.httpClient), option.WithEndpoint(endpoint))
	if err != nil {
		t.Fatalf("create test Drive service: %v", err)
	}
	p.calendarService, err = calendar.NewService(context.Background(), option.WithHTTPClient(p.httpClient), option.WithEndpoint(endpoint))
	if err != nil {
		t.Fatalf("create test Calendar service: %v", err)
	}
	p.peopleService, err = people.NewService(context.Background(), option.WithHTTPClient(p.httpClient), option.WithEndpoint(endpoint))
	if err != nil {
		t.Fatalf("create test People service: %v", err)
	}
	return p
}

func requireGoogleAuthError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("errors.Is(err, ErrAuth) = false, want true (err = %v)", err)
	}
}

func writeGoogleAuthorizationError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":{"code":401,"message":"Invalid Credentials"}}`))
}

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

func TestWrapGoogleErrorMarksUnauthorizedAndForbiddenResponsesAsAuthenticationFailures(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			apiErr := &googleapi.Error{Code: status, Message: http.StatusText(status)}
			err := wrapGoogleError("google test operation", apiErr)
			if !errors.Is(err, ErrAuth) {
				t.Fatalf("errors.Is(err, ErrAuth) = false, want true (err = %v)", err)
			}

			var wrappedAPIError *googleapi.Error
			if !errors.As(err, &wrappedAPIError) || wrappedAPIError != apiErr {
				t.Fatalf("wrapped error does not retain the Google API error: %v", err)
			}
		})
	}
}

func TestWrapGoogleErrorDoesNotMarkOtherGoogleErrorsAsAuthenticationFailures(t *testing.T) {
	err := wrapGoogleError("google test operation", &googleapi.Error{Code: http.StatusNotFound, Message: "Not Found"})
	if errors.Is(err, ErrAuth) {
		t.Fatalf("errors.Is(err, ErrAuth) = true, want false (err = %v)", err)
	}
}

func TestWrapGoogleErrorMarksPermanentTransferResponses(t *testing.T) {
	apiErr := &googleapi.Error{Code: http.StatusBadRequest, Message: "export unavailable"}
	err := wrapGoogleError("google export", apiErr)
	if !errors.Is(err, ErrPermanentTransfer) {
		t.Fatalf("errors.Is(err, ErrPermanentTransfer) = false, want true (err = %v)", err)
	}
	var wrappedAPIError *googleapi.Error
	if !errors.As(err, &wrappedAPIError) || wrappedAPIError != apiErr {
		t.Fatalf("wrapped error does not retain the Google API error: %v", err)
	}
}

func TestWrapGoogleErrorDoesNotDoubleWrapAuthenticationFailures(t *testing.T) {
	wrapped := wrapGoogleError("google drive file lookup", &googleapi.Error{Code: http.StatusUnauthorized, Message: "Invalid Credentials"})
	got := wrapGoogleNotFound(wrapped)
	if got != wrapped {
		t.Fatalf("wrapGoogleNotFound changed an already translated auth error: %v", got)
	}
	if strings.Count(got.Error(), ErrAuth.Error()) != 1 {
		t.Fatalf("authentication failure appears more than once: %v", got)
	}
}

func TestGoogleDriveOperationsTranslateAuthorizationFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeGoogleAuthorizationError(w)
	}))
	defer server.Close()

	p := newGoogleTestProvider(t, server.URL+"/")

	tests := []googleAuthOperation{
		{name: "listing", run: func() error {
			_, err := p.GetDirectoryListing(context.Background(), "files", "/")
			return err
		}},
		{name: "inspect", run: func() error {
			_, err := p.InspectResource(context.Background(), "files", "/file.txt")
			return err
		}},
		{name: "download", run: func() error {
			body, err := p.StreamDownload(context.Background(), "files", "/file.txt")
			if body != nil {
				_ = body.Close()
			}
			return err
		}},
		{name: "upload", run: func() error {
			return p.StreamUpload(context.Background(), "files", "/file.txt", bytes.NewBufferString("body"), 4)
		}},
		{name: "file exists", run: func() error {
			_, _, err := p.FileExists(context.Background(), "files", "/file.txt")
			return err
		}},
		{name: "delete", run: func() error {
			return p.DeleteFile(context.Background(), "files", "/file.txt")
		}},
		{name: "rename", run: func() error {
			return p.RenameFile(context.Background(), "files", "/file.txt", "/renamed.txt")
		}},
		{name: "create directory", run: func() error {
			return p.CreateDirectory(context.Background(), "files", "/folder")
		}},
		{name: "metadata", run: func() error {
			return p.ApplyMetadata(context.Background(), "files", "/file.txt", FileMetadata{Description: "metadata"})
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireGoogleAuthError(t, test.run())
		})
	}
}

func TestGoogleCalendarAndContactOperationsTranslateAuthorizationFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeGoogleAuthorizationError(w)
	}))
	defer server.Close()

	p := newGoogleTestProvider(t, server.URL+"/")

	tests := []googleAuthOperation{
		{name: "calendar listing", run: func() error {
			_, err := p.GetDirectoryListing(context.Background(), "calendars", "/calendar-id")
			return err
		}},
		{name: "calendar download", run: func() error {
			body, err := p.StreamDownload(context.Background(), "calendars", "/calendar-id/event-id.ics")
			if body != nil {
				_ = body.Close()
			}
			return err
		}},
		{name: "calendar insert", run: func() error {
			return p.StreamUpload(context.Background(), "calendars", "/calendar-id/event-id.ics", bytes.NewBufferString("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"), 0)
		}},
		{name: "contact listing", run: func() error {
			_, err := p.GetDirectoryListing(context.Background(), "contacts", "/contacts")
			return err
		}},
		{name: "contact download", run: func() error {
			body, err := p.StreamDownload(context.Background(), "contacts", "/contacts/contact-id.vcf")
			if body != nil {
				_ = body.Close()
			}
			return err
		}},
		{name: "contact lookup during upload", run: func() error {
			return p.StreamUpload(context.Background(), "contacts", "/contacts/contact-id.vcf", bytes.NewBufferString("BEGIN:VCARD\r\nFN:Contact\r\nEND:VCARD\r\n"), 0)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireGoogleAuthError(t, test.run())
		})
	}
}

func TestGoogleCalendarAndContactMutationsTranslateAuthorizationFailures(t *testing.T) {
	mode := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case mode == "calendar update" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"items":[{"id":"existing-event"}]}`))
			return
		case mode == "contact update" && strings.Contains(r.URL.Path, "searchContacts"):
			_, _ = w.Write([]byte(`{"results":[{"person":{"resourceName":"people/existing"}}]}`))
			return
		case mode == "contact update" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"etag":"contact-etag"}`))
			return
		case mode == "contact create" && strings.Contains(r.URL.Path, "searchContacts"):
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		default:
			writeGoogleAuthorizationError(w)
		}
	}))
	defer server.Close()

	p := newGoogleTestProvider(t, server.URL+"/")

	tests := []googleAuthOperation{
		{name: "calendar update", run: func() error {
			return p.StreamUpload(context.Background(), "calendars", "/calendar-id/event-id.ics", bytes.NewBufferString("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:calendar-event\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"), 0)
		}},
		{name: "contact update", run: func() error {
			return p.StreamUpload(context.Background(), "contacts", "/contacts/contact-id.vcf", bytes.NewBufferString("BEGIN:VCARD\r\nFN:Existing Contact\r\nEND:VCARD\r\n"), 0)
		}},
		{name: "contact create", run: func() error {
			return p.StreamUpload(context.Background(), "contacts", "/contacts/contact-id.vcf", bytes.NewBufferString("BEGIN:VCARD\r\nFN:New Contact\r\nEND:VCARD\r\n"), 0)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode = test.name
			requireGoogleAuthError(t, test.run())
		})
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
