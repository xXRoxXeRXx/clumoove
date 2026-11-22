package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/db"
	"backend/internal/processor"
	"backend/internal/sanitize"
	"backend/internal/storage"
)

type verificationPassConfig struct {
	GuardAlreadyHeld  bool
	EntityType        string
	EntityID          string
	UserID            string
	SourceProvider    string
	TargetProvider    string
	TargetURL         string
	TargetUsername    string
	TargetPassword    string
	MegaSession       storage.MegaSession
	TargetDir         string
	Threads           int
	TargetClient      verificationTarget
	NewTargetProvider func(ctx context.Context, providerType, urlStr, username, password string) (connectableVerificationTarget, error)
	GetTasks          func(ctx context.Context) ([]*db.Task, error)
	ReconcileProgress func() error
	MarkVerified      func(ctx context.Context, task *db.Task, targetHash string) (bool, error)
	MarkMismatch      func(ctx context.Context, task *db.Task) (bool, error)
	Release           func(ctx context.Context)
	RenewLease        func(ctx context.Context) (bool, error)
	CanWrite          func() bool
}

type verificationTarget interface {
	storage.FileExister
	storage.FileHasher
	VerificationMode() storage.VerificationMode
}

type connectableVerificationTarget interface {
	verificationTarget
	Connect(ctx context.Context) (bool, error)
	Close() error
}

func (v *checksumVerifier) runPass(ctx context.Context, cfg verificationPassConfig) {
	guardKey := verificationGuardKey(cfg.EntityType, cfg.EntityID)
	if !cfg.GuardAlreadyHeld {
		if _, loaded := v.verifyingEntities.LoadOrStore(guardKey, true); loaded {
			workerLogf("verifier", "[VERIFIER] Verification pass already in progress for %s %s, skipping tick.\n", cfg.EntityType, cfg.EntityID)
			return
		}
		defer v.verifyingEntities.Delete(guardKey)
	}
	if cfg.Release != nil {
		defer func() {
			releaseCtx, cancelRelease := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelRelease()
			cfg.Release(releaseCtx)
		}()
	}

	passCtx, cancelPass := context.WithCancel(ctx)
	defer cancelPass()
	if cfg.RenewLease != nil {
		stillVerifying, err := cfg.RenewLease(passCtx)
		if err != nil {
			workerLogf("verifier", "[VERIFIER] Cannot confirm verification state for %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
			return
		}
		if !stillVerifying {
			return
		}
		stopWatch := make(chan struct{})
		defer close(stopWatch)
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stopWatch:
					return
				case <-passCtx.Done():
					return
				case <-ticker.C:
					stillVerifying, err := cfg.RenewLease(passCtx)
					if err != nil {
						workerLogf("verifier", "[VERIFIER] Cannot refresh verification state for %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
						continue
					}
					if !stillVerifying {
						workerLogf("verifier", "[VERIFIER] %s %s cancelled because it is no longer VERIFYING.\n", cfg.EntityType, cfg.EntityID)
						cancelPass()
						return
					}
				}
			}
		}()
	}

	canWrite := func() bool {
		if passCtx.Err() != nil {
			return false
		}
		if cfg.CanWrite != nil && !cfg.CanWrite() {
			return false
		}
		return true
	}
	markVerified := func(task *db.Task, targetHash string) bool {
		if !canWrite() {
			return false
		}
		if cfg.MarkVerified != nil {
			ok, err := cfg.MarkVerified(passCtx, task, targetHash)
			if err != nil {
				workerLogf("verifier", "[VERIFIER] Failed to save verification for task %s: %v\n", task.ID, err)
			}
			return ok && err == nil
		}
		return v.tasks.MarkTaskChecksumVerified(passCtx, task.ID, targetHash) == nil
	}
	markMismatch := func(task *db.Task, message, targetHash string) {
		task.Status = "FAILED"
		task.ErrorMessage = sql.NullString{String: message, Valid: true}
		if targetHash != "" {
			task.TargetHash = sql.NullString{String: targetHash, Valid: true}
		}
		if !canWrite() {
			return
		}
		if cfg.MarkMismatch != nil {
			task.NextRetryAt = sql.NullTime{}
			_, _ = cfg.MarkMismatch(passCtx, task)
			return
		}
		task.NextRetryAt = sql.NullTime{Time: time.Now(), Valid: true}
		_ = v.tasks.UpdateTaskStatus(task)
	}

	unverifiedTasks, err := cfg.GetTasks(passCtx)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Error getting unverified tasks for %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
		return
	}

	total := len(unverifiedTasks)
	if total == 0 {
		_ = cfg.ReconcileProgress()
		workerLogf("verifier", "[VERIFIER] %s %s verification completed (0 unverified remaining).\n", cfg.EntityType, cfg.EntityID)
		return
	}

	if cfg.TargetProvider == "webdav" {
		workerLogf("verifier", "[VERIFIER] WebDAV target has no checksum API; validating each target size.\n")
	}

	passCtx = storage.WithLocalUserScope(passCtx, cfg.UserID)
	targetClient := cfg.TargetClient
	if targetClient == nil {
		createdTarget, ok := v.connectTarget(passCtx, cfg)
		if !ok {
			return
		}
		defer createdTarget.Close()
		targetClient = createdTarget
	}

	verificationMode := targetClient.VerificationMode()
	numWorkers := 1
	if verificationMode == storage.VerificationCryptographicHash {
		numWorkers = cfg.Threads
		if numWorkers <= 0 {
			numWorkers = 4
		}
		if numWorkers > 8 {
			numWorkers = 8
		}
		if numWorkers > total {
			numWorkers = total
		}
	}

	workerLogf("verifier", "[VERIFIER] Starting checksum verification pass for %d tasks in %s %s (%d workers)\n", total, cfg.EntityType, cfg.EntityID, numWorkers)

	taskChan := make(chan *db.Task, total)
	for _, t := range unverifiedTasks {
		taskChan <- t
	}
	close(taskChan)

	var (
		processedCount atomic.Int64
		wg             sync.WaitGroup
	)
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskChan {
				if passCtx.Err() != nil {
					return
				}
				if !v.verifyTask(passCtx, cfg, targetClient, verificationMode, task, markVerified, markMismatch) {
					return
				}
				current := processedCount.Add(1)
				if current == 1 || current%50 == 0 || current == int64(total) {
					workerLogf("verifier", "[VERIFIER] %s %s verification progress: %d/%d tasks processed (%.1f%%)\n",
						cfg.EntityType, cfg.EntityID, current, total, float64(current)/float64(total)*100.0)
				}
			}
		}()
	}

	wg.Wait()
	if passCtx.Err() != nil {
		workerLogf("verifier", "[VERIFIER] %s %s verification pass aborted.\n", cfg.EntityType, cfg.EntityID)
		return
	}
	_ = cfg.ReconcileProgress()
	workerLogf("verifier", "[VERIFIER] %s %s checksum verification pass completed.\n", cfg.EntityType, cfg.EntityID)
}

func (v *checksumVerifier) connectTarget(passCtx context.Context, cfg verificationPassConfig) (connectableVerificationTarget, bool) {
	newTargetProvider := cfg.NewTargetProvider
	if newTargetProvider == nil {
		newTargetProvider = func(ctx context.Context, providerType, urlStr, username, password string) (connectableVerificationTarget, error) {
			return storage.NewProvider(ctx, providerType, urlStr, username, password, storage.WithProviderMegaSession(cfg.MegaSession))
		}
	}
	createdTarget, err := newTargetProvider(passCtx, cfg.TargetProvider, cfg.TargetURL, cfg.TargetUsername, cfg.TargetPassword)
	clear(cfg.MegaSession.MasterKey)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Failed to create target provider for verification on %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
		return nil, false
	}
	connected, connectErr := createdTarget.Connect(passCtx)
	if !connected {
		_ = createdTarget.Close()
		if connectErr == nil {
			connectErr = errors.New("provider rejected connection")
		}
		workerLogf("verifier", "[VERIFIER] Failed to connect to target provider for verification on %s %s: %v\n", cfg.EntityType, cfg.EntityID, connectErr)
		return nil, false
	}
	return createdTarget, true
}

func (v *checksumVerifier) verifyTask(
	passCtx context.Context,
	cfg verificationPassConfig,
	targetClient verificationTarget,
	verificationMode storage.VerificationMode,
	task *db.Task,
	markVerified func(*db.Task, string) bool,
	markMismatch func(*db.Task, string, string),
) bool {
	if task.ResourceType != "files" || task.IsDirectory() {
		return markVerified(task, "")
	}

	targetPath := processor.EffectiveTargetPath(task, cfg.TargetDir, cfg.SourceProvider, cfg.TargetProvider)
	targetResourceID := ""
	if cfg.TargetProvider == "immich" {
		var metadata storage.FileMetadata
		if err := json.Unmarshal(task.Metadata, &metadata); err != nil {
			workerLogf("verifier", "[VERIFIER] Immich task %s has invalid metadata; leaving it unverified: %v\n", task.ID, err)
			return true
		}
		assetID := metadata.CustomProps["immich_target_asset_id"]
		if strings.TrimSpace(assetID) == "" {
			workerLogf("verifier", "[VERIFIER] Immich task %s has no persisted target asset ID; leaving it unverified until retransfer.\n", task.ID)
			return true
		}
		targetResourceID = assetID
	}

	var targetHash string
	var errHash error
	isDirErr := func(err error) bool {
		if err == nil {
			return false
		}
		msg := strings.ToLower(err.Error())
		return strings.Contains(msg, "is a directory") || strings.Contains(msg, "is a folder")
	}
	verifySizeAndMark := func(hash string) bool {
		if isDirErr(errHash) {
			workerLogf("verifier", "[VERIFIER] [DIRECTORY_VERIFIED] %s — target directory confirmed [PASSED]\n", targetPath)
			return markVerified(task, hash)
		}
		exists, targetSize, sizeErr := storage.VerifyTargetSizeByResourceID(passCtx, targetClient, task.ResourceType, targetPath, targetResourceID)
		if sizeErr != nil {
			if isDirErr(sizeErr) {
				workerLogf("verifier", "[VERIFIER] [DIRECTORY_VERIFIED] %s — target directory confirmed [PASSED]\n", targetPath)
				return markVerified(task, hash)
			}
			workerLogf("verifier", "[VERIFIER] Cannot recheck target size for %s: %v\n", targetPath, sizeErr)
			return false
		}
		if !exists || (targetSize != task.FileSize && !(task.FileSize == 0 && task.IsDirectory())) {
			workerLogf("verifier", "[VERIFIER] [SIZE_MISMATCH] %s | Got: %d | Expected: %d\n", targetPath, targetSize, task.FileSize)
			markMismatch(task, fmt.Sprintf("target size mismatch: got %d bytes, expected %d", targetSize, task.FileSize), hash)
			return false
		}
		return markVerified(task, hash)
	}

	if verificationMode == storage.VerificationSizeOnly {
		workerLogf("verifier", "[VERIFIER] [SIZE_ONLY] %s | Target does not expose a comparable cryptographic hash — verifying size (%d bytes)\n", targetPath, task.FileSize)
		return verifySizeAndMark("")
	}
	if verificationMode == storage.VerificationNone {
		workerLogf("verifier", "[VERIFIER] [NO_INDEPENDENT_VERIFICATION] %s | Target does not support verification\n", targetPath)
		return true
	}

	for attempt := 0; attempt < 3; attempt++ {
		hashCtx, taskCancel := context.WithTimeout(passCtx, 15*time.Second)
		targetHash, errHash = storage.GetFileHashByResourceID(hashCtx, targetClient, task.ResourceType, targetPath, targetResourceID)
		taskCancel()

		if (errHash == nil && targetHash != "") || isNonRetryableHashError(errHash) ||
			(cfg.TargetProvider == "immich" && errors.Is(errHash, storage.ErrNotFound)) {
			break
		}
		if attempt < 2 {
			select {
			case <-passCtx.Done():
				return false
			case <-time.After(2 * time.Second):
			}
		}
	}

	if errHash == nil && targetHash != "" {
		cmp := storage.CompareTargetHash(task.SourceHash.String, task.WorkerHash.String, targetHash)
		if cmp.HasSource {
			if cmp.Comparable {
				if cmp.Matched {
					workerLogf("verifier", "[VERIFIER] [MATCH] %s | Algo: %s | Hash: %s\n", targetPath, cmp.TargetAlgo, cmp.TargetValue)
					_ = markVerified(task, targetHash)
				} else {
					workerLogf("verifier", "[VERIFIER] [MISMATCH] %s | Expected (%s): %s | Received (%s): %s — marking FAILED for automatic re-copy\n",
						targetPath, cmp.SourceAlgo, cmp.SourceValue, cmp.TargetAlgo, cmp.TargetValue)
					markMismatch(task, fmt.Sprintf("checksum mismatch: expected (%s) %s, got (%s) %s", cmp.SourceAlgo, cmp.SourceValue, cmp.TargetAlgo, cmp.TargetValue), targetHash)
				}
			} else if cmp.SourceAlgo == "ETAG" || cmp.TargetAlgo == "ETAG" {
				workerLogf("verifier", "[VERIFIER] [SIZE_VERIFIED] %s | Source (%s): %s | Target: No cryptographic hash on target (returned ETag: %s) — size (%d bytes) verified [PASSED]\n",
					targetPath, cmp.SourceAlgo, cmp.SourceValue, cmp.TargetValue, task.FileSize)
				_ = verifySizeAndMark(targetHash)
			} else {
				workerLogf("verifier", "[VERIFIER] [ALGO_DIFF] %s | Source (%s): %s | Target (%s): %s — size (%d bytes) verified\n",
					targetPath, cmp.SourceAlgo, cmp.SourceValue, cmp.TargetAlgo, cmp.TargetValue, task.FileSize)
				_ = verifySizeAndMark(targetHash)
			}
		} else {
			workerLogf("verifier", "[VERIFIER] [NO_SOURCE_HASH] %s | Target (%s): %s — registered target hash\n", targetPath, cmp.TargetAlgo, cmp.TargetValue)
			_ = verifySizeAndMark(targetHash)
		}
		return true
	}

	reason := "checksum not available"
	if errHash != nil {
		reason = sanitize.SanitizeError(errHash.Error())
	}
	if srcHash := storage.BestSourceHash(task.SourceHash.String, task.WorkerHash.String); srcHash != "" {
		workerLogf("verifier", "[VERIFIER] [NO_TARGET_HASH] %s | Expected: %s | Reason: %s — falling back to size verification (%d bytes)\n",
			targetPath, srcHash, reason, task.FileSize)
	} else {
		workerLogf("verifier", "[VERIFIER] [NO_TARGET_HASH] %s | Reason: %s — falling back to size verification (%d bytes)\n",
			targetPath, reason, task.FileSize)
	}
	return verifySizeAndMark("")
}

func isNonRetryableHashError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, storage.ErrHashNotSupported) || errors.Is(err, storage.ErrChecksumNotAvailable) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "checksum not available") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "not implemented") ||
		strings.Contains(msg, "is a directory") ||
		strings.Contains(msg, "is a folder")
}
