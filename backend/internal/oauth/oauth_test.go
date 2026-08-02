package oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestOneDriveOAuthFlow(t *testing.T) {
	t.Setenv("ONEDRIVE_CLIENT_ID", "client-id")
	t.Setenv("ONEDRIVE_CLIENT_SECRET", "client-secret")
	InitConfigs()

	authURL, err := GetAuthURL("onedrive", "https://clumoove.example/api/oauth/callback", "csrf-state")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "login.microsoftonline.com" || q.Get("state") != "csrf-state" || q.Get("scope") != "openid profile offline_access User.Read Files.ReadWrite.All" {
		t.Fatalf("unexpected OneDrive authorization URL: %s", authURL)
	}

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

	oldClient, oldUserURL, oldConfig := httpClient, oneDriveUserInfoURL, configs["onedrive"]
	httpClient, oneDriveUserInfoURL = server.Client(), server.URL+"/me"
	configs["onedrive"] = ProviderConfig{ClientID: "client-id", ClientSecret: "client-secret", TokenURL: server.URL + "/token"}
	defer func() { httpClient, oneDriveUserInfoURL, configs["onedrive"] = oldClient, oldUserURL, oldConfig }()

	token, err := ExchangeCode(context.Background(), "onedrive", "code", "https://clumoove.example/api/oauth/callback")
	if err != nil || token.RefreshToken != "refresh-token" {
		t.Fatalf("ExchangeCode = %#v, %v", token, err)
	}
	refreshed, err := RefreshToken(context.Background(), "onedrive", "refresh-token")
	if err != nil || refreshed.RefreshToken != "rotated-refresh" {
		t.Fatalf("RefreshToken = %#v, %v", refreshed, err)
	}
	user, err := GetUserInfo(context.Background(), "onedrive", "access-token")
	if err != nil || user != "Ada Example" {
		t.Fatalf("GetUserInfo = %q, %v", user, err)
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
