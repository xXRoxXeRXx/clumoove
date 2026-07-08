package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	MainQueueKey       = "migration_tasks_queue"
	ProcessingQueuePrefix = "migration_processing"
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

// Enqueue adds a task payload to the main queue
func (q *Queue) Enqueue(ctx context.Context, migrationID, taskID string) error {
	payload := Payload{
		MigrationID: migrationID,
		TaskID:      taskID,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	return q.client.LPush(ctx, MainQueueKey, data).Err()
}

// Dequeue pops a task from the main queue and pushes it to a worker-specific processing queue (reliable queue pattern)
func (q *Queue) Dequeue(ctx context.Context, workerID string, timeout time.Duration) (*Payload, error) {
	processingQueue := fmt.Sprintf("%s:%s", ProcessingQueuePrefix, workerID)

	// BRPOPLPUSH source destination timeout
	// Blocks until an item is available or timeout is reached
	res, err := q.client.BRPopLPush(ctx, MainQueueKey, processingQueue, timeout).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil // Timeout, no items
		}
		return nil, err
	}

	var payload Payload
	if err := json.Unmarshal([]byte(res), &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal queue payload: %w", err)
	}

	return &payload, nil
}

// Complete removes the task from the worker's processing queue after successful processing
func (q *Queue) Complete(ctx context.Context, workerID string, payload *Payload) error {
	processingQueue := fmt.Sprintf("%s:%s", ProcessingQueuePrefix, workerID)
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	// Remove item from processing queue
	return q.client.LRem(ctx, processingQueue, 1, data).Err()
}

// RequeueFailed moves a failed task from the worker's processing queue back to the main queue
func (q *Queue) RequeueFailed(ctx context.Context, workerID string, payload *Payload) error {
	processingQueue := fmt.Sprintf("%s:%s", ProcessingQueuePrefix, workerID)
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	// Remove from processing and push back to main queue in a transaction
	pipe := q.client.TxPipeline()
	pipe.LRem(ctx, processingQueue, 1, data)
	pipe.LPush(ctx, MainQueueKey, data)
	_, err = pipe.Exec(ctx)
	return err
}

// CleanAbandonedTasks can be run by a manager routine to find tasks left in worker processing queues
// (e.g. if a worker container is restarted or dies)
func (q *Queue) RecoverAbandonedTasks(ctx context.Context, workerID string) error {
	processingQueue := fmt.Sprintf("%s:%s", ProcessingQueuePrefix, workerID)
	for {
		// RPOPLPUSH from processing back to main queue
		res, err := q.client.RPopLPush(ctx, processingQueue, MainQueueKey).Result()
		if err != nil {
			if err == redis.Nil {
				break // No more items in processing queue
			}
			return err
		}
		log.Printf("[Queue] Recovered abandoned task payload: %s\n", res)
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

// GetAbandonedWorkerQueues scans Redis for all processing queues and returns the worker IDs of dead workers
func (q *Queue) GetAbandonedWorkerQueues(ctx context.Context) ([]string, error) {
	var cursor uint64
	var keys []string
	matchPattern := fmt.Sprintf("%s:*", ProcessingQueuePrefix)

	for {
		var err error
		var scanKeys []string
		scanKeys, cursor, err = q.client.Scan(ctx, cursor, matchPattern, 100).Result()
		if err != nil {
			return nil, err
		}
		keys = append(keys, scanKeys...)
		if cursor == 0 {
			break
		}
	}

	var abandonedWorkers []string
	for _, key := range keys {
		workerID := strings.TrimPrefix(key, ProcessingQueuePrefix+":")
		if workerID == "" {
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

	return abandonedWorkers, nil
}
