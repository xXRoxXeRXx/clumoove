package version

import "strings"

// Version specifies the current release version of Clumoove.
// It can be overridden at build time using:
// -ldflags "-X backend/internal/version.Version=x.y.z"
var Version = "0.18.0"

// UserAgent returns the standardized HTTP User-Agent header for outgoing requests.
// Format: "Clumoove/<version>" (or "Clumoove/dev" if Version is empty).
func UserAgent() string {
	v := strings.TrimSpace(Version)
	if v == "" {
		return "Clumoove/dev"
	}
	return "Clumoove/" + v
}
