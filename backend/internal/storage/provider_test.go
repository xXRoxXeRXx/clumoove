package storage

import (
	"strings"
	"testing"
	"time"
)

func TestProgressReaderDropsUpdatesWhenConsumerIsSlow(t *testing.T) {
	progress := make(chan int64)
	reader := &ProgressReader{Reader: strings.NewReader("data"), ProgressChan: progress}

	completed := make(chan struct{})
	go func() {
		defer close(completed)
		buffer := make([]byte, 4)
		_, _ = reader.Read(buffer)
	}()

	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("progress reporting blocked the transfer reader")
	}
}
