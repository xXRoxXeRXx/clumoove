package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type ProviderConfig struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Scopes       []string
}

var configs = map[string]ProviderConfig{}

var httpClient = &http.Client{
	Timeout: 15 * time.Second,
}

func InitConfigs() {
	configs["dropbox"] = ProviderConfig{
		ClientID:     os.Getenv("DROPBOX_CLIENT_ID"),
		ClientSecret: os.Getenv("DROPBOX_CLIENT_SECRET"),
		AuthURL:      "https://www.dropbox.com/oauth2/authorize",
		TokenURL:     "https://api.dropboxapi.com/oauth2/token",
	}
	configs["google"] = ProviderConfig{
		ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		Scopes: []string{
			"https://www.googleapis.com/auth/drive",
			"https://www.googleapis.com/auth/calendar",
			"https://www.googleapis.com/auth/contacts",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
	}
}

func GetAuthURL(provider, redirectURI, state string) (string, error) {
	config, ok := configs[provider]
	if !ok {
		return "", fmt.Errorf("unknown provider: %s", provider)
	}
	if config.ClientID == "" {
		return "", fmt.Errorf("client ID for %s is not configured in backend environment", provider)
	}

	u, err := url.Parse(config.AuthURL)
	if err != nil {
		return "", err
	}

	q := u.Query()
	q.Set("client_id", config.ClientID)
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
	if config.ClientID == "" || config.ClientSecret == "" {
		return nil, fmt.Errorf("client ID/secret for %s is not configured in backend environment", provider)
	}

	data := url.Values{}
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("client_id", config.ClientID)
	data.Set("client_secret", config.ClientSecret)
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
	default:
		return "OAuth User", nil
	}
}

// bytesReaderNull returns an io.Reader containing "null" to satisfy Dropbox's JSON body requirement.
func bytesReaderNull() *strings.Reader {
	return strings.NewReader("null")
}
