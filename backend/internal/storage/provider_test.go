package storage

import (
	"errors"
	"io"
	"net"
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

func TestValidateStoragePath(t *testing.T) {
	for _, value := range []string{"../secret", `folder\..\secret`} {
		if err := validateStoragePath(value); !errors.Is(err, ErrPathEscapesRoot) {
			t.Fatalf("validateStoragePath(%q) error = %v, want ErrPathEscapesRoot", value, err)
		}
	}
	if err := validateStoragePath("folder/file.txt"); err != nil {
		t.Fatalf("validateStoragePath() error = %v", err)
	}
}

func TestIsConnectionFailure(t *testing.T) {
	if !isConnectionFailure(io.EOF) || !isConnectionFailure(net.ErrClosed) {
		t.Fatal("connection errors must reset a provider session")
	}
	if isConnectionFailure(errors.New("permission denied")) {
		t.Fatal("permission errors must not reset a healthy provider session")
	}
}
