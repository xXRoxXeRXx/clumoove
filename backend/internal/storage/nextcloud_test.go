package storage

import (
	"context"
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

