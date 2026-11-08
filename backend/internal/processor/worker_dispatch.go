package processor

import (
	"context"
	"sync"
	"time"

	"backend/internal/queue"
)

// runTransferWorkers owns SQL dequeue, per-worker provider pools, and dispatch
// to the migration and sync lifecycles. Start owns process startup and
// background scheduling; this loop owns only claimed transfer work.
func (p *Processor) runTransferWorkers(ctx context.Context, notifyTasksCh <-chan struct{}) {
	var wg sync.WaitGroup
	for i := 0; i < p.transferWorkers; i++ {
		wg.Add(1)
		go func(threadID int) {
			defer wg.Done()
			pool := newProviderPool()
			defer pool.Close()
			// fallbackInterval is the maximum time an idle thread waits before
			// re-polling even without a notify signal. 5s is fine because
			// pg_notify delivers the wake-up immediately in the common case.
			fallbackInterval := 5 * time.Second
			if notifyTasksCh == nil {
				// LISTEN unavailable: use the original 2s poll to maintain
				// latency parity with the pre-optimisation behaviour.
				fallbackInterval = 2 * time.Second
			}
			for {
				select {
				case <-ctx.Done():
					return
				default:
					payload, err := p.queue.DequeueSQL(ctx, p.db, p.workerID)
					if err != nil {
						if ctx.Err() != nil {
							return
						}
						processorLogf("[Worker %s] Thread %d dequeue error: %v. Sleeping...\n", p.workerID, threadID, err)
						time.Sleep(2 * time.Second)
						continue
					}
					if payload == nil {
						p.pruneProviderPool(ctx, pool)
						select {
						case <-ctx.Done():
							return
						case <-notifyTasksCh:
						case <-time.After(fallbackInterval):
						}
						continue
					}
					if !p.acquireProviderSlot(ctx) {
						return
					}
					p.dispatchClaimedTask(ctx, payload, threadID, pool)
					p.releaseProviderSlot()
				}
			}
		}(i)
	}

	<-ctx.Done()
	processorLogf("[Worker %s] Shutdown signal received. Waiting for active tasks to finish...\n", p.workerID)
	wg.Wait()
	p.verificationWG.Wait()
	processorLogf("[Worker %s] Worker loop stopped.\n", p.workerID)
}

func (p *Processor) dispatchClaimedTask(ctx context.Context, payload *queue.Payload, threadID int, pool *providerPool) {
	if payload.SyncJobID != "" {
		processorLogf("[Worker %s] Thread %d processing sync task %s for job %s\n", p.workerID, threadID, payload.TaskID, payload.SyncJobID)
		if err := p.processSyncTask(ctx, payload, threadID, pool); err != nil {
			if shouldInvalidateProviderPool(err) {
				pool.invalidateSyncJob(payload.SyncJobID)
			}
			processorLogf("[Worker %s] Thread %d error processing sync task %s: %v\n", p.workerID, threadID, payload.TaskID, err)
			p.handleSyncTaskFailure(ctx, payload, err)
		} else {
			processorLogf("[Worker %s] Thread %d successfully processed sync task %s\n", p.workerID, threadID, payload.TaskID)
		}
		return
	}

	processorLogf("[Worker %s] Thread %d processing migration task %s for migration %s\n", p.workerID, threadID, payload.TaskID, payload.MigrationID)
	if err := p.processTask(ctx, payload, threadID, pool); err != nil {
		if shouldInvalidateProviderPool(err) {
			pool.invalidate(migrationProviderPoolKey(payload.MigrationID))
		}
		processorLogf("[Worker %s] Thread %d error processing task %s: %v\n", p.workerID, threadID, payload.TaskID, err)
		p.handleTaskFailure(ctx, payload, err)
	} else {
		processorLogf("[Worker %s] Thread %d successfully processed task %s\n", p.workerID, threadID, payload.TaskID)
	}
}
