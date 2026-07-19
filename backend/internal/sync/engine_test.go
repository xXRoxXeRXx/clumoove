package sync

import (
	"testing"
)

func TestGetSourceRelPath(t *testing.T) {
	tests := []struct {
		targetPath string
		targetDir  string
		expected   string
	}{
		{
			targetPath: "/backup/folder/file.txt",
			targetDir:  "/backup",
			expected:   "/folder/file.txt",
		},
		{
			targetPath: "/file.txt",
			targetDir:  "/",
			expected:   "/file.txt",
		},
		{
			targetPath: "/backup",
			targetDir:  "/backup",
			expected:   "/",
		},
		{
			targetPath: "/other/file.txt",
			targetDir:  "/backup",
			expected:   "/other/file.txt",
		},
	}

	for _, tt := range tests {
		result := getSourceRelPath(tt.targetPath, tt.targetDir)
		if result != tt.expected {
			t.Errorf("getSourceRelPath(%q, %q) = %q; expected %q", tt.targetPath, tt.targetDir, result, tt.expected)
		}
	}
}
