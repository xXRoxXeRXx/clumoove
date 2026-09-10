package storage

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/version"
)

func TestUserAgentTransport(t *testing.T) {
	var capturedUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: newUserAgentTransport(http.DefaultTransport),
	}

	// 1. Request without explicit User-Agent
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do failed: %v", err)
	}
	resp.Body.Close()

	expectedUA := version.UserAgent()
	if capturedUA != expectedUA {
		t.Errorf("expected User-Agent %q, got %q", expectedUA, capturedUA)
	}

	// 2. Request with default Go-http-client prefix
	req, err = http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", "Go-http-client/1.1")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("client.Do failed: %v", err)
	}
	resp.Body.Close()

	if capturedUA != expectedUA {
		t.Errorf("expected User-Agent %q after replacing Go-http-client, got %q", expectedUA, capturedUA)
	}

	// 3. Request with custom explicit non-Go-http-client User-Agent is preserved
	customUA := "Custom-Agent/2.0"
	req, err = http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("User-Agent", customUA)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("client.Do failed: %v", err)
	}
	resp.Body.Close()

	if capturedUA != customUA {
		t.Errorf("expected custom User-Agent %q to be preserved, got %q", customUA, capturedUA)
	}
}

func TestLoggingTransportSetsUserAgent(t *testing.T) {
	var capturedUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: newLoggingTransport(http.DefaultTransport),
	}
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if capturedUA != version.UserAgent() {
		t.Errorf("loggingTransport User-Agent = %q, want %q", capturedUA, version.UserAgent())
	}
}

func TestProvidersConfiguredWithUserAgentTransport(t *testing.T) {
	// 1. Providers using loggingTransport (which wraps User-Agent in RoundTrip)
	wd, err := NewWebDAVProvider("https://dav.example.test", "user", "pass")
	if err != nil {
		t.Fatalf("NewWebDAVProvider failed: %v", err)
	}
	if _, ok := wd.HTTPClient.Transport.(*loggingTransport); !ok {
		t.Errorf("WebDAV transport is %T, want *loggingTransport", wd.HTTPClient.Transport)
	}

	nc, err := NewNextcloudProvider("https://nextcloud.example.test", "user", "pass")
	if err != nil {
		t.Fatalf("NewNextcloudProvider failed: %v", err)
	}
	if _, ok := nc.HTTPClient.Transport.(*loggingTransport); !ok {
		t.Errorf("Nextcloud transport is %T, want *loggingTransport", nc.HTTPClient.Transport)
	}

	oc, err := NewOpenCloudProvider("https://oc.example.test", "user", "pass")
	if err != nil {
		t.Fatalf("NewOpenCloudProvider failed: %v", err)
	}
	if _, ok := oc.HTTPClient.Transport.(*loggingTransport); !ok {
		t.Errorf("OpenCloud transport is %T, want *loggingTransport", oc.HTTPClient.Transport)
	}

	mc, err := NewMagentacloudProvider("user", "pass")
	if err != nil {
		t.Fatalf("NewMagentacloudProvider failed: %v", err)
	}
	if _, ok := mc.HTTPClient.Transport.(*loggingTransport); !ok {
		t.Errorf("MagentaCLOUD transport is %T, want *loggingTransport", mc.HTTPClient.Transport)
	}

	hd, err := NewHiDriveProvider("access-token")
	if err != nil {
		t.Fatalf("NewHiDriveProvider failed: %v", err)
	}
	if _, ok := hd.HTTPClient.Transport.(*loggingTransport); !ok {
		t.Errorf("HiDrive transport is %T, want *loggingTransport", hd.HTTPClient.Transport)
	}

	kf, err := NewKoofrProvider("user", "pass")
	if err != nil {
		t.Fatalf("NewKoofrProvider failed: %v", err)
	}
	if _, ok := kf.HTTPClient.Transport.(*loggingTransport); !ok {
		t.Errorf("Koofr transport is %T, want *loggingTransport", kf.HTTPClient.Transport)
	}

	// 2. Providers using userAgentTransport
	dp, err := NewDropboxProvider("access-token")
	if err != nil {
		t.Fatalf("NewDropboxProvider failed: %v", err)
	}
	if _, ok := dp.HTTPClient.Transport.(*userAgentTransport); !ok {
		t.Errorf("Dropbox transport is %T, want *userAgentTransport", dp.HTTPClient.Transport)
	}

	im, err := NewImmichProvider("https://immich.example.test", "api-key")
	if err != nil {
		t.Fatalf("NewImmichProvider failed: %v", err)
	}
	if _, ok := im.HTTPClient.Transport.(*userAgentTransport); !ok {
		t.Errorf("Immich transport is %T, want *userAgentTransport", im.HTTPClient.Transport)
	}

	od, err := NewOneDriveProvider("access-token")
	if err != nil {
		t.Fatalf("NewOneDriveProvider failed: %v", err)
	}
	if _, ok := od.httpClient.Transport.(*userAgentTransport); !ok {
		t.Errorf("OneDrive transport is %T, want *userAgentTransport", od.httpClient.Transport)
	}
}


