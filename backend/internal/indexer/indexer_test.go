package indexer

import (
	"os"
	"testing"
	"time"
)

func TestSanitizeErrorRedactsCredentials(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			"dial tcp 10.0.0.5:443: connect: connection refused https://user:pass@10.0.0.5/remote.php/dav",
			"dial tcp 10.0.0.5:443: connect: connection refused https://***:***@10.0.0.5/remote.php/dav",
		},
		{
			"failed to connect to ftp://alice:secret@host.example.com/path",
			"failed to connect to ftp://***:***@host.example.com/path",
		},
		{
			"no credentials here, just a plain message",
			"no credentials here, just a plain message",
		},
		{
			"https://user:pass@host WITH trailing text and https://a:b@x/y",
			"https://***:***@host WITH trailing text and https://***:***@x/y",
		},
	}
	for _, c := range cases {
		got := sanitizeError(c.in)
		if got != c.want {
			t.Errorf("sanitizeError(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeErrorLeavesSchemeAndHost(t *testing.T) {
	in := "error contacting https://user:pass@db.internal:8080/dav/files"
	got := sanitizeError(in)
	if got == in {
		t.Errorf("expected credentials to be redacted, got %q", got)
	}
	// Scheme and host must be preserved for diagnostics.
	if !contains(got, "https://") || !contains(got, "db.internal:8080") {
		t.Errorf("expected scheme/host preserved, got %q", got)
	}
}

func TestIndexingTimeoutDefault(t *testing.T) {
	old := os.Getenv("INDEXING_TIMEOUT_MINUTES")
	_ = os.Unsetenv("INDEXING_TIMEOUT_MINUTES")
	defer func() { _ = os.Setenv("INDEXING_TIMEOUT_MINUTES", old) }()

	if d := indexingTimeout(); d != 20*time.Minute {
		t.Errorf("expected default 20m, got %v", d)
	}
}

func TestIndexingTimeoutFromEnv(t *testing.T) {
	old := os.Getenv("INDEXING_TIMEOUT_MINUTES")
	_ = os.Setenv("INDEXING_TIMEOUT_MINUTES", "5")
	defer func() { _ = os.Setenv("INDEXING_TIMEOUT_MINUTES", old) }()

	if d := indexingTimeout(); d != 5*time.Minute {
		t.Errorf("expected 5m from env, got %v", d)
	}
}

func TestIndexingTimeoutInvalidEnv(t *testing.T) {
	old := os.Getenv("INDEXING_TIMEOUT_MINUTES")
	_ = os.Setenv("INDEXING_TIMEOUT_MINUTES", "not-a-number")
	defer func() { _ = os.Setenv("INDEXING_TIMEOUT_MINUTES", old) }()

	if d := indexingTimeout(); d != 20*time.Minute {
		t.Errorf("expected default 20m for invalid env, got %v", d)
	}
}

func TestMarshalString(t *testing.T) {
	got := marshalString("hello \"world\"")
	want := `"hello \"world\""`
	if got != want {
		t.Errorf("marshalString = %q, want %q", got, want)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
