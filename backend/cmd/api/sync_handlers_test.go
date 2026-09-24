package main

import "testing"

func TestSyncMkdirPath(t *testing.T) {
	tests := []struct {
		name       string
		parent     string
		folderName string
		want       string
		valid      bool
	}{
		{name: "root", parent: "/", folderName: "Documents", want: "/Documents", valid: true},
		{name: "nested parent", parent: "/Team/Projects", folderName: "2026", want: "/Team/Projects/2026", valid: true},
		{name: "empty parent is root", folderName: "Documents", want: "/Documents", valid: true},
		{name: "normalizes harmless dot segment", parent: "/Team/./Projects", folderName: "2026", want: "/Team/Projects/2026", valid: true},
		{name: "rejects relative parent", parent: "Team", folderName: "Documents"},
		{name: "rejects parent traversal", parent: "/Team/../Private", folderName: "Documents"},
		{name: "rejects separator in name", parent: "/", folderName: "Team/Private"},
		{name: "rejects traversal name", parent: "/", folderName: ".."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, valid := syncMkdirPath(tt.parent, tt.folderName)
			if valid != tt.valid || got != tt.want {
				t.Fatalf("syncMkdirPath(%q, %q) = (%q, %v), want (%q, %v)", tt.parent, tt.folderName, got, valid, tt.want, tt.valid)
			}
		})
	}
}
