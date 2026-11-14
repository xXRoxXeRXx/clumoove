package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/storage"
)

type syncBrowseCredentials struct {
	provider    string
	url         string
	username    string
	password    string
	megaSession storage.MegaSession
}

func (c *syncBrowseCredentials) clear() {
	crypto.ZeroString(&c.password)
	clear(c.megaSession.MasterKey)
}

type openedSyncEndpoint[T any] struct {
	Client   T
	Provider string
	closeFn  func()
}

type syncOAuthProviderRefreshError struct{ err error }

func (e syncOAuthProviderRefreshError) Error() string { return e.err.Error() }
func (e syncOAuthProviderRefreshError) Unwrap() error { return e.err }

func (e *openedSyncEndpoint[T]) Close() {
	if e != nil && e.closeFn != nil {
		e.closeFn()
	}
}

func openSyncJobEndpoint[T any](api *syncAPI, ctx context.Context, id, userID, role string, job *db.SyncJob) (*openedSyncEndpoint[T], error) {
	profileID := job.SourceProfileID
	if role == "target" {
		profileID = job.TargetProfileID
	}
	if profileID.Valid {
		conn, err := api.profiles.OpenOwned(ctx, userID, profileID.String)
		if err != nil {
			return nil, err
		}
		if err := conn.SyncJobOAuthTokens(id, role, syncJobRefreshTokenEncrypted(role, job)); err != nil {
			conn.Close()
			return nil, err
		}
		client, err := narrowProvider[T](conn.Provider())
		if err != nil {
			conn.Close()
			return nil, err
		}
		return &openedSyncEndpoint[T]{Client: client, Provider: conn.Profile().Provider, closeFn: conn.Close}, nil
	}

	// Profile-less jobs predate reusable profiles and retain their encrypted
	// credential snapshot for compatibility.
	creds := syncBrowseCredentials{
		provider: job.SourceProvider,
		url:      job.SourceURL,
		username: job.SourceUsername,
	}
	passwordEncrypted := job.SourcePasswordEncrypted
	refreshEncrypted := job.SourceRefreshTokenEncrypted.String
	expiresAt := job.SourceTokenExpiresAt
	if role == "target" {
		creds.provider = job.TargetProvider
		creds.url = job.TargetURL
		creds.username = job.TargetUsername
		passwordEncrypted = job.TargetPasswordEncrypted
		refreshEncrypted = job.TargetRefreshTokenEncrypted.String
		expiresAt = job.TargetTokenExpiresAt
	}

	if oauth.IsProvider(creds.provider) && expiresAt.Valid && !time.Now().Before(expiresAt.Time.Add(-2*time.Minute)) && refreshEncrypted != "" {
		expectedRefreshEnc := refreshEncrypted
		refreshToken, err := crypto.DecryptWithDomain(expectedRefreshEnc, api.encryptionKey, crypto.DomainOAuthRefreshToken)
		if err != nil {
			return nil, err
		}
		defer crypto.ZeroString(&refreshToken)
		if refreshToken != "" {
			res, rerr := oauth.RefreshWithRotationLock(ctx, oauth.RotationRefreshRequest{
				DB:            api.db,
				Cache:         api.oauthRotations,
				Locker:        api.oauthLocker,
				EncryptionKey: api.encryptionKey,
				UserID:        userID,
				Provider:      creds.provider,
				RefreshToken:  refreshToken,
				Refresh: func(refreshCtx context.Context, prov, tok string) (*oauth.TokenResponse, error) {
					token, rerr := oauth.RefreshToken(refreshCtx, prov, tok)
					if rerr != nil {
						return nil, syncOAuthProviderRefreshError{err: rerr}
					}
					return token, nil
				},
				OnLocked: func(lockCtx context.Context, target string) (string, bool, string, error) {
					latestJob, lerr := db.GetSyncJobContext(lockCtx, api.db, id)
					if lerr != nil {
						return "", false, "", nil
					}
					latestPassEnc := latestJob.SourcePasswordEncrypted
					latestRefreshEnc := latestJob.SourceRefreshTokenEncrypted.String
					latestExpiresAt := latestJob.SourceTokenExpiresAt
					if role == "target" {
						latestPassEnc = latestJob.TargetPasswordEncrypted
						latestRefreshEnc = latestJob.TargetRefreshTokenEncrypted.String
						latestExpiresAt = latestJob.TargetTokenExpiresAt
					}
					if latestExpiresAt.Valid && time.Now().Before(latestExpiresAt.Time.Add(-2*time.Minute)) && latestPassEnc != "" {
						if plain, derr := crypto.DecryptWithDomain(latestPassEnc, api.encryptionKey, crypto.DomainOAuthAccessToken); derr == nil {
							return plain, true, "", nil
						}
					}
					if latestRefreshEnc != "" && latestRefreshEnc != expectedRefreshEnc {
						expectedRefreshEnc = latestRefreshEnc
						updatedPlain, decErr := crypto.DecryptWithDomain(expectedRefreshEnc, api.encryptionKey, crypto.DomainOAuthRefreshToken)
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
				Persist: func(persistCtx context.Context, _ string, token *oauth.TokenResponse) (string, bool, error) {
					accessEncrypted, accessErr := crypto.EncryptWithDomain(token.AccessToken, api.encryptionKey, crypto.DomainOAuthAccessToken)
					if accessErr != nil {
						return "", false, accessErr
					}
					newRefreshEncrypted, refreshErr := crypto.EncryptWithDomain(token.RefreshToken, api.encryptionKey, crypto.DomainOAuthRefreshToken)
					if refreshErr != nil {
						return "", false, refreshErr
					}
					expiresIn := token.ExpiresIn
					if expiresIn <= 0 {
						expiresIn = 3600
					}
					err := db.UpdateSyncJobOAuthTokensContext(persistCtx, api.db, id, role, accessEncrypted, newRefreshEncrypted, time.Now().Add(time.Duration(expiresIn)*time.Second), expectedRefreshEnc)
					if errors.Is(err, db.ErrOAuthTokenConflict) {
						if latestJob, lerr := db.GetSyncJobContext(persistCtx, api.db, id); lerr == nil {
							latestPassEnc := latestJob.SourcePasswordEncrypted
							if role == "target" {
								latestPassEnc = latestJob.TargetPasswordEncrypted
							}
							if plain, derr := crypto.DecryptWithDomain(latestPassEnc, api.encryptionKey, crypto.DomainOAuthAccessToken); derr == nil {
								return plain, true, nil
							}
						}
					}
					return "", false, err
				},
			})
			if rerr == nil {
				if res.Adopted {
					creds.password = res.AdoptedAccess
				} else if res.Token != nil {
					creds.password = res.Token.AccessToken
				}
			} else {
				var providerErr syncOAuthProviderRefreshError
				if !errors.As(rerr, &providerErr) {
					creds.clear()
					return nil, rerr
				}
			}
		}
	}

	if creds.password == "" && passwordEncrypted != "" {
		password, err := crypto.DecryptWithDomain(passwordEncrypted, api.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(creds.provider)))
		if err != nil {
			creds.clear()
			return nil, err
		}
		creds.password = password
	}
	rawClient, err := api.profiles.openDirectProvider(ctx, userID, creds.provider, creds.url, creds.username, creds.password, creds.megaSession)
	creds.clear()
	if err != nil {
		return nil, err
	}
	client, err := narrowProvider[T](rawClient)
	if err != nil {
		return nil, err
	}
	return &openedSyncEndpoint[T]{Client: client, Provider: creds.provider, closeFn: func() { _ = rawClient.Close() }}, nil
}

func syncJobRefreshTokenEncrypted(role string, job *db.SyncJob) string {
	if role == "target" {
		return job.TargetRefreshTokenEncrypted.String
	}
	return job.SourceRefreshTokenEncrypted.String
}

type createSyncRequest struct {
	SourceProfileID      string   `json:"source_profile_id,omitempty"`
	TargetProfileID      string   `json:"target_profile_id,omitempty"`
	SourceURL            string   `json:"source_url"`
	SourceUsername       string   `json:"source_username"`
	SourcePassword       string   `json:"source_password"`
	SourceRefreshToken   string   `json:"source_refresh_token,omitempty"`
	SourceTokenExpiresAt *string  `json:"source_token_expires_at,omitempty"`
	TargetURL            string   `json:"target_url"`
	TargetUsername       string   `json:"target_username"`
	TargetPassword       string   `json:"target_password"`
	TargetRefreshToken   string   `json:"target_refresh_token,omitempty"`
	TargetTokenExpiresAt *string  `json:"target_token_expires_at,omitempty"`
	SourceProvider       string   `json:"source_provider"`
	TargetProvider       string   `json:"target_provider"`
	Direction            string   `json:"direction"`
	ConflictStrategy     string   `json:"conflict_strategy"`
	DeletePropagation    bool     `json:"delete_propagation"`
	IntervalMinutes      int      `json:"interval_minutes"`
	Threads              int      `json:"threads"`
	BandwidthLimitMbps   int      `json:"bandwidth_limit_mbps"`
	TargetDir            string   `json:"target_dir"`
	SelectedPaths        []string `json:"selected_paths"`
}

func (api *syncAPI) handleListSyncs(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	jobs, err := db.GetSyncJobsForUserContext(r.Context(), api.db, userID)
	if err != nil {
		api.logf(r, "Error fetching sync jobs for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if jobs == nil {
		jobs = []db.SyncJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (api *syncAPI) handleCreateSync(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "migration-sync-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	var req createSyncRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	// Merge any referenced reusable connection profiles into the request.
	src, err := api.profiles.SnapshotRequest(r.Context(), userID, requestConnectionInput{
		ProfileID:       req.SourceProfileID,
		DefaultProvider: "nextcloud",
		NormalizeURL:    true,
		Provider:        req.SourceProvider,
		URL:             req.SourceURL,
		Username:        req.SourceUsername,
		Password:        req.SourcePassword,
		RefreshToken:    req.SourceRefreshToken,
		TokenExpiresAt:  parseSyncTokenExpiry(req.SourceTokenExpiresAt),
	})
	if err != nil {
		writeRequestSnapshotErrorWith(w, r, api.logf, "handleCreateSync: failed to load source profile", err)
		return
	}

	tgt, err := api.profiles.SnapshotRequest(r.Context(), userID, requestConnectionInput{
		ProfileID:       req.TargetProfileID,
		DefaultProvider: "nextcloud",
		NormalizeURL:    true,
		Provider:        req.TargetProvider,
		URL:             req.TargetURL,
		Username:        req.TargetUsername,
		Password:        req.TargetPassword,
		RefreshToken:    req.TargetRefreshToken,
		TokenExpiresAt:  parseSyncTokenExpiry(req.TargetTokenExpiresAt),
	})
	if err != nil {
		writeRequestSnapshotErrorWith(w, r, api.logf, "handleCreateSync: failed to load target profile", err)
		return
	}

	if oauth.IsProvider(src.Provider) && !src.HasRefreshToken {
		writeValidationError(w, ErrRefreshTokenMissing)
		return
	}
	if oauth.IsProvider(tgt.Provider) && !tgt.HasRefreshToken {
		writeValidationError(w, ErrRefreshTokenMissing)
		return
	}
	if src.Provider == "immich" || tgt.Provider == "immich" {
		writeError(w, http.StatusBadRequest, ErrImmichSyncUnsupported)
		return
	}

	if req.Direction == "" {
		req.Direction = "one_way"
	}
	if !validSyncDirection(req.Direction) {
		writeValidationError(w, ErrSyncDirectionInvalid)
		return
	}
	if req.ConflictStrategy == "" {
		req.ConflictStrategy = "OVERWRITE"
	}
	if !db.ValidConflictStrategy(req.ConflictStrategy) {
		writeError(w, http.StatusBadRequest, ErrConflictStrategyInvalid)
		return
	}
	if req.IntervalMinutes <= 0 {
		req.IntervalMinutes = 15
	}
	if req.Threads <= 0 || req.Threads > 16 {
		req.Threads = 8
	}
	if req.BandwidthLimitMbps < 0 {
		req.BandwidthLimitMbps = 0
	} else if req.BandwidthLimitMbps > 1000 {
		req.BandwidthLimitMbps = 1000
	}
	if req.TargetDir == "" {
		req.TargetDir = "/"
	}

	// Validate provider URLs for host-based providers upfront
	if err := storage.ValidateProviderURL(src.Provider, src.URL); err != nil {
		writeError(w, http.StatusBadRequest, ErrProfileURLRequired)
		return
	}
	if err := storage.ValidateProviderURL(tgt.Provider, tgt.URL); err != nil {
		writeError(w, http.StatusBadRequest, ErrProfileURLRequired)
		return
	}

	job := &db.SyncJob{
		UserID:                       userID,
		SourceProfileID:              src.ProfileID,
		TargetProfileID:              tgt.ProfileID,
		SourceURL:                    src.URL,
		SourceUsername:               src.Username,
		SourcePasswordEncrypted:      src.Creds.PasswordEncrypted,
		SourceRefreshTokenEncrypted:  src.Creds.RefreshTokenEncrypted,
		SourceTokenExpiresAt:         src.TokenExpiresAt,
		SourceMegaSessionIDEncrypted: src.Creds.MegaSessionIDEncrypted,
		SourceMegaMasterKeyEncrypted: src.Creds.MegaMasterKeyEncrypted,
		TargetURL:                    tgt.URL,
		TargetUsername:               tgt.Username,
		TargetPasswordEncrypted:      tgt.Creds.PasswordEncrypted,
		TargetRefreshTokenEncrypted:  tgt.Creds.RefreshTokenEncrypted,
		TargetTokenExpiresAt:         tgt.TokenExpiresAt,
		TargetMegaSessionIDEncrypted: tgt.Creds.MegaSessionIDEncrypted,
		TargetMegaMasterKeyEncrypted: tgt.Creds.MegaMasterKeyEncrypted,
		SourceProvider:               src.Provider,
		TargetProvider:               tgt.Provider,
		Direction:                    req.Direction,
		ConflictStrategy:             req.ConflictStrategy,
		DeletePropagation:            req.DeletePropagation,
		IntervalMinutes:              req.IntervalMinutes,
		Threads:                      req.Threads,
		BandwidthLimitMbps:           req.BandwidthLimitMbps,
		Status:                       "IDLE",
		TargetDir:                    req.TargetDir,
		SelectedPaths:                req.SelectedPaths,
	}

	// Create a duration-based linked schedule. Cron's minute field is limited to
	// 0-59, so values such as a 90-minute interval cannot be represented by a
	// cron expression. The scheduler reads interval_minutes from this sync job
	// and advances next_run_at relative to the current time.
	nextRun := time.Now().Add(time.Duration(req.IntervalMinutes) * time.Minute)

	sched := &db.Schedule{
		UserID:         userID,
		TaskType:       "sync",
		CronExpression: sql.NullString{},
		NextRunAt:      sql.NullTime{Time: nextRun, Valid: true},
		IsActive:       true,
	}
	jobID, err := db.CreateSyncJobAndSchedule(api.db, job, sched)
	if err != nil {
		api.logf(r, "Failed to create sync job and schedule: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	api.writeAudit(r, db.AuditSyncCreated, jobID, userID, map[string]interface{}{
		"source_provider": src.Provider,
		"target_provider": tgt.Provider,
		"direction":       req.Direction,
	})

	writeJSON(w, http.StatusOK, map[string]string{"id": jobID})
}

func validSyncDirection(direction string) bool {
	return direction == "one_way" || direction == "two_way"
}

func (api *syncAPI) handleGetSyncStatus(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}

	if !api.requireSyncOwnership(w, r, id, userID) {
		return
	}

	job, err := db.GetSyncJobContext(r.Context(), api.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}

	if job.Status == "RUNNING" || job.Status == "INDEXING" {
		if activeFiles, err := db.GetActiveSyncTaskPaths(api.db, r.Context(), id); err == nil {
			job.ActiveFiles = activeFiles
		}
	} else if job.Status == "VERIFYING" {
		if verifyingFiles, err := db.GetVerifyingSyncTaskPaths(api.db, r.Context(), id); err == nil {
			job.ActiveFiles = verifyingFiles
		}
	}

	writeJSON(w, http.StatusOK, job)
}

func (api *syncAPI) requireSyncPass(w http.ResponseWriter, r *http.Request, id string) bool {
	if api.syncPass != nil {
		return true
	}
	api.logf(r, "[Sync] SyncPass unavailable for sync job %s", id)
	writeError(w, http.StatusInternalServerError, ErrInternalError)
	return false
}

func (api *syncAPI) handleStartSync(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "migration-sync-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}

	if !api.requireSyncOwnership(w, r, id, userID) || !api.requireSyncPass(w, r, id) {
		return
	}

	result, err := api.syncPass.Start(api.backgroundCtx, id)
	if err != nil {
		api.logf(r, "[Sync] Failed to claim sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !result.Started {
		if result.Status == "" {
			writeError(w, http.StatusNotFound, ErrSyncNotFound)
			return
		}
		if result.Status == "RUNNING" || result.Status == "INDEXING" || result.Status == "VERIFYING" {
			writeError(w, http.StatusConflict, ErrSyncAlreadyRunning)
			return
		}
		writeError(w, http.StatusConflict, ErrSyncInvalidState)
		return
	}

	api.writeAudit(r, db.AuditSyncStarted, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (api *syncAPI) handlePauseSync(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")

	if !api.requireSyncOwnership(w, r, id, userID) || !api.requireSyncPass(w, r, id) {
		return
	}

	// A paused sync abandons the current pass. Resume starts a freshly indexed
	// pass; it never attempts to continue a coordinator that was cancelled.
	paused, err := api.syncPass.Pause(r.Context(), id)
	if err != nil {
		api.logf(r, "Failed to pause sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !paused {
		writeError(w, http.StatusConflict, ErrSyncInvalidState)
		return
	}
	api.writeAudit(r, db.AuditSyncPaused, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (api *syncAPI) handleResumeSync(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")

	if !api.requireSyncOwnership(w, r, id, userID) || !api.requireSyncPass(w, r, id) {
		return
	}

	result, err := api.syncPass.Resume(r.Context(), api.backgroundCtx, id)
	if err != nil {
		api.logf(r, "Failed to resume sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !result.Resumed {
		writeError(w, http.StatusConflict, ErrSyncInvalidState)
		return
	}
	if result.Deferred {
		if result.StartError != nil {
			api.warnf(r, "Deferred resumed sync job %s after immediate start error: %v", id, result.StartError)
		} else {
			api.warnf(r, "Deferred resumed sync job %s because another instance claimed it", id)
		}
	}

	api.writeAudit(r, db.AuditSyncResumed, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true, "deferred": result.Deferred})
}

func (api *syncAPI) handleDeleteSync(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")

	if !api.requireSyncOwnership(w, r, id, userID) || !api.requireSyncPass(w, r, id) {
		return
	}

	err := api.syncPass.Delete(r.Context(), id)
	if err != nil {
		api.logf(r, "Failed to delete sync job: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	api.writeAudit(r, db.AuditSyncDeleted, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (api *syncAPI) handleDownloadSyncReport(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")

	if !api.requireSyncOwnership(w, r, id, userID) {
		return
	}

	job, err := db.GetSyncJobContext(r.Context(), api.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}

	failedTasks, err := db.GetFailedSyncTasksForReport(api.db, id, job.RunGeneration)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"sync_report_%s.csv\"", id))

	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"ID", "File Path", "Size (Bytes)", "Status", "Error Message", "Created At"})

	for _, task := range failedTasks {
		errMsg := ""
		if task.ErrorMessage.Valid {
			errMsg = sanitizeCSVFormula(task.ErrorMessage.String)
		}
		filePath := sanitizeCSVFormula(task.FilePath)

		_ = writer.Write([]string{
			task.ID,
			filePath,
			fmt.Sprintf("%d", task.FileSize),
			task.Status,
			errMsg,
			task.CreatedAt.Format(time.RFC3339),
		})
	}
	writer.Flush()
}

func (api *syncAPI) handleSyncErrors(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}
	if !api.requireSyncOwnership(w, r, id, userID) {
		return
	}
	limit, offset := parseErrorListPagination(r)
	items, total, err := db.GetSyncErrorsContext(r.Context(), api.db, id, limit, offset)
	if err != nil {
		api.logf(r, "Error fetching sync job %s errors: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	sanitizeErrorListItems(items)
	writeJSON(w, http.StatusOK, map[string]interface{}{"errors": items, "total": total, "limit": limit, "offset": offset})
}

func (api *syncAPI) handleSyncStream(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	if !api.acquireSyncStream(w, r, userID) {
		return
	}
	defer api.releaseSyncStream(userID)

	// Disable the server write deadline for this long-lived connection.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// 3s poll is a good balance: fast enough to feel live, slow enough to not
	// hammer the DB. Change-detection skips the flush when nothing changed.
	ticker := time.NewTicker(api.streamPollInterval)
	defer ticker.Stop()

	// Periodic comment heartbeat keeps the SSE connection alive behind proxies
	// that would otherwise GC an idle connection between data frames.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	// lastJSON tracks the previous payload to avoid flushing identical data
	// to the client on every tick when nothing has changed.
	var lastJSON []byte

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-ticker.C:
			jobs, err := db.GetSyncJobsForUserContext(r.Context(), api.db, userID)
			if err != nil {
				continue
			}
			if jobs == nil {
				jobs = []db.SyncJob{}
			}
			for i := range jobs {
				if jobs[i].Status == "RUNNING" || jobs[i].Status == "INDEXING" {
					if activeFiles, err := db.GetActiveSyncTaskPaths(api.db, r.Context(), jobs[i].ID); err == nil {
						jobs[i].ActiveFiles = activeFiles
					}
				} else if jobs[i].Status == "VERIFYING" {
					if verifyingFiles, err := db.GetVerifyingSyncTaskPaths(api.db, r.Context(), jobs[i].ID); err == nil {
						jobs[i].ActiveFiles = verifyingFiles
					}
				}
			}

			data, err := json.Marshal(jobs)
			if err != nil {
				continue
			}

			// Only push to client when data actually changed
			if bytes.Equal(data, lastJSON) {
				continue
			}
			lastJSON = data

			fmt.Fprintf(w, "event: sync_jobs\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// sanitizeCSVFormula prevents spreadsheet formula injection by prefixing
// cells that start with a trigger character with a single quote.
func sanitizeCSVFormula(input string) string {
	if len(input) == 0 {
		return input
	}
	firstChar := input[0]
	if firstChar == '=' || firstChar == '+' || firstChar == '-' || firstChar == '@' || firstChar == '\t' || firstChar == '\r' {
		return "'" + input
	}
	return input
}

// parseSyncTokenExpiry converts an optional RFC3339 expiry timestamp from the
// request into a sql.NullTime. A missing/invalid value falls back to "now" so
// the engine's ensureFreshToken treats the token as already-expired and refreshes
// on first use (safer than silently assuming a far-future expiry).
func parseSyncTokenExpiry(raw *string) sql.NullTime {
	if raw == nil || *raw == "" {
		return sql.NullTime{Time: time.Now(), Valid: true}
	}
	exp, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return sql.NullTime{Time: time.Now(), Valid: true}
	}
	return sql.NullTime{Time: exp, Valid: true}
}

func (api *syncAPI) handleSetSyncThreads(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "migration-sync-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}

	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	if !api.requireSyncOwnership(w, r, id, userID) {
		return
	}

	var req ThreadsRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	threads := req.Threads
	if threads < 1 || threads > 16 {
		writeError(w, http.StatusBadRequest, ErrThreadsOutOfRange)
		return
	}

	if err := db.UpdateSyncJobThreads(api.db, id, threads); err != nil {
		api.logf(r, "Error updating threads for sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (api *syncAPI) handleSetSyncBandwidth(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "migration-sync-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}

	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	if !api.requireSyncOwnership(w, r, id, userID) {
		return
	}

	var req BandwidthRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	if req.LimitMbps < 0 || req.LimitMbps > 1000 {
		writeError(w, http.StatusBadRequest, ErrBandwidthOutOfRange)
		return
	}

	if err := db.UpdateSyncJobBandwidthLimit(api.db, id, req.LimitMbps); err != nil {
		api.logf(r, "Error updating bandwidth limit for sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	job, err := db.GetSyncJobContext(r.Context(), api.db, id)
	if err != nil {
		api.logf(r, "Error loading sync job %s after bandwidth update: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	// Only active passes have a worker-side throttler to update. The persisted
	// value is used when a later pass starts, so idle and terminal jobs do not
	// need a Redis broadcast.
	if job.Status != "INDEXING" && job.Status != "RUNNING" && job.Status != "VERIFYING" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	if api.publishBandwidthChange != nil {
		if err := api.publishBandwidthChange(r.Context(), queue.BandwidthEvent{
			SyncJobID:          id,
			BandwidthLimitMbps: req.LimitMbps,
		}); err != nil {
			api.logf(r, "Warning: failed to publish bandwidth change for sync job %s: %v", id, err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type updateSyncScheduleRequest struct {
	IntervalMinutes int `json:"interval_minutes"`
}

type updateSyncScopeRequest struct {
	SelectedPaths     []string `json:"selected_paths"`
	TargetDir         string   `json:"target_dir"`
	ConflictStrategy  string   `json:"conflict_strategy,omitempty"`
	Direction         string   `json:"direction,omitempty"`
	DeletePropagation *bool    `json:"delete_propagation,omitempty"`
}

func (api *syncAPI) handleUpdateSyncSchedule(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "migration-sync-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")
	if !api.requireSyncOwnership(w, r, id, userID) {
		return
	}

	var req updateSyncScheduleRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	validIntervals := map[int]bool{5: true, 15: true, 30: true, 60: true, 360: true, 1440: true}
	if !validIntervals[req.IntervalMinutes] {
		writeValidationError(w, ErrSyncIntervalInvalid)
		return
	}

	if err := db.UpdateSyncJobInterval(api.db, id, req.IntervalMinutes); err != nil {
		api.logf(r, "Error updating sync schedule for job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	api.writeAudit(r, db.AuditSyncUpdated, id, userID, map[string]any{"interval_minutes": req.IntervalMinutes})
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (api *syncAPI) handleBrowseSyncJob(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "browse", connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}
	if !api.requireSyncOwnership(w, r, id, userID) {
		return
	}

	job, err := db.GetSyncJobContext(r.Context(), api.db, id)
	if err != nil {
		api.logf(r, "Error getting sync job %s for browse: %v", id, err)
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}

	role := r.URL.Query().Get("role")
	if role != "target" {
		role = "source"
	}
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		reqPath = "/"
	}
	resourceType := r.URL.Query().Get("resource_type")
	if resourceType == "" {
		resourceType = "files"
	}
	if resourceType != "files" && resourceType != "calendars" && resourceType != "contacts" {
		writeError(w, http.StatusBadRequest, ErrInvalidResourceType)
		return
	}

	endpoint, err := openSyncJobEndpoint[migrationBrowser](api, r.Context(), id, userID, role, job)
	if err != nil {
		if errors.Is(err, errProfileInvalidProvider) || errors.Is(err, errProfileProvider) {
			errCode := ErrSourceUrlInvalid
			if role == "target" {
				errCode = ErrTargetUrlInvalid
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": string(errCode)})
			return
		}
		api.logf(r, "handleBrowseSyncJob: failed to open endpoint for job %s role %s: %v", id, role, err)
		errCode := ErrSourceConnectionFailed
		if role == "target" {
			errCode = ErrTargetConnectionFailed
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": string(errCode)})
		return
	}
	defer endpoint.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	ok, err := endpoint.Client.Connect(ctx)
	if !ok {
		api.logf(r, "handleBrowseSyncJob: connection failed for provider %s: %v", endpoint.Provider, err)
		errCode := ErrSourceConnectionFailed
		if role == "target" {
			errCode = ErrTargetConnectionFailed
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": string(errCode)})
		return
	}

	items, err := endpoint.Client.GetDirectoryListing(ctx, resourceType, reqPath)
	if err != nil {
		api.logf(r, "handleBrowseSyncJob: failed to list %s for path %s (provider %s): %v", resourceType, reqPath, endpoint.Provider, err)
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": string(ErrListFailed)})
		return
	}

	collections := items

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"items":   collections,
		"files":   collections,
	})
}

type syncMkdirRequest struct {
	Role string `json:"role"`
	Path string `json:"path"`
	Name string `json:"name"`
}

func syncMkdirPath(parent, name string) (string, bool) {
	if parent == "" {
		parent = "/"
	}
	name = strings.TrimSpace(name)
	if !validManagedPath(parent) || !validManagerUploadName(name) {
		return "", false
	}
	return path.Join(path.Clean(parent), name), true
}

func (api *syncAPI) handleSyncMkdir(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "sync-mkdir", connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}
	if !api.requireSyncOwnership(w, r, id, userID) {
		return
	}

	var req syncMkdirRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	if req.Role != "target" {
		writeValidationError(w, ErrInvalidBody)
		return
	}
	dirPath, valid := syncMkdirPath(req.Path, req.Name)
	if !valid {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": ErrFolderPathInvalid})
		return
	}

	job, err := db.GetSyncJobContext(r.Context(), api.db, id)
	if err != nil {
		api.logf(r, "handleSyncMkdir: failed to load sync job %s: %v", id, err)
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}

	endpoint, err := openSyncJobEndpoint[migrationDirectoryCreator](api, r.Context(), id, userID, "target", job)
	if err != nil {
		if errors.Is(err, errProfileInvalidProvider) || errors.Is(err, errProfileProvider) {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": ErrTargetUrlInvalid})
			return
		}
		api.logf(r, "handleSyncMkdir: failed to open target endpoint for job %s: %v", id, err)
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": ErrTargetConnectionFailed})
		return
	}
	defer endpoint.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if ok, err := endpoint.Client.Connect(ctx); !ok {
		api.logf(r, "handleSyncMkdir: target connection failed for job %s: %v", id, err)
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": ErrTargetConnectionFailed})
		return
	}
	if err := endpoint.Client.CreateDirectory(ctx, "files", dirPath); err != nil {
		api.logf(r, "handleSyncMkdir: target directory creation failed for job %s: %v", id, err)
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": ErrFolderCreateFailed})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (api *syncAPI) handleUpdateSyncScope(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "migration-sync-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")
	if !api.requireSyncOwnership(w, r, id, userID) {
		return
	}

	job, err := db.GetSyncJobContext(r.Context(), api.db, id)
	if err != nil {
		api.logf(r, "Error getting sync job %s for scope update: %v", id, err)
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}

	if job.Status == "INDEXING" || job.Status == "RUNNING" || job.Status == "VERIFYING" {
		writeError(w, http.StatusConflict, ErrSyncInvalidState)
		return
	}

	if job.SourceProvider == "immich" || job.TargetProvider == "immich" {
		writeError(w, http.StatusBadRequest, ErrImmichSyncUnsupported)
		return
	}

	var req updateSyncScopeRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	if len(req.SelectedPaths) == 0 {
		writeValidationError(w, ErrNoSourcePaths)
		return
	}

	if req.TargetDir == "" {
		req.TargetDir = "/"
	}

	if err := db.UpdateSyncJobScope(api.db, id, req.SelectedPaths, req.TargetDir, req.ConflictStrategy, req.Direction, req.DeletePropagation); err != nil {
		if errors.Is(err, db.ErrSyncInvalidState) {
			writeError(w, http.StatusConflict, ErrSyncInvalidState)
			return
		}
		api.logf(r, "Error updating scope for sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	api.writeAudit(r, db.AuditSyncUpdated, id, userID, map[string]any{
		"selected_paths": req.SelectedPaths,
		"target_dir":     req.TargetDir,
	})

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
