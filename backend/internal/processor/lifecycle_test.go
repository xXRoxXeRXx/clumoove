package processor

import (
	"bytes"
	"context"
	"testing"
	"time"

	"backend/internal/throttle"
)

func TestStartWaitsForTaskListenerToReleaseItsConnectionAfterCancellation(t *testing.T) {
	listenerClosed := make(chan struct{})
	p := &Processor{
		workerID:  "worker-test",
		notifyDSN: "postgres://listener",
		listenForTasks: func(ctx context.Context, _ string) (<-chan struct{}, error) {
			ch := make(chan struct{})
			go func() {
				<-ctx.Done()
				time.Sleep(50 * time.Millisecond)
				close(listenerClosed)
				close(ch)
			}()
			return ch, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() {
		p.Start(ctx)
		close(returned)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after cancellation")
	}
	select {
	case <-listenerClosed:
	default:
		t.Fatal("Start returned before the task listener released its connection")
	}
}

func TestProcessorCancelMigrationCancelsOnlyMatchingTasks(t *testing.T) {
	p := &Processor{workerID: "w-test"}
	ctxM1, cancelM1 := context.WithCancel(context.Background())
	defer cancelM1()
	ctxM2, cancelM2 := context.WithCancel(context.Background())
	defer cancelM2()
	ctxS1, cancelS1 := context.WithCancel(context.Background())
	defer cancelS1()

	p.activeTasks.Store("t1", activeTaskInfo{migrationID: "mig-1", cancel: cancelM1})
	p.activeTasks.Store("t2", activeTaskInfo{migrationID: "mig-2", cancel: cancelM2})
	p.activeTasks.Store("t3", activeTaskInfo{syncJobID: "sync-1", syncGeneration: 1, cancel: cancelS1})

	p.CancelMigration("mig-1")

	if ctxM1.Err() == nil {
		t.Fatal("expected mig-1 task context to be cancelled")
	}
	if ctxM2.Err() != nil {
		t.Fatalf("unexpected cancellation for mig-2: %v", ctxM2.Err())
	}
	if ctxS1.Err() != nil {
		t.Fatalf("unexpected cancellation for sync-1: %v", ctxS1.Err())
	}
}

func TestProcessorCancelSyncJobRespectsGenerationAndAllGenerations(t *testing.T) {
	p := &Processor{workerID: "w-test"}
	ctxGen1, cancelGen1 := context.WithCancel(context.Background())
	defer cancelGen1()
	ctxGen2, cancelGen2 := context.WithCancel(context.Background())
	defer cancelGen2()
	ctxOther, cancelOther := context.WithCancel(context.Background())
	defer cancelOther()

	p.activeTasks.Store("t1", activeTaskInfo{syncJobID: "sync-1", syncGeneration: 1, cancel: cancelGen1})
	p.activeTasks.Store("t2", activeTaskInfo{syncJobID: "sync-1", syncGeneration: 2, cancel: cancelGen2})
	p.activeTasks.Store("t3", activeTaskInfo{syncJobID: "sync-2", syncGeneration: 1, cancel: cancelOther})

	p.CancelSyncJob("sync-1", 1, false)
	if ctxGen1.Err() == nil {
		t.Fatal("expected sync-1 gen 1 to be cancelled")
	}
	if ctxGen2.Err() != nil {
		t.Fatal("did not expect sync-1 gen 2 to be cancelled when allGenerations=false")
	}
	if ctxOther.Err() != nil {
		t.Fatal("did not expect sync-2 to be cancelled")
	}

	p.CancelSyncJob("sync-1", 0, true)
	if ctxGen2.Err() == nil {
		t.Fatal("expected sync-1 gen 2 to be cancelled when allGenerations=true")
	}
	if ctxOther.Err() != nil {
		t.Fatal("did not expect sync-2 to be cancelled")
	}
}

func TestProcessorSetBandwidthLimitUpdatesActiveThrottler(t *testing.T) {
	p := &Processor{workerID: "w-test"}
	th := throttle.NewMigrationThrottler(0)
	p.throttlers.Store("mig-1", th)

	p.SetBandwidthLimit("mig-1", 1)
	p.SetBandwidthLimit("missing-job", 5)

	// 1 Mbps corresponds to a 131072-byte token bucket burst, so a 262144-byte
	// read is capped to one burst while an unlimited (0 Mbps) reader reads all.
	payload := bytes.Repeat([]byte("a"), 262144)
	r := throttle.NewThrottledReader(context.Background(), bytes.NewReader(payload), th)
	buf := make([]byte, len(payload))
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("throttled read: %v", err)
	}
	if n != 131072 {
		t.Fatalf("read = %d bytes, want 131072 (1 Mbps burst)", n)
	}
}

func TestProcessorUsesSuppliedProviderSlots(t *testing.T) {
	slots := make(chan struct{}, 2)
	p, err := NewProcessor(nil, nil, Config{
		WorkerID:      "w-test",
		MaxThreads:    4,
		ProviderSlots: slots,
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	if p.providerSlots != slots {
		t.Fatal("expected Processor to share supplied ProviderSlots channel")
	}
}
