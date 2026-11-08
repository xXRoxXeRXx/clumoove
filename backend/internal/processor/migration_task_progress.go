package processor

import (
	"context"
	"sync/atomic"
	"time"

	"backend/internal/db"
)

// startMigrationProgressReporter keeps the non-cumulative live-byte display
// and claim heartbeat active for one transfer. Completion bytes are persisted
// separately by the terminal-outcome stage, preventing retries from counting
// the same file twice.
func (p *Processor) startMigrationProgressReporter(ctx context.Context, cancel context.CancelFunc, migrationID string, taskID string, claimEpoch int64) (chan<- int64, func()) {
	progressChan := make(chan int64, 10)
	progressDone := make(chan struct{})
	lastByteNano := time.Now().UnixNano()
	taskStart := time.Now()

	go func() {
		defer close(progressDone)
		var bufferedBytes int64
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case bytes, ok := <-progressChan:
				if !ok {
					if bufferedBytes > 0 {
						_ = db.AddLiveBytes(p.db, ctx, migrationID, bufferedBytes)
					}
					return
				}
				bufferedBytes += bytes
				atomic.StoreInt64(&lastByteNano, time.Now().UnixNano())
			case <-ticker.C:
				if bufferedBytes > 0 {
					_ = db.AddLiveBytes(p.db, ctx, migrationID, bufferedBytes)
					bufferedBytes = 0
				}
			}
		}
	}()

	heartbeatStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		consecutiveFailures := 0
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ticker.C:
				stale := time.Since(taskStart) > taskHeartbeatGrace && time.Now().UnixNano()-atomic.LoadInt64(&lastByteNano) > int64(taskHeartbeatByteStale)
				if stale {
					continue
				}
				owned, err := db.HeartbeatTaskClaim(p.db, ctx, taskID, claimEpoch)
				if err == nil && !owned {
					cancel()
					return
				}
				if err != nil {
					consecutiveFailures++
					processorLogf("[Worker %s] heartbeat error for task %s (failure %d/5): %v", p.workerID, taskID, consecutiveFailures, err)
					if consecutiveFailures >= 5 {
						cancel()
						return
					}
					continue
				}
				consecutiveFailures = 0
			}
		}
	}()

	return progressChan, func() {
		close(progressChan)
		<-progressDone
		close(heartbeatStop)
	}
}
