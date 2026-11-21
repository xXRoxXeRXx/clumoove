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

func TestCloudResourceNonTransferableReason(t *testing.T) {
	vault := CloudResource{Metadata: FileMetadata{CustomProps: map[string]string{"onedrive_special_folder": "vault"}}}
	if reason, skip := vault.NonTransferableReason(); !skip || reason == "" {
		t.Fatalf("vault.NonTransferableReason() = (%q, %v), want non-empty reason and true", reason, skip)
	}
	boxNote := CloudResource{Metadata: FileMetadata{CustomProps: map[string]string{"box_non_transferable_kind": "box_note"}}}
	if reason, skip := boxNote.NonTransferableReason(); !skip || reason == "" {
		t.Fatalf("boxNote.NonTransferableReason() = (%q, %v), want non-empty reason and true", reason, skip)
	}
	regular := CloudResource{Name: "doc.pdf"}
	if reason, skip := regular.NonTransferableReason(); skip || reason != "" {
		t.Fatalf("regular.NonTransferableReason() = (%q, %v), want (\"\", false)", reason, skip)
	}
}
