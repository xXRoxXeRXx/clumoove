package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/observability"
)

// handleOAuthAuth handles the OAuth authorization redirect.
func (s *APIServer) handleOAuthAuth(w http.ResponseWriter, r *http.Request) {
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

	redirectURI := s.getRedirectURI(r)
	authURL, err := oauth.GetAuthURL(provider, redirectURI, stateParam)
	if err != nil {
		logger.ErrorContext(r.Context(), "oauth_authorization_failed", slog.String("operation", "authorize"), slog.String("provider", provider), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		writeError(w, http.StatusInternalServerError, ErrOauthGenerationFailed)
		return
	}

	logger.InfoContext(r.Context(), "oauth_authorization_started", slog.String("operation", "authorize"), slog.String("provider", provider))
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

func (s *APIServer) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	logger := observability.Logger(r.Context()).With(slog.String("component", "oauth"))

	if code == "" || state == "" {
		logger.WarnContext(r.Context(), "oauth_callback_rejected", slog.String("operation", "callback"), slog.String("error_kind", "validation"))
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", "http://localhost:5173", ErrOauthGenerationFailed)
		return
	}

	parts := strings.SplitN(state, ":", 4)
	if len(parts) < 3 {
		logger.WarnContext(r.Context(), "oauth_callback_rejected", slog.String("operation", "callback"), slog.String("error_kind", "validation"))
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", "http://localhost:5173", ErrOauthGenerationFailed)
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
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", "http://localhost:5173", ErrOauthOriginUntrusted)
		return
	}

	cookie, err := r.Cookie("oauth_state")
	if err != nil || cookie.Value == "" || cookie.Value != stateToken {
		logger.WarnContext(r.Context(), "oauth_callback_rejected", slog.String("operation", "callback"), slog.String("provider", provider), slog.String("error_kind", "authorization"))
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", origin, ErrOauthGenerationFailed)
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

	redirectURI := s.getRedirectURI(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tokenResp, err := oauth.ExchangeCode(ctx, provider, code, redirectURI)
	if err != nil {
		logger.ErrorContext(r.Context(), "oauth_exchange_failed", slog.String("operation", "exchange_code"), slog.String("provider", provider), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", origin, ErrOauthExchangeFailed)
		return
	}

	username, err := oauth.GetUserInfo(ctx, provider, tokenResp.AccessToken)
	if err != nil {
		logger.WarnContext(r.Context(), "oauth_user_info_unavailable", slog.String("operation", "get_user_info"), slog.String("provider", provider), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		username = "OAuth User"
	}

	logger.InfoContext(r.Context(), "oauth_callback_completed", slog.String("operation", "callback"), slog.String("provider", provider))
	s.renderOAuthResultHTML(w, provider, tokenResp.AccessToken, tokenResp.RefreshToken, tokenResp.ExpiresIn, username, purpose, origin)
}

func (s *APIServer) renderOAuthResultHTML(w http.ResponseWriter, provider, token, refreshToken string, expiresIn int, username, purpose, targetOrigin string, errorCode ...APIErrorCode) {
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

func (s *APIServer) RunOAuthRotationDaemon(ctx context.Context) {
	logger := oauthRotationLogger(ctx)
	logger.InfoContext(ctx, "oauth_rotation_daemon_started")
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.InfoContext(ctx, "oauth_rotation_daemon_stopped")
			return
		case <-ticker.C:
			s.rotateExpiringOAuthTokens(ctx)
		}
	}
}

func oauthRotationLogger(ctx context.Context) *slog.Logger {
	return observability.Logger(ctx).With(slog.String("component", "oauth_rotation"))
}

// shouldSkipOAuthTokenRotation checks whether a token's expiry still lies sufficiently
// far in the future (at least 5 minutes) that rotation by the background daemon should
// be skipped, for example because a worker recently performed an inline refresh.
func shouldSkipOAuthTokenRotation(expiresAt time.Time, now time.Time) bool {
	if expiresAt.IsZero() {
		return false
	}
	return now.Before(expiresAt.Add(-5 * time.Minute))
}

// isOAuthRotationAlreadyResolved detects whether a concurrent worker has already
// persisted a new refresh token in the database, meaning that an invalid_grant /
// rejected refresh token was caused by rotation race rather than real revocation.
func isOAuthRotationAlreadyResolved(currentRefreshEnc, freshRefreshEnc string) bool {
	return freshRefreshEnc != "" && freshRefreshEnc != currentRefreshEnc
}

func (s *APIServer) rotateExpiringOAuthTokens(ctx context.Context) {
	logger := oauthRotationLogger(ctx)
	expiringMig, err := db.GetExpiringOAuthMigrations(s.db)
	if err != nil {
		logger.ErrorContext(ctx, "oauth_rotation_scan_failed", slog.String("job_type", "migration"), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
	}

	expiringSync, errSync := db.GetExpiringOAuthSyncJobs(s.db)
	if errSync != nil {
		logger.ErrorContext(ctx, "oauth_rotation_scan_failed", slog.String("job_type", "sync"), observability.Error(errSync), slog.String("error_kind", observability.ErrorKind(errSync)))
	}

	expiringRestore, errRestore := db.GetExpiringOAuthRestoreRuns(s.db)
	if errRestore != nil {
		logger.ErrorContext(ctx, "oauth_rotation_scan_failed", slog.String("job_type", "restore"), observability.Error(errRestore), slog.String("error_kind", observability.ErrorKind(errRestore)))
	}

	if err == nil {
		for _, entry := range expiringMig {
			entry := entry
			s.rotateExpiringOAuthJob(ctx, logger, expiringOAuthJobSpec{
				jobType:  "migration",
				jobID:    entry.MigrationID,
				role:     entry.Role,
				provider: entry.Provider,
				loadSnapshot: func(loadCtx context.Context) (string, string, time.Time, bool, string, error) {
					mig, loadErr := db.GetMigrationContext(loadCtx, s.db, entry.MigrationID)
					if loadErr != nil {
						return "", "", time.Time{}, false, "", loadErr
					}
					active := mig.Status == "RUNNING" || mig.Status == "INDEXING"
					var refreshEnc string
					var expiresAt time.Time
					if entry.Role == "source" {
						refreshEnc = mig.SourceRefreshTokenEncrypted.String
						if mig.SourceTokenExpiresAt.Valid {
							expiresAt = mig.SourceTokenExpiresAt.Time
						}
					} else {
						refreshEnc = mig.TargetRefreshTokenEncrypted.String
						if mig.TargetTokenExpiresAt.Valid {
							expiresAt = mig.TargetTokenExpiresAt.Time
						}
					}
					return mig.UserID.String, refreshEnc, expiresAt, active, mig.Status, nil
				},
				persist: func(persistCtx context.Context, accessEnc, refreshEnc string, expiresAt time.Time, expectedRefreshEnc string) error {
					return db.UpdateMigrationOAuthTokensContext(persistCtx, s.db, db.OAuthTokenUpdate{
						MigrationID:           entry.MigrationID,
						Role:                  entry.Role,
						AccessTokenEncrypted:  accessEnc,
						RefreshTokenEncrypted: refreshEnc,
						ExpiresAt:             expiresAt,
					}, expectedRefreshEnc)
				},
				markFailed: func() {
					errMsg := fmt.Sprintf("OAuth token refresh failed (%s)", entry.Provider)
					_ = db.UpdateMigrationStatus(s.db, entry.MigrationID, "FAILED", &errMsg)
				},
			})
		}
	}

	if errSync == nil {
		for _, entry := range expiringSync {
			entry := entry
			s.rotateExpiringOAuthJob(ctx, logger, expiringOAuthJobSpec{
				jobType:  "sync",
				jobID:    entry.SyncJobID,
				role:     entry.Role,
				provider: entry.Provider,
				loadSnapshot: func(loadCtx context.Context) (string, string, time.Time, bool, string, error) {
					job, loadErr := db.GetSyncJobContext(loadCtx, s.db, entry.SyncJobID)
					if loadErr != nil {
						return "", "", time.Time{}, false, "", loadErr
					}
					active := job.Status == "RUNNING" || job.Status == "ACTIVE"
					var refreshEnc string
					var expiresAt time.Time
					if entry.Role == "source" {
						refreshEnc = job.SourceRefreshTokenEncrypted.String
						if job.SourceTokenExpiresAt.Valid {
							expiresAt = job.SourceTokenExpiresAt.Time
						}
					} else {
						refreshEnc = job.TargetRefreshTokenEncrypted.String
						if job.TargetTokenExpiresAt.Valid {
							expiresAt = job.TargetTokenExpiresAt.Time
						}
					}
					return job.UserID, refreshEnc, expiresAt, active, job.Status, nil
				},
				persist: func(persistCtx context.Context, accessEnc, refreshEnc string, expiresAt time.Time, expectedRefreshEnc string) error {
					return db.UpdateSyncJobOAuthTokensContext(persistCtx, s.db, entry.SyncJobID, entry.Role, accessEnc, refreshEnc, expiresAt, expectedRefreshEnc)
				},
				markFailed: func() {
					errMsg := fmt.Sprintf("OAuth token refresh failed (%s)", entry.Provider)
					_ = db.UpdateSyncJobStatus(s.db, entry.SyncJobID, "FAILED", &errMsg)
				},
			})
		}
	}

	if errRestore == nil {
		for _, entry := range expiringRestore {
			entry := entry
			s.rotateExpiringOAuthJob(ctx, logger, expiringOAuthJobSpec{
				jobType:  "restore",
				jobID:    entry.RestoreRunID,
				role:     "target",
				provider: entry.Provider,
				loadSnapshot: func(loadCtx context.Context) (string, string, time.Time, bool, string, error) {
					_, refreshEnc, expiresAt, loadErr := db.GetRestoreRunCredentialSnapshotContext(loadCtx, s.db, entry.RestoreRunID)
					if loadErr != nil {
						return entry.UserID, "", time.Time{}, false, "", loadErr
					}
					var exp time.Time
					if expiresAt.Valid {
						exp = expiresAt.Time
					}
					return entry.UserID, refreshEnc.String, exp, true, "", nil
				},
				persist: func(persistCtx context.Context, accessEnc, refreshEnc string, expiresAt time.Time, expectedRefreshEnc string) error {
					return db.UpdateRestoreRunOAuthTokens(persistCtx, s.db, entry.RestoreRunID, accessEnc, refreshEnc, expiresAt, expectedRefreshEnc)
				},
			})
		}
	}
}

type expiringOAuthJobSpec struct {
	jobType      string
	jobID        string
	role         string
	provider     string
	loadSnapshot func(ctx context.Context) (userID, refreshEnc string, expiresAt time.Time, active bool, status string, err error)
	persist      func(ctx context.Context, accessEnc, refreshEnc string, expiresAt time.Time, expectedRefreshEnc string) error
	markFailed   func()
}

func (s *APIServer) rotateExpiringOAuthJob(ctx context.Context, logger *slog.Logger, spec expiringOAuthJobSpec) {
	userID, currentRefreshEnc, currentExpiresAt, active, status, err := spec.loadSnapshot(ctx)
	if err != nil {
		logger.ErrorContext(ctx, "oauth_rotation_fetch_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}
	if !active {
		logger.DebugContext(ctx, fmt.Sprintf("oauth_rotation_%s_not_active", spec.jobType), slog.String("job_id", spec.jobID), slog.String("status", status))
		return
	}
	if currentRefreshEnc == "" {
		return
	}
	if shouldSkipOAuthTokenRotation(currentExpiresAt, time.Now()) {
		logger.DebugContext(ctx, "oauth_rotation_already_fresh", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role))
		return
	}

	refreshToken, err := crypto.DecryptWithDomain(currentRefreshEnc, s.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		logger.ErrorContext(ctx, "oauth_rotation_decrypt_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}
	defer crypto.ZeroString(&refreshToken)

	var locker oauth.TokenLocker
	if s.queue != nil {
		locker = s.queue
	}

	var rotatedExpiresAt time.Time
	res, err := oauth.RefreshWithRotationLock(ctx, oauth.RotationRefreshRequest{
		DB:            s.db,
		Cache:         s.rotationCache(),
		Locker:        locker,
		EncryptionKey: s.encryptionKey,
		UserID:        userID,
		Provider:      spec.provider,
		RefreshToken:  refreshToken,
		Refresh: func(refreshCtx context.Context, prov, tok string) (*oauth.TokenResponse, error) {
			tCtx, cancel := context.WithTimeout(refreshCtx, 15*time.Second)
			defer cancel()
			return oauth.RefreshToken(tCtx, prov, tok)
		},
		OnLocked: func(lockCtx context.Context, target string) (string, bool, string, error) {
			_, latestRefreshEnc, latestExpiresAt, latestActive, _, lerr := spec.loadSnapshot(lockCtx)
			if lerr != nil {
				return "", false, "", nil
			}
			if !latestActive || latestRefreshEnc == "" || shouldSkipOAuthTokenRotation(latestExpiresAt, time.Now()) {
				return "", true, "", nil
			}
			if latestRefreshEnc != currentRefreshEnc {
				currentRefreshEnc = latestRefreshEnc
				updatedPlain, decErr := crypto.DecryptWithDomain(currentRefreshEnc, s.encryptionKey, crypto.DomainOAuthRefreshToken)
				if decErr != nil {
					return "", false, "", decErr
				}
				if updatedPlain != "" && updatedPlain != target {
					refreshToken = updatedPlain
					return "", false, updatedPlain, nil
				}
			}
			return "", false, "", nil
		},
		Persist: func(persistCtx context.Context, _ string, tokenResp *oauth.TokenResponse) (string, bool, error) {
			newAccessEnc, err := crypto.EncryptWithDomain(tokenResp.AccessToken, s.encryptionKey, crypto.DomainOAuthAccessToken)
			if err != nil {
				logger.ErrorContext(persistCtx, "oauth_rotation_encrypt_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("token_type", "access"), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
				return "", false, err
			}
			newRefreshEnc, err := crypto.EncryptWithDomain(tokenResp.RefreshToken, s.encryptionKey, crypto.DomainOAuthRefreshToken)
			if err != nil {
				logger.ErrorContext(persistCtx, "oauth_rotation_encrypt_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("token_type", "refresh"), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
				return "", false, err
			}
			expiresIn := tokenResp.ExpiresIn
			if expiresIn <= 0 {
				expiresIn = 3600
			}
			rotatedExpiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
			err = spec.persist(persistCtx, newAccessEnc, newRefreshEnc, rotatedExpiresAt, currentRefreshEnc)
			if errors.Is(err, db.ErrOAuthTokenConflict) {
				logger.InfoContext(persistCtx, "oauth_rotation_update_conflict", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role))
				return "", true, nil
			}
			if err != nil {
				logger.ErrorContext(persistCtx, "oauth_rotation_persist_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
				return "", false, err
			}
			return "", false, nil
		},
	})
	if err != nil {
		logger.ErrorContext(ctx, "oauth_rotation_refresh_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role), slog.String("provider", spec.provider), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		if errors.Is(err, oauth.ErrRefreshTokenInvalid) && spec.markFailed != nil {
			if _, freshRefreshEnc, _, _, _, ferr := spec.loadSnapshot(ctx); ferr == nil && isOAuthRotationAlreadyResolved(currentRefreshEnc, freshRefreshEnc) {
				logger.InfoContext(ctx, "oauth_rotation_already_resolved", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role))
				return
			}
			spec.markFailed()
		}
		return
	}
	if res.Adopted {
		return
	}
	logger.InfoContext(ctx, "oauth_rotation_completed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role), slog.String("provider", spec.provider), slog.Time("expires_at", rotatedExpiresAt))
}
