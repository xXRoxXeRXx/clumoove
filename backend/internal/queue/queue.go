package queue

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

type Payload struct {
	MigrationID string `json:"migration_id"`
	TaskID      string `json:"task_id"`
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
			Addr: redisAddr,
		})
	} else {
		if opt.Password == "redis_secret" {
			return nil, fmt.Errorf("insecure REDIS_PASSWORD: 'redis_secret' is the default weak password. Please set a secure password in the environment variables.")
		}
		client = redis.NewClient(opt)
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

// DequeueSQL pops a task from the database queue natively respecting migration thread limits.
func (q *Queue) DequeueSQL(ctx context.Context, db *sql.DB, workerID string) (*Payload, error) {
	query := `
		WITH available_tasks AS (
			SELECT t.id, t.migration_id
			FROM tasks t
			JOIN migrations m ON t.migration_id = m.id
			WHERE t.status = 'PENDING'
			AND m.status IN ('RUNNING', 'INDEXING')
			AND (
				SELECT COUNT(*) FROM tasks t2 
				WHERE t2.migration_id = m.id AND t2.status = 'RUNNING'
			) < m.threads
			ORDER BY t.created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE tasks
		SET status = 'RUNNING', updated_at = CURRENT_TIMESTAMP, worker_hash = $1
		WHERE id = (SELECT id FROM available_tasks)
		RETURNING id, migration_id
	`
	var payload Payload
	err := db.QueryRowContext(ctx, query, workerID).Scan(&payload.TaskID, &payload.MigrationID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // No tasks available
		}
		return nil, fmt.Errorf("failed to dequeue task from sql: %w", err)
	}
	return &payload, nil
}

// RecoverAbandonedTasks resets tasks for a dead worker back to PENDING.
func (q *Queue) RecoverAbandonedTasks(ctx context.Context, db *sql.DB, workerID string) error {
	query := `
		UPDATE tasks 
		SET status = 'PENDING', worker_hash = NULL, updated_at = CURRENT_TIMESTAMP 
		WHERE status = 'RUNNING' AND worker_hash = $1
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
