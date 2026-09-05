package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"backend/internal/db"
	"backend/internal/storage"
)

func archiveTestResolved(provider storage.StorageProvider) *resolvedFileProfile {
	return &resolvedFileProfile{
		profile:  &db.ConnectionProfile{Provider: "nextcloud"},
		provider: provider,
		ctx:      context.Background(),
		close:    func() {},
	}
}

func archiveDirectory(path string) storage.CloudResource {
	return storage.CloudResource{Path: path, Name: pathBase(path), IsDir: true}
}

func archiveFile(path string, size int64) storage.CloudResource {
	return storage.CloudResource{Path: path, Name: pathBase(path), Size: size}
}

func pathBase(value string) string {
	for i := len(value) - 1; i >= 0; i-- {
		if value[i] == '/' {
			return value[i+1:]
		}
	}
	return value
}

func TestPlanFileArchiveTraversesBreadthFirst(t *testing.T) {
	provider := &legacyFileManagerTestProvider{listings: map[string][]storage.CloudResource{
		"/docs":        {archiveDirectory("/docs/nested"), archiveFile("/docs/a.txt", 2)},
		"/docs/nested": {archiveFile("/docs/nested/b.txt", 3)},
	}}
	members, err := planFileArchive(archiveTestResolved(provider), []batchReference{{reference: fileReference{Kind: "directory", Locator: storage.ManagerLocator{Path: "/docs"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].name != "docs/a.txt" || members[1].name != "docs/nested/b.txt" {
		t.Fatalf("members = %#v", members)
	}
}

func TestPlanFileArchiveRejectsCyclesAndLimits(t *testing.T) {
	tests := []struct {
		name     string
		listings map[string][]storage.CloudResource
	}{
		{
			name: "cycle",
			listings: map[string][]storage.CloudResource{
				"/docs":      {archiveDirectory("/docs/loop")},
				"/docs/loop": {archiveDirectory("/docs")},
			},
		},
		{
			name: "declared byte limit",
			listings: map[string][]storage.CloudResource{
				"/docs": {archiveFile("/docs/large.bin", fileArchiveMaximumBytes+1)},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := planFileArchive(archiveTestResolved(&legacyFileManagerTestProvider{listings: test.listings}), []batchReference{{reference: fileReference{Kind: "directory", Locator: storage.ManagerLocator{Path: "/docs"}}}})
			if !errors.Is(err, storage.ErrManagerDirectoryCycle) && !errors.Is(err, storage.ErrManagerDirectoryTooLarge) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestWriteFileArchiveAddsFailureManifest(t *testing.T) {
	provider := &legacyFileManagerTestProvider{downloadErr: errors.New("provider failure")}
	var output bytes.Buffer
	if err := writeFileArchive(&output, archiveTestResolved(provider), []fileArchiveMember{{locator: storage.ManagerLocator{Path: "/missing.txt"}, name: "missing.txt"}}); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 1 || reader.File[0].Name != "_clumoove-failures.json" {
		t.Fatalf("archive entries = %#v", reader.File)
	}
	stream, err := reader.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	manifest, err := io.ReadAll(stream)
	if err != nil || !bytes.Contains(manifest, []byte(ErrFilesProviderUnavailable)) || bytes.Contains(manifest, []byte("missing.txt")) {
		t.Fatalf("manifest = %q, err = %v", manifest, err)
	}
}
