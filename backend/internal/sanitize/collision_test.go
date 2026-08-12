package sanitize

import (
	"context"
	"testing"
	"time"

	"backend/internal/storage"
)

func TestCheckCaseCollision_NoCollision(t *testing.T) {
	mock := &mockProvider{
		files: map[string][]storage.CloudResource{
			"/target": {
				{Path: "/target/file.txt", Name: "file.txt", Size: 100, LastModified: time.Now()},
				{Path: "/target/other.pdf", Name: "other.pdf", Size: 200, LastModified: time.Now()},
			},
		},
	}

	collision, err := CheckCaseCollision(context.Background(), mock, "files", "/target", "new.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if collision != "" {
		t.Errorf("expected no collision, got %q", collision)
	}
}

func TestCheckCaseCollision_WithCollision(t *testing.T) {
	mock := &mockProvider{
		files: map[string][]storage.CloudResource{
			"/target": {
				{Path: "/target/Foto.jpg", Name: "Foto.jpg", Size: 100, LastModified: time.Now()},
				{Path: "/target/other.pdf", Name: "other.pdf", Size: 200, LastModified: time.Now()},
			},
		},
	}

	collision, err := CheckCaseCollision(context.Background(), mock, "files", "/target", "foto.jpg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if collision != "Foto.jpg" {
		t.Errorf("expected collision with Foto.jpg, got %q", collision)
	}
}

func TestCheckCaseCollision_ExactMatch(t *testing.T) {
	mock := &mockProvider{
		files: map[string][]storage.CloudResource{
			"/target": {
				{Path: "/target/file.txt", Name: "file.txt", Size: 100, LastModified: time.Now()},
			},
		},
	}

	collision, err := CheckCaseCollision(context.Background(), mock, "files", "/target", "file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if collision != "" {
		t.Errorf("expected no collision for exact match, got %q", collision)
	}
}

func TestCheckCaseCollision_SkipsDirectories(t *testing.T) {
	mock := &mockProvider{
		files: map[string][]storage.CloudResource{
			"/target": {
				{Path: "/target/Foto", Name: "Foto", IsDir: true, LastModified: time.Now()},
			},
		},
	}

	collision, err := CheckCaseCollision(context.Background(), mock, "files", "/target", "foto")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if collision != "" {
		t.Errorf("expected no collision for directory, got %q", collision)
	}
}

func TestResolveCollision_NoExistingFiles(t *testing.T) {
	mock := &mockProvider{
		files: map[string][]storage.CloudResource{},
	}

	resolved, err := ResolveCollision(context.Background(), mock, "files", "/target", "file.txt", "dropbox")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != "file_1.txt" {
		t.Errorf("expected file_1.txt, got %q", resolved)
	}
}

func TestResolveCollision_WithExistingFile(t *testing.T) {
	mock := &mockProvider{
		files: map[string][]storage.CloudResource{
			"/target": {
				{Path: "/target/file_1.txt", Name: "file_1.txt", Size: 100, LastModified: time.Now()},
			},
		},
	}

	resolved, err := ResolveCollision(context.Background(), mock, "files", "/target", "file.txt", "nextcloud")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != "file_2.txt" {
		t.Errorf("expected file_2.txt, got %q", resolved)
	}
}

func TestResolveCollision_CaseInsensitive(t *testing.T) {
	mock := &mockProvider{
		files: map[string][]storage.CloudResource{
			"/target": {
				{Path: "/target/File_1.txt", Name: "File_1.txt", Size: 100, LastModified: time.Now()},
			},
		},
	}

	resolved, err := ResolveCollision(context.Background(), mock, "files", "/target", "file.txt", "dropbox")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != "file_2.txt" {
		t.Errorf("expected file_2.txt, got %q", resolved)
	}
}

func TestResolveCollision_NoExtension(t *testing.T) {
	mock := &mockProvider{
		files: map[string][]storage.CloudResource{
			"/target": {
				{Path: "/target/README_1", Name: "README_1", Size: 100, LastModified: time.Now()},
			},
		},
	}

	resolved, err := ResolveCollision(context.Background(), mock, "files", "/target", "README", "nextcloud")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != "README_2" {
		t.Errorf("expected README_2, got %q", resolved)
	}
}
