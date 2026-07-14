package storage

import (
	"context"
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
// hostname is resolved and every returned IP is inspected at provider-construction
// time (defense in depth). The decisive check, however, happens per-connection
// inside the transport's DialContext (see egressDialer), which re-resolves and
// re-validates the address immediately before each dial — closing the
// DNS-rebinding (TOCTOU) window that construction-time-only validation leaves open.
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
// directly (used for the S3 endpoint query parameter). It is also exported so
// other packages (e.g. the email package) can reuse the identical egress
// policy for their own user-supplied hosts.
func validateEgressHost(host string) error {
	if host == "" {
		return fmt.Errorf("empty host")
	}
	return checkHostEgress(host)
}

// ValidateEgressHost is the exported entry point for the egress policy. See
// validateEgressHost.
func ValidateEgressHost(host string) error {
	return validateEgressHost(host)
}

// egressDialer returns a DialContext that pins egress to a validated address.
// For a hostname it re-resolves on every connection and dials only an address
// that passes the SSRF checks, closing the DNS-rebinding (TOCTOU) window: the
// IP that is actually dialed is the one validated, immediately before the
// connection is opened. (Construction-time validation alone is defeatable by
// flipping the DNS record between validation and connect.)
//
// The original hostname MUST remain in the request URL so that net/http keeps
// using it as the TLS ServerName (SNI) and certificate-validation target.
// Because the transport derives ServerName from the request URL's host — not from
// the address we dial — certificate verification still targets the real hostname
// while the TCP connection goes to the validated IP.
func egressDialer(host string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		// Literal IP: already validated at construction; dial directly.
		if net.ParseIP(host) != nil {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}

		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			// addr unexpectedly lacked a port; fall back to the host directly.
			port = ""
		}

		ips, err := net.LookupIP(host)
		if err != nil {
			return nil, fmt.Errorf("egress: failed to resolve %q: %w", host, err)
		}

		var lastErr error
		for _, ip := range ips {
			if blocked, reason := isBlockedIP(ip); blocked {
				lastErr = fmt.Errorf("egress to %s (%s) is not allowed (%s)", host, ip, reason)
				continue
			}
			target := net.JoinHostPort(ip.String(), port)
			var d net.Dialer
			conn, err := d.DialContext(ctx, network, target)
			if err != nil {
				lastErr = err
				continue
			}
			return conn, nil
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("egress: host %q resolved to no dialable addresses", host)
	}
}

func checkHostEgress(host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if blocked, reason := isBlockedIP(ip); blocked {
			return fmt.Errorf("egress to %s is not allowed (%s)", host, reason)
		}
		return nil
	}

	// Hostname: resolve and inspect every address. This is defense in
	// depth alongside the per-connection re-validation in egressDialer.
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
