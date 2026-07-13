package storage

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// blockPrivateEgress, when true, additionally blocks RFC1918/ULA (private)
// ranges for outbound provider connections. By default it is false: this tool
// exists to migrate between self-hosted / internal servers, so RFC1918 is
// permitted. What is always blocked (the highest-value SSRF targets) is
// loopback and link-local — the latter includes the cloud instance metadata
// endpoint 169.254.169.254.
var blockPrivateEgress = os.Getenv("MIGRATION_BLOCK_PRIVATE") == "1" ||
	strings.EqualFold(os.Getenv("MIGRATION_BLOCK_PRIVATE"), "true")

// validateEgressURL rejects URLs whose host resolves to a blocked (internal)
// address, defending the API server against Server-Side Request Forgery via the
// connect/browse endpoints. Both literal IPs and hostnames are checked, and a
// hostname is resolved and every returned IP is inspected (mitigating the
// trivial DNS-rebinding case where the name points at an internal address).
func validateEgressURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid provider URL: %w", err)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("provider URL has no host")
	}
	return checkHostEgress(host)
}

// validateEgressHost is like validateEgressURL but takes a host/endpoint
// directly (used for the S3 endpoint query parameter).
func validateEgressHost(host string) error {
	if host == "" {
		return fmt.Errorf("empty host")
	}
	return checkHostEgress(host)
}

func checkHostEgress(host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if blocked, reason := isBlockedIP(ip); blocked {
			return fmt.Errorf("egress to %s is not allowed (%s)", host, reason)
		}
		return nil
	}

	// Hostname: resolve and inspect every address. A single internal resolution
	// is enough to reject — that closes the obvious DNS-rebinding vector at
	// construction time.
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("failed to resolve host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("host %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if blocked, reason := isBlockedIP(ip); blocked {
			return fmt.Errorf("egress to %s (%s) is not allowed (%s)", host, ip, reason)
		}
	}
	return nil
}

// isBlockedIP reports whether the address must be denied for outbound
// connections. Loopback and link-local are always blocked; RFC1918/ULA only
// when blockPrivateEgress is enabled.
func isBlockedIP(ip net.IP) (bool, string) {
	if ip.IsLoopback() {
		return true, "loopback"
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true, "link-local"
	}
	if blockPrivateEgress && ip.IsPrivate() {
		return true, "private"
	}
	return false, ""
}
