package version

import "testing"

func TestUserAgent(t *testing.T) {
	orig := Version
	defer func() { Version = orig }()

	Version = "0.18.0"
	if got := UserAgent(); got != "Clumoove/0.18.0" {
		t.Errorf("UserAgent() = %q, want %q", got, "Clumoove/0.18.0")
	}

	Version = "  "
	if got := UserAgent(); got != "Clumoove/dev" {
		t.Errorf("UserAgent() = %q, want %q", got, "Clumoove/dev")
	}

	Version = ""
	if got := UserAgent(); got != "Clumoove/dev" {
		t.Errorf("UserAgent() = %q, want %q", got, "Clumoove/dev")
	}
}
