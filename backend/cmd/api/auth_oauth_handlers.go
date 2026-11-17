package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"backend/internal/oauth"
	"backend/internal/observability"
)

// handleOAuthAuth handles the OAuth authorization redirect.
func (api *accountAPI) handleOAuthAuth(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	logger := observability.Logger(r.Context()).With(slog.String("component", "oauth"))

	if provider == "" {
		writeError(w, http.StatusBadRequest, ErrOauthProviderMissing)
		return
	}

	origin := r.URL.Query().Get("origin")
	if origin == "" {
		if referer := r.Header.Get("Referer"); referer != "" {
			if parsed, err := url.Parse(referer); err == nil {
				origin = fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
			}
		}
	}
	if origin == "" {
		logger.WarnContext(r.Context(), "oauth_authorization_rejected", slog.String("operation", "authorize"), slog.String("error_kind", "validation"))
		writeError(w, http.StatusBadRequest, ErrOauthOriginMissing)
		return
	}
	if parsedOrigin, err := url.Parse(origin); err != nil || (parsedOrigin.Scheme != "http" && parsedOrigin.Scheme != "https") {
		logger.WarnContext(r.Context(), "oauth_authorization_rejected", slog.String("operation", "authorize"), slog.String("error_kind", "validation"))
		writeError(w, http.StatusBadRequest, ErrOauthOriginInvalid)
		return
	}
	if !allowedOrigins[origin] {
		logger.WarnContext(r.Context(), "oauth_authorization_rejected", slog.String("operation", "authorize"), slog.String("error_kind", "authorization"))
		writeError(w, http.StatusBadRequest, ErrOauthOriginUntrusted)
		return
	}

	purpose := r.URL.Query().Get("purpose")
	if purpose == "" {
		purpose = "login"
	}

	stateToken := generateRandomString(16)
	if stateToken == "" {
		logger.ErrorContext(r.Context(), "oauth_authorization_failed", slog.String("operation", "authorize"), slog.String("error_kind", "internal"))
		writeError(w, http.StatusInternalServerError, ErrOauthGenerationFailed)
		return
	}

	cookie := &http.Cookie{
		Name:     "oauth_state",
		Value:    stateToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		// SameSite=None is required because the OAuth provider redirects here
		// from a cross-site origin; Strict/Lax would suppress this CSRF cookie.
		SameSite: http.SameSiteNoneMode,
		MaxAge:   300,
	}
	http.SetCookie(w, cookie)

	stateParam := fmt.Sprintf("%s:%s:%s:%s", stateToken, provider, purpose, origin)

	redirectURI := api.getRedirectURI(r)
	authURL, err := oauth.GetAuthURL(provider, redirectURI, stateParam)
	if err != nil {
		logger.ErrorContext(r.Context(), "oauth_authorization_failed", slog.String("operation", "authorize"), slog.String("provider", provider), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		writeError(w, http.StatusInternalServerError, ErrOauthGenerationFailed)
		return
	}

	logger.InfoContext(r.Context(), "oauth_authorization_started", slog.String("operation", "authorize"), slog.String("provider", provider))
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

func (api *accountAPI) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	logger := observability.Logger(r.Context()).With(slog.String("component", "oauth"))

	if code == "" || state == "" {
		logger.WarnContext(r.Context(), "oauth_callback_rejected", slog.String("operation", "callback"), slog.String("error_kind", "validation"))
		api.renderOAuthResultHTML(w, "", "", "", 0, "", "", "http://localhost:5173", ErrOauthGenerationFailed)
		return
	}

	parts := strings.SplitN(state, ":", 4)
	if len(parts) < 3 {
		logger.WarnContext(r.Context(), "oauth_callback_rejected", slog.String("operation", "callback"), slog.String("error_kind", "validation"))
		api.renderOAuthResultHTML(w, "", "", "", 0, "", "", "http://localhost:5173", ErrOauthGenerationFailed)
		return
	}
	stateToken := parts[0]
	provider := parts[1]
	origin := parts[len(parts)-1]
	purpose := "login"
	if len(parts) >= 4 {
		purpose = parts[2]
	}

	if !allowedOrigins[origin] {
		logger.WarnContext(r.Context(), "oauth_callback_rejected", slog.String("operation", "callback"), slog.String("provider", provider), slog.String("error_kind", "authorization"))
		// Never reflect an untrusted origin into the callback document. Besides not
		// being a valid postMessage target, it would be embedded in an inline script.
		api.renderOAuthResultHTML(w, "", "", "", 0, "", "", "http://localhost:5173", ErrOauthOriginUntrusted)
		return
	}

	cookie, err := r.Cookie("oauth_state")
	if err != nil || cookie.Value == "" || cookie.Value != stateToken {
		logger.WarnContext(r.Context(), "oauth_callback_rejected", slog.String("operation", "callback"), slog.String("provider", provider), slog.String("error_kind", "authorization"))
		api.renderOAuthResultHTML(w, "", "", "", 0, "", "", origin, ErrOauthGenerationFailed)
		return
	}

	clearCookie := &http.Cookie{
		Name:     "oauth_state",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		// Must match the attributes used when setting oauth_state above.
		SameSite: http.SameSiteNoneMode,
		MaxAge:   -1,
	}
	http.SetCookie(w, clearCookie)

	redirectURI := api.getRedirectURI(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tokenResp, err := oauth.ExchangeCode(ctx, provider, code, redirectURI)
	if err != nil {
		logger.ErrorContext(r.Context(), "oauth_exchange_failed", slog.String("operation", "exchange_code"), slog.String("provider", provider), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		api.renderOAuthResultHTML(w, "", "", "", 0, "", "", origin, ErrOauthExchangeFailed)
		return
	}

	username, err := oauth.GetUserInfo(ctx, provider, tokenResp.AccessToken)
	if err != nil {
		logger.WarnContext(r.Context(), "oauth_user_info_unavailable", slog.String("operation", "get_user_info"), slog.String("provider", provider), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		username = "OAuth User"
	}

	logger.InfoContext(r.Context(), "oauth_callback_completed", slog.String("operation", "callback"), slog.String("provider", provider))
	api.renderOAuthResultHTML(w, provider, tokenResp.AccessToken, tokenResp.RefreshToken, tokenResp.ExpiresIn, username, purpose, origin)
}

func (api *accountAPI) renderOAuthResultHTML(w http.ResponseWriter, provider, token, refreshToken string, expiresIn int, username, purpose, targetOrigin string, errorCode ...APIErrorCode) {
	// targetOrigin is embedded in the callback's inline script. Keep this
	// sink-level guard even though callback callers validate the state origin,
	// so future call paths cannot reflect an untrusted value into JavaScript.
	if !allowedOrigins[targetOrigin] {
		targetOrigin = "http://localhost:5173"
	}

	var errCode string
	if len(errorCode) > 0 {
		errCode = string(errorCode[0])
	}

	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	nonce := base64.StdEncoding.EncodeToString(nonceBytes)
	w.Header().Set("Content-Security-Policy", "script-src 'nonce-"+nonce+"'; frame-ancestors 'none'; object-src 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Keep request-influenced data out of executable JavaScript. The browser
	// reads this inert JSON element from a fixed script template below.
	// json.Marshal escapes '<', so data cannot terminate the script element.
	oauthResultData := struct {
		ErrorCode    string `json:"errorCode"`
		TargetOrigin string `json:"targetOrigin"`
		Provider     string `json:"provider"`
		Purpose      string `json:"purpose"`
		Token        string `json:"token"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int    `json:"expiresIn"`
		Username     string `json:"username"`
	}{errCode, targetOrigin, provider, purpose, token, refreshToken, expiresIn, username}
	encodedData, err := json.Marshal(oauthResultData)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	statusMessage := "<h3>Authorization Successful</h3><p>You can close this window now.</p>"
	if errCode != "" {
		statusMessage = "<h3 style='color: #ef4444;'>Authorization Failed</h3><p>Authorization could not be completed.</p>"
	}

	fmt.Fprintf(w, `
		<!DOCTYPE html>
		<html>
		<head>
			<title>Authorization Status</title>
			<style>
				body {
					font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
					display: flex;
					align-items: center;
					justify-content: center;
					height: 100vh;
					margin: 0;
					background-color: #f8fafc;
					color: #334155;
				}
				.card {
					background: white;
					padding: 2rem;
					border-radius: 8px;
					box-shadow: 0 4px 6px -1px rgb(0 0 0 / 0.1);
					text-align: center;
				}
			</style>
		</head>
		<body>
			<div class="card">
				%s
			</div>
			<script id="oauth-result" type="application/json" nonce="%s">`, statusMessage, nonce)
	_, _ = w.Write(encodedData)
	fmt.Fprintf(w, `</script>
			<script nonce="%s">
				const oauthResult = JSON.parse(document.getElementById("oauth-result").textContent);
				if (oauthResult.errorCode) {
					try {
						if (!window.opener) {
							console.error("window.opener is null on error page!");
						} else {
							window.opener.postMessage({ type: "oauth-error", error_code: oauthResult.errorCode }, oauthResult.targetOrigin);
						}
					} catch (e) {
						console.error("Failed to post oauth-error:", e);
					}
					setTimeout(() => { window.close(); }, 1000);
				} else {
					try {
						if (!window.opener) {
							const errMsg = document.createElement("p");
							errMsg.style.color = "red";
							errMsg.innerText = "Fehler: window.opener ist null.";
							document.querySelector(".card").appendChild(errMsg);
						} else {
							window.opener.postMessage({
								type: "oauth-success",
								provider: oauthResult.provider,
								purpose: oauthResult.purpose,
								token: oauthResult.token,
								refreshToken: oauthResult.refreshToken,
								expiresIn: oauthResult.expiresIn,
								username: oauthResult.username
							}, oauthResult.targetOrigin);
							window.close();
						}
					} catch (e) {
						console.error("Failed to post oauth-success:", e);
					}
				}
			</script>
		</body>
		</html>
	`, nonce)
}
