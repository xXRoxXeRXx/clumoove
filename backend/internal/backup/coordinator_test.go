package backup

import (
	"database/sql"
	"testing"
)

func TestNewCoordinatorValidatesPackWriterLimit(t *testing.T) {
	for _, limit := range []int{0, 5} {
		if _, err := NewCoordinator(&sql.DB{}, "key", limit); err == nil {
			t.Errorf("NewCoordinator(limit=%d) succeeded", limit)
		}
	}
	coordinator, err := NewCoordinator(&sql.DB{}, "key", 2)
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	if cap(coordinator.packWriterSlots) != 2 {
		t.Fatalf("pack writer capacity = %d, want 2", cap(coordinator.packWriterSlots))
	}
}

func TestRepositoryPath(t *testing.T) {
	tests := []struct {
		name string
		root string
		want string
		err  bool
	}{
		{name: "nested repository", root: "clumoove/repositories/123", want: "clumoove/repositories/123/packs/a.pack"},
		{name: "absolute repository", root: "/backups/123", want: "/backups/123/packs/a.pack"},
		{name: "reject traversal", root: "backups/../other", err: true},
		{name: "reject backslash", root: `backups\other`, err: true},
		{name: "reject empty", root: "", err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repositoryPath(tt.root, "packs", "a.pack")
			if (err != nil) != tt.err {
				t.Fatalf("repositoryPath() error = %v, want error=%v", err, tt.err)
			}
			if got != tt.want {
				t.Fatalf("repositoryPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSafeRemotePath(t *testing.T) {
	for _, value := range []string{"backup/../outside", `backup\outside`, ""} {
		if err := safeRemotePath(value); err == nil {
			t.Errorf("safeRemotePath(%q) succeeded", value)
		}
	}
}

func TestFailureCodeForState(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  string
	}{
		{name: "scan", state: "SCANNING", want: "BACKUP_SCAN_FAILED"},
		{name: "run", state: "RUNNING", want: "BACKUP_RUN_FAILED"},
		{name: "verify", state: "VERIFYING", want: "BACKUP_VERIFICATION_FAILED"},
		{name: "unknown defaults to run", state: "UNKNOWN", want: "BACKUP_RUN_FAILED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := failureCodeForState(test.state); got != test.want {
				t.Fatalf("failureCodeForState(%q) = %q, want %q", test.state, got, test.want)
			}
		})
	}
}
