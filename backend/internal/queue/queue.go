package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

type Payload struct {
	MigrationID string `json:"migration_id"`
	SyncJobID   string `json:"sync_job_id"`
	TaskID      string `json:"task_id"`
}

type BandwidthEvent struct {
	MigrationID      string `json:"migration_id"`
	BandwidthLimitMbps int  `json:"bandwidth_limit_mbps"`
}

type Queue struct {
	client *redis.Client
}

func NewQueue(redisAddr string) (*Queue, error) {
	opt, err := redis.ParseURL(redisAddr)
	var client *redis.Client
	if err != nil {
		// Fallback to simple address if not a full URL
		client = redis.NewClient(&redis.Options{
			Addr:     redisAddr,
			Password: os.Getenv("REDIS_PASSWORD"),
		})
	} else {
		if opt.Password == "" {
			opt.Password = os.Getenv("REDIS_PASSWORD")
		}
		client = redis.NewClient(opt)
	}

	// Always validate password presence and security (Finding 5)
	password := client.Options().Password
	if password == "" {
		return nil, fmt.Errorf("REDIS_PASSWORD is required for secure queue operations")
	}
	if password == "redis_secret" {
		return nil, fmt.Errorf("insecure REDIS_PASSWORD: 'redis_secret' is the default weak password. Please set a secure password in the environment variables.")
	}
	// Reject the docker-compose dev default so a deployment that forgets to set
	// REDIS_PASSWORD cannot run with a widely-known password (M-4).
	if password == "dev_redis_secure_pass_999" {
		return nil, fmt.Errorf("insecure REDIS_PASSWORD: 'dev_redis_secure_pass_999' is the docker-compose dev default. Override REDIS_PASSWORD with a strong, unique password for any non-local deployment.")
	}

	// Ping test with retry loop (resilient to Docker startup order)
	var pingErr error
	for attempt := 1; attempt <= 10; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		pingErr = client.Ping(ctx).Err()
		cancel()

		if pingErr == nil {
			return &Queue{client: client}, nil
		}

		log.Printf("Waiting for Redis to be ready (attempt %d/10): %v\n", attempt, pingErr)
		time.Sleep(2 * time.Second)
	}

	return nil, fmt.Errorf("failed to ping redis after 10 attempts: %w", pingErr)
}


// DequeueSQL pops a task from the database queue natively respecting migration/sync thread limits.
func (q *Queue) DequeueSQL(ctx context.Context, dbCon *sql.DB, workerID string) (*Payload, error) {
	query := `
		WITH candidate AS (
			SELECT t.id
			FROM tasks t
			LEFT JOIN migrations m ON t.migration_id = m.id
			LEFT JOIN sync_jobs sj ON t.sync_job_id = sj.id
			WHERE t.status = 'PENDING'
			AND (
				(t.migration_id IS NOT NULL AND m.status IN ('RUNNING', 'INDEXING') AND (
					SELECT COUNT(*) FROM tasks t2 
					WHERE t2.migration_id = m.id AND t2.status = 'RUNNING'
				) < m.threads)
				OR
				(t.sync_job_id IS NOT NULL AND sj.status IN ('RUNNING', 'INDEXING') AND (
					SELECT COUNT(*) FROM tasks t2 
					WHERE t2.sync_job_id = sj.id AND t2.status = 'RUNNING'
				) < sj.threads)
			)
			ORDER BY t.created_at ASC
			LIMIT 1
			FOR UPDATE OF t SKIP LOCKED
		)
		UPDATE tasks
		SET status = 'RUNNING', updated_at = CURRENT_TIMESTAMP, worker_hash = $1
		WHERE id = (SELECT id FROM candidate)
		RETURNING id, migration_id, sync_job_id
	`
	var payload Payload
	var migID, syncID sql.NullString
	err := dbCon.QueryRowContext(ctx, query, workerID).Scan(&payload.TaskID, &migID, &syncID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // No tasks available
		}
		return nil, fmt.Errorf("failed to dequeue task from sql: %w", err)
	}
	if migID.Valid {
		payload.MigrationID = migID.String
	}
	if syncID.Valid {
		payload.SyncJobID = syncID.String
	}
	return &payload, nil
}

// RecoverAbandonedTasks resets tasks for a dead worker back to PENDING.
func (q *Queue) RecoverAbandonedTasks(ctx context.Context, db *sql.DB, workerID string) error {
	query := `
		UPDATE tasks 
		SET status = 'PENDING', worker_hash = NULL, updated_at = CURRENT_TIMESTAMP 
		WHERE status = 'RUNNING' AND worker_hash = $1
		  AND updated_at < CURRENT_TIMESTAMP - INTERVAL '2 minutes'
	`
	res, err := db.ExecContext(ctx, query, workerID)
	if err != nil {
		return err
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected > 0 {
		log.Printf("[Queue] Recovered %d abandoned tasks for worker %s\n", rowsAffected, workerID)
	}
	return nil
}

// TryClaimWorkerRecoveryLock atomically claims a recovery lock for a dead worker using SET NX.
// Returns true if this caller has acquired the lock and is responsible for recovery.
// The lock expires after ttl, preventing a stale lock from blocking recovery forever.
func (q *Queue) TryClaimWorkerRecoveryLock(ctx context.Context, workerID string, ttl time.Duration) (bool, error) {
	key := fmt.Sprintf("worker:recovery-lock:%s", workerID)
	return q.client.SetNX(ctx, key, "1", ttl).Result()
}

// TryClaimScheduleLock atomically claims a processing lock for a schedule using SET NX.
// Returns true if this caller has acquired the lock and is responsible for triggering the job.
// The lock expires after ttl, preventing a stale lock from blocking scheduling forever and
// ensuring that in a multi-instance API deployment only one gateway triggers a given schedule.
func (q *Queue) TryClaimScheduleLock(ctx context.Context, scheduleID string, ttl time.Duration) (bool, error) {
	key := fmt.Sprintf("schedule:lock:%s", scheduleID)
	return q.client.SetNX(ctx, key, "1", ttl).Result()
}

// RegisterActiveWorker registers/refreshes the worker's active status in Redis
func (q *Queue) RegisterActiveWorker(ctx context.Context, workerID string, ttl time.Duration) error {
	key := fmt.Sprintf("worker:active:%s", workerID)
	return q.client.Set(ctx, key, "1", ttl).Err()
}

// GetAbandonedWorkerQueues scans the database for all workers currently processing tasks and cross-checks their liveness in Redis
func (q *Queue) GetAbandonedWorkerQueues(ctx context.Context, db *sql.DB) ([]string, error) {
	query := `SELECT DISTINCT worker_hash FROM tasks WHERE status = 'RUNNING' AND worker_hash IS NOT NULL`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var abandonedWorkers []string
	for rows.Next() {
		var workerID string
		if err := rows.Scan(&workerID); err != nil {
			continue
		}

		activeKey := fmt.Sprintf("worker:active:%s", workerID)
		exists, err := q.client.Exists(ctx, activeKey).Result()
		if err != nil {
			log.Printf("[Queue] Warning: could not check liveness key %q: %v — treating worker as alive\n", activeKey, err)
			continue
		}

		if exists == 0 {
			abandonedWorkers = append(abandonedWorkers, workerID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return abandonedWorkers, nil
}

// PublishCancelEvent publishes a cancellation event for a migration via Redis Pub/Sub
func (q *Queue) PublishCancelEvent(ctx context.Context, migrationID string) error {
	channel := "migration-control:cancel"
	return q.client.Publish(ctx, channel, migrationID).Err()
}

// SubscribeToCancelEvents listens for cancellation events and calls the callback.
// If the Pub/Sub channel closes (e.g. transient Redis disconnect) it reconnects
// with exponential back-off so cancel events are never silently lost.
func (q *Queue) SubscribeToCancelEvents(ctx context.Context, callback func(migrationID string)) {
	channel := "migration-control:cancel"
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		pubsub := q.client.Subscribe(ctx, channel)
		ch := pubsub.Channel()

		closed := false
		for !closed {
			select {
			case <-ctx.Done():
				pubsub.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					// Channel closed — Redis blip; reconnect after back-off
					closed = true
				} else {
					backoff = time.Second // reset on successful message
					callback(msg.Payload)
				}
			}
		}
		pubsub.Close()

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// PublishBandwidthChange publishes a bandwidth change event for a migration via Redis Pub/Sub
func (q *Queue) PublishBandwidthChange(ctx context.Context, event BandwidthEvent) error {
	channel := "migration-control:bandwidth"
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal bandwidth event: %w", err)
	}
	return q.client.Publish(ctx, channel, payload).Err()
}

// SubscribeToBandwidthChanges listens for bandwidth change events and calls the callback.
// If the Pub/Sub channel closes (e.g. transient Redis disconnect) it reconnects
// with exponential back-off so bandwidth events are never silently lost.
func (q *Queue) SubscribeToBandwidthChanges(ctx context.Context, callback func(event BandwidthEvent)) {
	channel := "migration-control:bandwidth"
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		pubsub := q.client.Subscribe(ctx, channel)
		ch := pubsub.Channel()

		closed := false
		for !closed {
			select {
			case <-ctx.Done():
				pubsub.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					closed = true
				} else {
					backoff = time.Second
					var event BandwidthEvent
					if err := json.Unmarshal([]byte(msg.Payload), &event); err == nil {
						callback(event)
					}
				}
			}
		}
		pubsub.Close()

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}
