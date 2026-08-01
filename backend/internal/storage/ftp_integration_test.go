//go:build integration

package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

// TestFTPProviderIntegration exercises an externally configured FTPS server.
// Configure one or both modes with trusted certificates before running:
// FTPS_EXPLICIT_URL=ftp://host:21?tls=explicit
// FTPS_IMPLICIT_URL=ftps://host:990
// FTPS_USERNAME and FTPS_PASSWORD
func TestFTPProviderIntegration(t *testing.T) {
	username, password := os.Getenv("FTPS_USERNAME"), os.Getenv("FTPS_PASSWORD")
	if username == "" || password == "" {
		t.Skip("set FTPS_USERNAME and FTPS_PASSWORD to run FTPS integration tests")
	}
	endpoints := []string{os.Getenv("FTPS_EXPLICIT_URL"), os.Getenv("FTPS_IMPLICIT_URL")}
	if endpoints[0] == "" && endpoints[1] == "" {
		t.Skip("set FTPS_EXPLICIT_URL and/or FTPS_IMPLICIT_URL to run FTPS integration tests")
	}

	for _, endpoint := range endpoints {
		if endpoint == "" {
			continue
		}
		t.Run(endpoint, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			provider, err := NewFTPProvider(endpoint, username, password)
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Close()
			if connected, err := provider.Connect(ctx); err != nil || !connected {
				t.Fatalf("Connect() = %v, %v", connected, err)
			}

			base := fmt.Sprintf("clumoove-ftps-integration-%d", time.Now().UnixNano())
			original := "/" + base + ".tmp"
			renamed := "/" + base
			payload := []byte("FTPS protected passive transfer")
			if err := provider.StreamUpload(ctx, "files", original, bytes.NewReader(payload), int64(len(payload))); err != nil {
				t.Fatal(err)
			}
			defer provider.DeleteFile(context.Background(), "files", renamed)
			defer provider.DeleteFile(context.Background(), "files", original)
			if err := provider.RenameFile(ctx, "files", original, renamed); err != nil {
				t.Fatal(err)
			}
			exists, size, err := provider.FileExists(ctx, "files", renamed)
			if err != nil || !exists || size != int64(len(payload)) {
				t.Fatalf("FileExists() = %v, %d, %v", exists, size, err)
			}
			stream, err := provider.StreamDownload(ctx, "files", renamed)
			if err != nil {
				t.Fatal(err)
			}
			actual, readErr := io.ReadAll(stream)
			closeErr := stream.Close()
			if readErr != nil || closeErr != nil || !bytes.Equal(actual, payload) {
				t.Fatalf("download = %q, read error = %v, close error = %v", actual, readErr, closeErr)
			}
			if err := provider.DeleteFile(ctx, "files", renamed); err != nil {
				t.Fatal(err)
			}
		})
	}
}
