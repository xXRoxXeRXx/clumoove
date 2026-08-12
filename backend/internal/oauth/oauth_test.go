package oauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"backend/internal/crypto"
	"backend/internal/observability"
)

const testEncryptionKey = "0123456789abcdef0123456789abcdef"

func configureOAuthTestCredentials(t *testing.T, providers ...string) {
	t.Helper()
	enc, err := crypto.EncryptWithDomain("client-secret", testEncryptionKey, crypto.DomainOAuthClientSecret)
	if err != nil {
		t.Fatal(err)
	}
	credentials := make(map[string]Credentials, len(providers))
	for _, provider := range providers {
		credentials[provider] = Credentials{ClientID: "client-id", ClientSecretEnc: enc}
	}
	Configure(func() (map[string]Credentials, error) { return credentials, nil }, testEncryptionKey)
	t.Cleanup(Invalidate)
}

func TestOneDriveOAuthFlow(t *testing.T) {
	configureOAuthTestCredentials(t, "onedrive")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/me" {
			if r.Header.Get("Authorization") != "Bearer access-token" {
				t.Error("missing bearer token")
			}
			_, _ = io.WriteString(w, `{"displayName":"Ada Example","mail":"ada@example.test"}`)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("grant_type") == "authorization_code" {
			_, _ = io.WriteString(w, `{"access_token":"access-token","refresh_token":"refresh-token","expires_in":3600}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"refreshed-access","refresh_token":"rotated-refresh","expires_in":3600}`)
	}))
	defer server.Close()

	client := newOAuthClient(server.Client(), map[string]ProviderConfig{
		"onedrive": {
			AuthURL:     server.URL + "/authorize",
			TokenURL:    server.URL + "/token",
			UserInfoURL: server.URL + "/me",
			Scopes:      []string{"openid", "profile", "offline_access", "User.Read", "Files.ReadWrite.All"},
		},
	})

	authURL, err := client.getAuthURL("onedrive", "https://clumoove.example/api/oauth/callback", "csrf-state")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != strings.TrimPrefix(server.URL, "http://") || q.Get("state") != "csrf-state" || q.Get("scope") != "openid profile offline_access User.Read Files.ReadWrite.All" {
		t.Fatalf("unexpected OneDrive authorization URL: %s", authURL)
	}

	token, err := client.exchangeCode(context.Background(), "onedrive", "code", "https://clumoove.example/api/oauth/callback")
	if err != nil || token.RefreshToken != "refresh-token" {
		t.Fatalf("exchangeCode = %#v, %v", token, err)
	}
	refreshed, err := client.refreshToken(context.Background(), "onedrive", "refresh-token")
	if err != nil || refreshed.RefreshToken != "rotated-refresh" {
		t.Fatalf("refreshToken = %#v, %v", refreshed, err)
	}
	user, err := client.getUserInfo(context.Background(), "onedrive", "access-token")
	if err != nil || user != "Ada Example" {
		t.Fatalf("getUserInfo = %q, %v", user, err)
	}
}

func TestExchangeCodeDoesNotExposeProviderDescription(t *testing.T) {
	configureOAuthTestCredentials(t, "google")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"client_secret=leak-me"}`)
	}))
	defer server.Close()

	client := newOAuthClient(server.Client(), map[string]ProviderConfig{"google": {TokenURL: server.URL}})
	_, err := client.exchangeCode(context.Background(), "google", "code", "https://clumoove.example/callback")
	if err == nil || err.Error() != "token exchange failed with status: 400" {
		t.Fatalf("exchangeCode error = %v, want redacted status error", err)
	}
}

func TestRefreshTokenClassifiesAndPreserves(t *testing.T) {
	configureOAuthTestCredentials(t, "google")
	tests := []struct {
		name      string
		status    int
		response  string
		invalid   bool
		kind      string
		wantToken string
	}{
		{name: "preserves omitted refresh token", status: http.StatusOK, response: `{"access_token":"new-access","expires_in":3600}`, wantToken: "original-refresh"},
		{name: "invalid grant is permanent", status: http.StatusBadRequest, response: `{"error":"invalid_grant","error_description":"refresh_token=leak-me"}`, invalid: true, kind: "authentication"},
		{name: "rate limited is retryable", status: http.StatusTooManyRequests, response: `{"error":"temporarily_unavailable"}`, kind: "rate_limited"},
		{name: "server error is retryable", status: http.StatusBadGateway, response: `{"error":"server_error"}`, kind: "internal"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			client := newOAuthClient(server.Client(), map[string]ProviderConfig{"google": {TokenURL: server.URL}})

			token, err := client.refreshToken(context.Background(), "google", "original-refresh")
			if tc.status == http.StatusOK {
				if err != nil || token.RefreshToken != tc.wantToken {
					t.Fatalf("refreshToken = %#v, %v", token, err)
				}
				return
			}
			if err == nil || errors.Is(err, ErrRefreshTokenInvalid) != tc.invalid {
				t.Fatalf("refreshToken error = %v, invalid = %v, want %v", err, errors.Is(err, ErrRefreshTokenInvalid), tc.invalid)
			}
			if strings.Contains(err.Error(), "leak-me") {
				t.Fatalf("refresh error leaked provider description: %v", err)
			}
			if got := observability.ErrorKind(err); got != tc.kind {
				t.Fatalf("ErrorKind(%v) = %q, want %q", err, got, tc.kind)
			}
		})
	}
}

func TestGetUserInfoProviders(t *testing.T) {
	var requestIssues []error
	var requestIssuesMu sync.Mutex
	recordIssue := func(format string, args ...any) {
		requestIssuesMu.Lock()
		defer requestIssuesMu.Unlock()
		requestIssues = append(requestIssues, fmt.Errorf(format, args...))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-token" {
			recordIssue("missing bearer token")
		}
		switch r.URL.Path {
		case "/dropbox":
			body, _ := io.ReadAll(r.Body)
			if r.Method != http.MethodPost || string(body) != "null" {
				recordIssue("Dropbox request = %s %q", r.Method, body)
			}
			_, _ = io.WriteString(w, `{"name":{"display_name":"Ada Dropbox"},"email":"ada@example.test"}`)
		case "/google":
			_, _ = io.WriteString(w, `{"name":"Ada Google","email":"ada@example.test"}`)
		case "/hidrive":
			_, _ = io.WriteString(w, `{"account":"ada","alias":"Ada HiDrive"}`)
		case "/onedrive":
			_, _ = io.WriteString(w, `{"displayName":"Ada OneDrive"}`)
		default:
			http.NotFound(w, r)
		}
	}))

	client := newOAuthClient(server.Client(), map[string]ProviderConfig{
		"dropbox":  {UserInfoURL: server.URL + "/dropbox"},
		"google":   {UserInfoURL: server.URL + "/google"},
		"hidrive":  {UserInfoURL: server.URL + "/hidrive"},
		"onedrive": {UserInfoURL: server.URL + "/onedrive"},
	})
	for provider, want := range map[string]string{
		"dropbox":  "Ada Dropbox",
		"google":   "Ada Google",
		"hidrive":  "Ada HiDrive",
		"onedrive": "Ada OneDrive",
	} {
		got, err := client.getUserInfo(context.Background(), provider, "access-token")
		if err != nil || got != want {
			t.Errorf("getUserInfo(%q) = %q, %v; want %q", provider, got, err, want)
		}
	}
	server.Close()
	requestIssuesMu.Lock()
	defer requestIssuesMu.Unlock()
	for _, issue := range requestIssues {
		t.Error(issue)
	}
}

func TestDecodeOAuthResponseLimit(t *testing.T) {
	var response TokenResponse
	err := decodeOAuthResponse(strings.NewReader(`{"access_token":"`+strings.Repeat("x", int(maxOAuthResponseBodyBytes))+`"}`), &response)
	if err == nil {
		t.Fatal("decodeOAuthResponse accepted a response larger than the limit")
	}
}

func TestIsProvider(t *testing.T) {
	for _, provider := range []string{"dropbox", "google", "onedrive", "hidrive"} {
		if !IsProvider(provider) {
			t.Errorf("IsProvider(%q) = false, want true", provider)
		}
	}
	if IsProvider("nextcloud") {
		t.Error("IsProvider(nextcloud) = true, want false")
	}
}
