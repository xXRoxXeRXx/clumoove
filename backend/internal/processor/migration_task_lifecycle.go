package processor

import (
	"context"
	"fmt"
	"strings"

	"backend/internal/crypto"
	"backend/internal/megasecret"
	"backend/internal/oauth"
	"backend/internal/queue"
)

func (p *Processor) processTask(ctx context.Context, payload *queue.Payload, threadID int, pool *providerPool) (err error) {
	// Shadow ctx with a cancelable one
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p.activeTasks.Store(payload.TaskID, activeTaskInfo{
		migrationID: payload.MigrationID,
		cancel:      cancel,
	})
	defer func() {
		p.activeTasks.Delete(payload.TaskID)
	}()

	ctx, claim, handled, err := p.claimMigrationTask(ctx, payload)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}
	mig := claim.migration
	task := claim.task
	migrationThrottler := claim.throttler
	logPath := task.FilePath
	if task.ResourceType == "files" {
		logPath = effectiveTargetPath(task, mig.TargetDir, mig.SourceProvider, mig.TargetProvider)
	}

	processorLogf("[Worker %s] Thread %d -> Request: [%s] %s (%d bytes) [%s -> %s]\n",
		p.workerID, threadID, strings.ToUpper(task.ResourceType), logPath, task.FileSize, mig.SourceProvider, mig.TargetProvider)

	// Decrypt credentials
	sourcePass, err := crypto.DecryptWithDomain(mig.SourcePasswordEncrypted, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(mig.SourceProvider)))
	if err != nil {
		return fmt.Errorf("failed to decrypt source password: %w", err)
	}

	targetPass, err := crypto.DecryptWithDomain(mig.TargetPasswordEncrypted, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(mig.TargetProvider)))
	if err != nil {
		return fmt.Errorf("failed to decrypt target password: %w", err)
	}

	// For OAuth providers: if the token is expired or within 2 minutes of expiry,
	// refresh it now so this task does not hit a 401. The daemon already handles
	// proactive rotation every 5 min, but tasks could be dequeued right as a token
	// expires, so we have this last-resort inline refresh.
	sourceProviderPass, err := p.ensureFreshOAuthToken(ctx, mig, "source", sourcePass)
	if err != nil {
		return fmt.Errorf("failed to refresh source OAuth token: %w", err)
	}

	targetProviderPass, err := p.ensureFreshOAuthToken(ctx, mig, "target", targetPass)
	if err != nil {
		return fmt.Errorf("failed to refresh target OAuth token: %w", err)
	}

	// Provider contexts carry scoped local-user and MEGA session state while a
	// worker-local pool owns the resulting provider for this migration.
	sourceCtx, err := megasecret.WithMegaSession(ctx, mig.SourceProvider, mig.SourceMegaSessionIDEncrypted, mig.SourceMegaMasterKeyEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt source MEGA session: %w", err)
	}
	targetCtx, err := megasecret.WithMegaSession(ctx, mig.TargetProvider, mig.TargetMegaSessionIDEncrypted, mig.TargetMegaMasterKeyEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt target MEGA session: %w", err)
	}
	poolKey := migrationProviderPoolKey(mig.ID)
	sourceClient, err := pool.get(sourceCtx, poolKey, "source", providerPoolSpec{
		providerType: mig.SourceProvider, url: mig.SourceURL, username: mig.SourceUsername,
		password: sourceProviderPass, passwordEncrypted: mig.SourcePasswordEncrypted,
		megaSessionIDEncrypted: mig.SourceMegaSessionIDEncrypted, megaMasterKeyEncrypted: mig.SourceMegaMasterKeyEncrypted,
		threads: mig.Threads,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to source provider: %w", err)
	}
	// MEGA's remote tree permits duplicate same-name folders. Serialize the
	// complete target operation before acquiring its client, so each client sees
	// the preceding task's tree and cannot race its parent-directory creation.
	unlockMegaTarget := p.lockMegaTarget(mig.TargetProvider, mig.TargetURL, mig.TargetUsername)
	defer unlockMegaTarget()
	targetClient, err := pool.get(targetCtx, poolKey, "target", providerPoolSpec{
		providerType: mig.TargetProvider, url: mig.TargetURL, username: mig.TargetUsername,
		password: targetProviderPass, passwordEncrypted: mig.TargetPasswordEncrypted,
		megaSessionIDEncrypted: mig.TargetMegaSessionIDEncrypted, megaMasterKeyEncrypted: mig.TargetMegaMasterKeyEncrypted,
		threads: mig.Threads,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to target provider: %w", err)
	}

	return p.executeMigrationTransfer(ctx, cancel, mig, task, sourceClient, targetClient, migrationThrottler)
}
