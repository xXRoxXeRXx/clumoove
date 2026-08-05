package storage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewNextcloudProviderURLNormalization(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "https://nextcloud.example.com",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://nextcloud.example.com/",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://nextcloud.example.com/remote.php/dav",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://nextcloud.example.com/remote.php/dav/",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://nextcloud.example.com/remote.php/dav/files/user",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://nextcloud.example.com/remote.php/dav/files/user/",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://nextcloud.example.com/remote.php/dav/files/user/subfolder",
			expected: "https://nextcloud.example.com/remote.php/dav",
		},
		{
			input:    "https://example.com/nextcloud/remote.php/dav/files/user",
			expected: "https://example.com/nextcloud/remote.php/dav",
		},
		{
			input:    "https://example.com/remote.php/webdav/files/user",
			expected: "https://example.com/remote.php/dav",
		},
	}

	for _, tt := range tests {
		p, err := NewNextcloudProvider(tt.input, "user", "pass")
		if err != nil {
			t.Errorf("NewNextcloudProvider(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if p.BaseURL != tt.expected {
			t.Errorf("NewNextcloudProvider(%q).BaseURL = %q, want %q", tt.input, p.BaseURL, tt.expected)
		}
	}
}

func TestNextcloudProviderVerificationModeIsSizeOnly(t *testing.T) {
	p, err := NewNextcloudProvider("https://nextcloud.example.com", "user", "pass")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	if got := p.VerificationMode(); got != VerificationSizeOnly {
		t.Fatalf("VerificationMode() = %q, want %q", got, VerificationSizeOnly)
	}
}

func TestNextcloudFileExistsFallsBackToPropfindForUnknownHEADLength(t *testing.T) {
	propfindResponse := `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:">
	<d:response>
		<d:propstat>
			<d:prop>
				<d:getcontentlength>0</d:getcontentlength>
				<d:resourcetype/>
			</d:prop>
			<d:status>HTTP/1.1 200 OK</d:status>
		</d:propstat>
	</d:response>
</d:multistatus>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusOK) // Deliberately omit Content-Length.
		case "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(propfindResponse))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	p, err := NewNextcloudProvider(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("NewNextcloudProvider: %v", err)
	}
	exists, size, err := p.FileExists(context.Background(), "files", "/empty.txt")
	if err != nil || !exists || size != 0 {
		t.Fatalf("FileExists() = (%v, %d, %v), want (true, 0, nil)", exists, size, err)
	}
}

func TestNextcloudFileExistsUsesHEADForExplicitZeroLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("unexpected method %s", r.Method)
			return
		}
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p, err := NewNextcloudProvider(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("NewNextcloudProvider: %v", err)
	}
	exists, size, err := p.FileExists(context.Background(), "files", "/empty.txt")
	if err != nil || !exists || size != 0 {
		t.Fatalf("FileExists() = (%v, %d, %v), want (true, 0, nil)", exists, size, err)
	}
}

func TestNextcloudCalendarListingFiltering(t *testing.T) {
	xmlResponse := `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav" xmlns:cs="http://calendarserver.org/ns/">
	<d:response>
		<d:href>/remote.php/dav/calendars/testuser/</d:href>
		<d:propstat>
			<d:status>HTTP/1.1 200 OK</d:status>
			<d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop>
		</d:propstat>
	</d:response>
	<d:response>
		<d:href>/remote.php/dav/calendars/testuser/personal/</d:href>
		<d:propstat>
			<d:status>HTTP/1.1 200 OK</d:status>
			<d:prop><d:resourcetype><d:collection/><cal:calendar/></d:resourcetype></d:prop>
		</d:propstat>
	</d:response>
	<d:response>
		<d:href>/remote.php/dav/calendars/testuser/work/</d:href>
		<d:propstat>
			<d:status>HTTP/1.1 200 OK</d:status>
			<d:prop><d:resourcetype><d:collection/><cal:calendar/></d:resourcetype></d:prop>
		</d:propstat>
	</d:response>
	<d:response>
		<d:href>/remote.php/dav/calendars/testuser/inbox/</d:href>
		<d:propstat>
			<d:status>HTTP/1.1 200 OK</d:status>
			<d:prop><d:resourcetype><d:collection/><cal:schedule-inbox/></d:resourcetype></d:prop>
		</d:propstat>
	</d:response>
	<d:response>
		<d:href>/remote.php/dav/calendars/testuser/outbox/</d:href>
		<d:propstat>
			<d:status>HTTP/1.1 200 OK</d:status>
			<d:prop><d:resourcetype><d:collection/><cal:schedule-outbox/></d:resourcetype></d:prop>
		</d:propstat>
	</d:response>
	<d:response>
		<d:href>/remote.php/dav/calendars/testuser/notifications/</d:href>
		<d:propstat>
			<d:status>HTTP/1.1 200 OK</d:status>
			<d:prop><d:resourcetype><d:collection/><cs:notification-inbox/></d:resourcetype></d:prop>
		</d:propstat>
	</d:response>
	<d:response>
		<d:href>/remote.php/dav/calendars/testuser/readme.txt</d:href>
		<d:propstat>
			<d:status>HTTP/1.1 200 OK</d:status>
			<d:prop><d:resourcetype/><d:getcontentlength>100</d:getcontentlength></d:prop>
		</d:propstat>
	</d:response>
</d:multistatus>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(xmlResponse))
	}))
	defer server.Close()

	p, err := NewNextcloudProvider(server.URL, "testuser", "password")
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	items, err := p.GetDirectoryListing(context.Background(), "calendars", "/")
	if err != nil {
		t.Fatalf("GetDirectoryListing failed: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("Expected 2 calendar items, got %d", len(items))
	}

	expectedNames := map[string]bool{"personal": true, "work": true}
	for _, item := range items {
		if !expectedNames[item.Name] {
			t.Errorf("Unexpected item in calendar listing: %s", item.Name)
		}
		if !item.IsDir {
			t.Errorf("Expected calendar item %s to have IsDir=true", item.Name)
		}
	}
}

func TestIsSystemOrAppGeneratedCollection(t *testing.T) {
	systemNames := []string{
		"app-generated--deck--board-1",
		"app-generated--circles--group-1",
		"z-server-generated--system",
		"contact_birthdays",
		"contact-birthdays",
		"contact_birthdays.ics",
		"birthdays",
		"inbox",
		"outbox",
	}

	for _, name := range systemNames {
		if !IsSystemOrAppGeneratedCollection(name) {
			t.Errorf("Expected IsSystemOrAppGeneratedCollection(%q) = true, got false", name)
		}
	}

	userNames := []string{"personal", "work", "contacts-default", "family"}
	for _, name := range userNames {
		if IsSystemOrAppGeneratedCollection(name) {
			t.Errorf("Expected IsSystemOrAppGeneratedCollection(%q) = false, got true", name)
		}
	}

	systemPaths := []string{
		"/app-generated--deck--board-1/card-1.ics",
		"/contact_birthdays/contacts-default.ics",
		"/z-server-generated--system/Database:admin.vcf",
	}
	for _, p := range systemPaths {
		if !IsSystemOrAppGeneratedPath(p) {
			t.Errorf("Expected IsSystemOrAppGeneratedPath(%q) = true, got false", p)
		}
	}
}

func TestNextcloudFileExistsHEADFallback(t *testing.T) {
	// Server returns 500 on HEAD (typical SabreDAV CardDAV behavior), but 207 MultiStatus on PROPFIND.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Method == "PROPFIND" {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
	<d:response>
		<d:href>/remote.php/dav/contacts/user/contacts/c123.vcf</d:href>
		<d:propstat>
			<d:status>HTTP/1.1 200 OK</d:status>
			<d:prop><d:getcontentlength>150</d:getcontentlength></d:prop>
		</d:propstat>
	</d:response>
</d:multistatus>`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	p, err := NewNextcloudProvider(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	exists, size, err := p.FileExists(context.Background(), "contacts", "/contacts/c123.vcf")
	if err != nil {
		t.Fatalf("FileExists returned error despite PROPFIND fallback: %v", err)
	}
	if !exists {
		t.Errorf("Expected exists=true, got false")
	}
	if size != 150 {
		t.Errorf("Expected size=150, got %d", size)
	}
}

func TestNextcloudInspectResourceNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	p, err := NewNextcloudProvider(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("NewNextcloudProvider: %v", err)
	}
	_, err = p.InspectResource(context.Background(), "files", "/missing.txt")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("InspectResource missing error = %v, want ErrNotFound", err)
	}
}

