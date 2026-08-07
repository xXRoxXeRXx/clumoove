package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ProviderConfig struct {
	AuthURL  string
	TokenURL string
	Scopes   []string
}

// configs holds the static endpoints and scopes for each provider. Client
// identity (ID/secret) is no longer embedded here; it is loaded at runtime from
// the instance_oauth_providers table via the process cache.
var configs = map[string]ProviderConfig{
	"dropbox": {
		AuthURL:  "https://www.dropbox.com/oauth2/authorize",
		TokenURL: "https://api.dropboxapi.com/oauth2/token",
	},
	"google": {
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		Scopes: []string{
			"https://www.googleapis.com/auth/drive",
			"https://www.googleapis.com/auth/calendar",
			"https://www.googleapis.com/auth/contacts",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
	},
	"onedrive": {
		AuthURL:  "https://login.microsoftonline.com/consumers/oauth2/v2.0/authorize",
		TokenURL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/token",
		// Files.ReadWrite.All is required to access files shared with the user;
		// Files.ReadWrite alone is insufficient for remote OneDrive items.
		Scopes: []string{"openid", "profile", "offline_access", "User.Read", "Files.ReadWrite.All"},
	},
	// Note: HiDrive OAuth requires comma-separated scopes ("admin,rw"), joined as single string.
	"hidrive": {
		AuthURL:  "https://my.hidrive.com/client/authorize",
		TokenURL: "https://my.hidrive.com/oauth2/token",
		Scopes:   []string{"admin,rw"},
	},
}

var httpClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		IdleConnTimeout: 30 * time.Second,
		MaxIdleConns:    10,
	},
}

var oneDriveUserInfoURL = "https://graph.microsoft.com/v1.0/me?$select=displayName,mail,userPrincipalName,id"

var providerNames = map[string]struct{}{
	"dropbox":  {},
	"google":   {},
	"onedrive": {},
	"hidrive":  {},
}

// IsProvider reports whether provider uses the shared OAuth credential lifecycle.
func IsProvider(provider string) bool {
	_, ok := providerNames[provider]
	return ok
}

func GetAuthURL(provider, redirectURI, state string) (string, error) {
	config, ok := configs[provider]
	if !ok {
		return "", fmt.Errorf("unknown provider: %s", provider)
	}
	clientID, err := clientID(provider)
	if err != nil {
		return "", fmt.Errorf("client ID for %s is not configured", provider)
	}

	u, err := url.Parse(config.AuthURL)
	if err != nil {
		return "", err
	}

	q := u.Query()
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	if len(config.Scopes) > 0 {
		q.Set("scope", strings.Join(config.Scopes, " "))
	}
	// Request offline access for Google to receive a refresh_token.
	if provider == "google" {
		q.Set("access_type", "offline")
		q.Set("prompt", "consent") // force consent screen so refresh_token is always returned
	}
	u.RawQuery = q.Encode()

	return u.String(), nil
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func ExchangeCode(ctx context.Context, provider, code, redirectURI string) (*TokenResponse, error) {
	config, ok := configs[provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
	clientID, err := clientID(provider)
	if err != nil {
		return nil, fmt.Errorf("client ID for %s is not configured", provider)
	}
	clientSecret, err := clientSecret(provider)
	if err != nil {
		return nil, fmt.Errorf("client secret for %s is not configured", provider)
	}

	data := url.Values{}
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, "POST", config.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			ErrorDescription string `json:"error_description"`
			Error            string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		if errResp.ErrorDescription != "" {
			return nil, fmt.Errorf("token exchange failed: %s", errResp.ErrorDescription)
		}
		return nil, fmt.Errorf("token exchange failed with status: %d", resp.StatusCode)
	}

	var tr TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}

	return &tr, nil
}

func GetUserInfo(ctx context.Context, provider, token string) (string, error) {
	switch provider {
	case "dropbox":
		req, err := http.NewRequestWithContext(ctx, "POST", "https://api.dropboxapi.com/2/users/get_current_account", bytesReaderNull())
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("failed to fetch user info: status %d", resp.StatusCode)
		}

		var info struct {
			Name struct {
				DisplayName string `json:"display_name"`
			} `json:"name"`
			Email string `json:"email"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return "", err
		}

		if info.Name.DisplayName != "" {
			return info.Name.DisplayName, nil
		}
		return info.Email, nil
	case "google":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/oauth2/v2/userinfo", nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("failed to fetch google user info: status %d", resp.StatusCode)
		}

		var info struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return "", err
		}

		if info.Name != "" {
			return info.Name, nil
		}
		return info.Email, nil
	case "hidrive":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.hidrive.strato.com/2.1/user/me?fields=account,alias", nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("failed to fetch hidrive user info: status %d", resp.StatusCode)
		}

		var info struct {
			Account string `json:"account"`
			Alias   string `json:"alias"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return "", err
		}

		if info.Alias != "" {
			return info.Alias, nil
		}
		if info.Account != "" {
			return info.Account, nil
		}
		return "HiDrive User", nil
	case "onedrive":
		req, err := http.NewRequestWithContext(ctx, "GET", oneDriveUserInfoURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("failed to fetch onedrive user info: status %d", resp.StatusCode)
		}
		var info struct {
			DisplayName       string `json:"displayName"`
			Mail              string `json:"mail"`
			UserPrincipalName string `json:"userPrincipalName"`
			ID                string `json:"id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return "", err
		}
		for _, value := range []string{info.DisplayName, info.Mail, info.UserPrincipalName, info.ID} {
			if value != "" {
				return value, nil
			}
		}
		return "OneDrive User", nil
	default:
		return "OAuth User", nil
	}
}

// RefreshToken exchanges a refresh token for a new access (and possibly refresh) token.
// If the provider does not return a new refresh token (e.g. Google), the original
// refresh token is preserved in the returned TokenResponse.
func RefreshToken(ctx context.Context, provider, refreshToken string) (*TokenResponse, error) {
	config, ok := configs[provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
	clientID, err := clientID(provider)
	if err != nil {
		return nil, fmt.Errorf("client ID for %s is not configured", provider)
	}
	clientSecret, err := clientSecret(provider)
	if err != nil {
		return nil, fmt.Errorf("client secret for %s is not configured", provider)
	}

	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)

	req, err := http.NewRequestWithContext(ctx, "POST", config.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// OAuth error bodies are controlled by an external provider and can include
		// credential hints. Do not surface their contents to callers, logs, or the
		// persisted migration status.
		return nil, fmt.Errorf("token refresh failed with status: %d", resp.StatusCode)
	}

	var tr TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}

	// Google and some providers don't return a new refresh_token on every refresh;
	// preserve the original so we can keep rotating.
	if tr.RefreshToken == "" {
		tr.RefreshToken = refreshToken
	}
	// Default expiry to 1 hour if provider didn't specify
	if tr.ExpiresIn == 0 {
		tr.ExpiresIn = 3600
	}

	return &tr, nil
}

// bytesReaderNull returns an io.Reader containing "null" to satisfy Dropbox's JSON body requirement.
func bytesReaderNull() *strings.Reader {
	return strings.NewReader("null")
}
