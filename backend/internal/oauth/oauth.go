package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxOAuthResponseBodyBytes int64 = 1 << 20

type ProviderConfig struct {
	AuthURL     string
	TokenURL    string
	UserInfoURL string
	Scopes      []string
}

// providerConfigs holds the static endpoints and scopes for each provider. Client
// identity (ID/secret) is no longer embedded here; it is loaded at runtime from
// the instance_oauth_providers table via the process cache.
var providerConfigs = map[string]ProviderConfig{
	"dropbox": {
		AuthURL:     "https://www.dropbox.com/oauth2/authorize",
		TokenURL:    "https://api.dropboxapi.com/oauth2/token",
		UserInfoURL: "https://api.dropboxapi.com/2/users/get_current_account",
	},
	"google": {
		AuthURL:     "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:    "https://oauth2.googleapis.com/token",
		UserInfoURL: "https://www.googleapis.com/oauth2/v2/userinfo",
		Scopes: []string{
			"https://www.googleapis.com/auth/drive",
			"https://www.googleapis.com/auth/calendar",
			"https://www.googleapis.com/auth/contacts",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
	},
	"onedrive": {
		AuthURL:     "https://login.microsoftonline.com/consumers/oauth2/v2.0/authorize",
		TokenURL:    "https://login.microsoftonline.com/consumers/oauth2/v2.0/token",
		UserInfoURL: "https://graph.microsoft.com/v1.0/me?$select=displayName,mail,userPrincipalName,id",
		// Files.ReadWrite.All is required to access files shared with the user;
		// Files.ReadWrite alone is insufficient for remote OneDrive items.
		Scopes: []string{"openid", "profile", "offline_access", "User.Read", "Files.ReadWrite.All"},
	},
	// HiDrive treats "admin,rw" as one comma-separated scope string.
	"hidrive": {
		AuthURL:     "https://my.hidrive.com/client/authorize",
		TokenURL:    "https://my.hidrive.com/oauth2/token",
		UserInfoURL: "https://api.hidrive.strato.com/2.1/user/me?fields=account,alias",
		Scopes:      []string{"admin,rw"},
	},
}

// oauthClient keeps transport and endpoint dependencies together so tests can
// use isolated clients without mutating package state. The default client and
// providerConfigs are immutable after package initialization.
type oauthClient struct {
	httpClient *http.Client
	configs    map[string]ProviderConfig
}

func newOAuthClient(httpClient *http.Client, configs map[string]ProviderConfig) *oauthClient {
	return &oauthClient{httpClient: httpClient, configs: configs}
}

var defaultOAuthClient = newOAuthClient(&http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		IdleConnTimeout: 30 * time.Second,
		MaxIdleConns:    10,
	},
}, providerConfigs)

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
	return defaultOAuthClient.getAuthURL(provider, redirectURI, state)
}

func (c *oauthClient) getAuthURL(provider, redirectURI, state string) (string, error) {
	config, ok := c.configs[provider]
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
	return defaultOAuthClient.exchangeCode(ctx, provider, code, redirectURI)
}

func (c *oauthClient) exchangeCode(ctx context.Context, provider, code, redirectURI string) (*TokenResponse, error) {
	config, ok := c.configs[provider]
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

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed with status: %d", resp.StatusCode)
	}

	var tr TokenResponse
	if err := decodeOAuthResponse(resp.Body, &tr); err != nil {
		return nil, err
	}

	return &tr, nil
}

func GetUserInfo(ctx context.Context, provider, token string) (string, error) {
	return defaultOAuthClient.getUserInfo(ctx, provider, token)
}

func (c *oauthClient) getUserInfo(ctx context.Context, provider, token string) (string, error) {
	config, ok := c.configs[provider]
	if !ok {
		return "OAuth User", nil
	}

	switch provider {
	case "dropbox":
		req, err := http.NewRequestWithContext(ctx, "POST", config.UserInfoURL, strings.NewReader("null"))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
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
		if err := decodeOAuthResponse(resp.Body, &info); err != nil {
			return "", err
		}

		if info.Name.DisplayName != "" {
			return info.Name.DisplayName, nil
		}
		return info.Email, nil
	case "google":
		req, err := http.NewRequestWithContext(ctx, "GET", config.UserInfoURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.httpClient.Do(req)
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
		if err := decodeOAuthResponse(resp.Body, &info); err != nil {
			return "", err
		}

		if info.Name != "" {
			return info.Name, nil
		}
		return info.Email, nil
	case "hidrive":
		req, err := http.NewRequestWithContext(ctx, "GET", config.UserInfoURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.httpClient.Do(req)
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
		if err := decodeOAuthResponse(resp.Body, &info); err != nil {
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
		req, err := http.NewRequestWithContext(ctx, "GET", config.UserInfoURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := c.httpClient.Do(req)
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
		if err := decodeOAuthResponse(resp.Body, &info); err != nil {
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
	return defaultOAuthClient.refreshToken(ctx, provider, refreshToken)
}

// ErrRefreshTokenInvalid indicates that the OAuth provider rejected credentials
// needed to refresh a token. Callers must treat it as permanent, not retryable.
var ErrRefreshTokenInvalid = errors.New("oauth: refresh token invalid or revoked")

// refreshError intentionally keeps provider error details out of Error(), so
// callers may log it safely. Its internal code is used only for classification.
type refreshError struct {
	status int
	code   string
}

func (e *refreshError) Error() string {
	return fmt.Sprintf("token refresh failed with status: %d", e.status)
}

func (e *refreshError) Unwrap() error {
	if e.status == http.StatusUnauthorized || (e.status == http.StatusBadRequest && isInvalidRefreshCode(e.code)) {
		return ErrRefreshTokenInvalid
	}
	return nil
}

// ErrorKind supplies the stable observability category without exposing the
// provider's error code. Other HTTP failures fall through to HTTPStatus.
func (e *refreshError) ErrorKind() string {
	if errors.Is(e, ErrRefreshTokenInvalid) {
		return "authentication"
	}
	return ""
}

// HTTPStatus lets generic observability preserve the status-derived category
// for transient provider errors such as 429 and 5xx.
func (e *refreshError) HTTPStatus() int { return e.status }

func isInvalidRefreshCode(code string) bool {
	switch strings.ToLower(code) {
	case "invalid_grant", "invalid_client", "invalid_token", "unauthorized_client":
		return true
	default:
		return false
	}
}

func (c *oauthClient) refreshToken(ctx context.Context, provider, refreshToken string) (*TokenResponse, error) {
	config, ok := c.configs[provider]
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

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// OAuth error bodies are controlled by an external provider and can include
		// credential hints. Do not surface their contents to callers, logs, or the
		// persisted migration status. Read only the standard error code to classify
		// an invalid refresh credential as permanent.
		var errResp struct {
			Error string `json:"error"`
		}
		// If the bounded decode fails, leave code empty and classify this as
		// retryable. A malformed or oversized provider error must not turn into a
		// terminal migration failure based on an untrusted response body.
		_ = decodeOAuthResponse(resp.Body, &errResp)
		return nil, &refreshError{status: resp.StatusCode, code: errResp.Error}
	}

	var tr TokenResponse
	if err := decodeOAuthResponse(resp.Body, &tr); err != nil {
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

func decodeOAuthResponse(body io.Reader, destination any) error {
	return json.NewDecoder(io.LimitReader(body, maxOAuthResponseBodyBytes)).Decode(destination)
}
