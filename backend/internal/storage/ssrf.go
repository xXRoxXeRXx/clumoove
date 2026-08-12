package storage

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// blockPrivateEgress, when true, additionally blocks RFC1918/ULA (private)
// ranges for outbound provider connections. By default it is false: this tool
// exists to migrate between self-hosted / internal servers, so RFC1918 is
// permitted. What is always blocked (the highest-value SSRF targets) is
// loopback and link-local — the latter includes the cloud instance metadata
// endpoint 169.254.169.254.
var blockPrivateEgress atomic.Bool

func init() {
	blockPrivateEgress.Store(os.Getenv("MIGRATION_BLOCK_PRIVATE") == "1" ||
		strings.EqualFold(os.Getenv("MIGRATION_BLOCK_PRIVATE"), "true"))
}

// resolveEgressIPsForDial is replaceable by package tests so they can exercise
// a full HTTP request across a DNS-rebinding boundary without depending on DNS.
// Production always uses resolveEgressIPs.
var resolveEgressIPsForDial = resolveEgressIPs

// validateEgressURL rejects URLs whose host resolves to a blocked (internal)
// address, defending the API server against Server-Side Request Forgery via the
// connect/browse endpoints. Both literal IPs and hostnames are checked, and a
// hostname is resolved and every returned IP is inspected at provider-construction
// time (defense in depth). The decisive check, however, happens per-connection
// inside the transport's DialContext (see egressDialer), which re-resolves and
// re-validates the address immediately before each dial — closing the
// DNS-rebinding (TOCTOU) window that construction-time-only validation leaves open.
func validateEgressURL(rawURL string) error {
	parsed, err := parseEgressURL(rawURL)
	if err != nil {
		return err
	}
	return checkHostEgress(context.Background(), parsed.Hostname())
}

func validateEgressURLContext(ctx context.Context, rawURL string) error {
	parsed, err := parseEgressURL(rawURL)
	if err != nil {
		return err
	}
	return checkHostEgress(ctx, parsed.Hostname())
}

// parseEgressURL deliberately never wraps url.Parse errors. url.Error can
// retain the original URL, including userinfo, and storage-layer errors must
// not expose credentials to callers that later log or map them.
func parseEgressURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid provider URL")
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("provider URL has no host")
	}
	return parsed, nil
}

// validateEgressHost is like validateEgressURL but takes a host/endpoint
// directly (used for the S3 endpoint query parameter). It is also exported so
// other packages (e.g. the email package) can reuse the identical egress
// policy for their own user-supplied hosts.
func validateEgressHost(host string) error {
	return validateEgressHostContext(context.Background(), host)
}

func validateEgressHostContext(ctx context.Context, host string) error {
	if host == "" {
		return fmt.Errorf("empty host")
	}
	return checkHostEgress(ctx, host)
}

// ValidateEgressHostContext is the exported, cancellation-aware entry point
// for the egress policy.
func ValidateEgressHostContext(ctx context.Context, host string) error {
	return validateEgressHostContext(ctx, host)
}

// ValidateEgressHost is the compatibility entry point for callers without a
// request context. New callers should use ValidateEgressHostContext.
func ValidateEgressHost(host string) error {
	return ValidateEgressHostContext(context.Background(), host)
}

// NewEgressHTTPClient returns an HTTP client that validates the requested URL
// and re-validates its hostname immediately before every TCP connection.
// Redirects are intentionally not followed: a user-configured endpoint must
// not be able to select a second egress destination via a response header.
// It is intended for user-configured webhook-style endpoints.
func NewEgressHTTPClient(rawURL string) (*http.Client, error) {
	u, err := parseEgressURL(rawURL)
	if err != nil {
		return nil, err
	}
	if err := checkHostEgress(context.Background(), u.Hostname()); err != nil {
		return nil, err
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	// Provider transports intentionally do not honor environment proxies: a
	// proxy dial target cannot be host-pinned by egressDialer. Disable inherited
	// ProxyFromEnvironment so this client matches the other provider clients.
	transport.Proxy = nil
	transport.DialContext = egressDialer(u.Hostname())
	return &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: rejectEgressRedirect}, nil
}

// NewEgressStreamingHTTPClient returns an SSRF-protected HTTP client for
// long-running streamed transfers. Unlike NewEgressHTTPClient, it deliberately
// has no total request deadline: a healthy large upload must be allowed to run
// for longer than a small control-plane request. Connection setup is still
// bounded by the transport defaults and the server must produce its response
// headers within five minutes after the request body has been sent.
func NewEgressStreamingHTTPClient(rawURL string) (*http.Client, error) {
	u, err := parseEgressURL(rawURL)
	if err != nil {
		return nil, err
	}
	if err := checkHostEgress(context.Background(), u.Hostname()); err != nil {
		return nil, err
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.Proxy = nil
	transport.DialContext = egressDialer(u.Hostname())
	transport.ResponseHeaderTimeout = 5 * time.Minute
	return &http.Client{Transport: transport, Timeout: 0, CheckRedirect: rejectEgressRedirect}, nil
}

// rejectEgressRedirect returns the redirect response without issuing a request
// to its Location target. This prevents redirects from changing an egress
// destination after the original user-supplied URL has been validated.
func rejectEgressRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
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
func egressDialer(configuredHost string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	configuredIP := net.ParseIP(configuredHost)

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("egress: invalid dial address %q: %w", addr, err)
		}

		// A literal endpoint was checked by provider construction. Never let a
		// request select a different literal address, even if a caller enables
		// redirect following accidentally.
		if ip := net.ParseIP(host); ip != nil {
			if configuredIP != nil {
				if !configuredIP.Equal(ip) {
					return nil, fmt.Errorf("egress: literal dial address %q does not match configured endpoint", host)
				}
				addr = net.JoinHostPort(configuredIP.String(), port)
			} else {
				return nil, fmt.Errorf("egress: literal dial address %q forbidden for hostname endpoint %q", host, configuredHost)
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}
		if !strings.EqualFold(host, configuredHost) {
			return nil, fmt.Errorf("egress: dial host %q does not match configured endpoint %q", host, configuredHost)
		}

		ips, err := resolveEgressIPsForDial(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("egress: failed to resolve %q: %w", host, err)
		}

		sortIPsIPv4First(ips)

		var errMsgs []string
		for _, ip := range ips {
			if blocked, reason := isBlockedIP(ip); blocked {
				errMsgs = append(errMsgs, fmt.Sprintf("%s blocked (%s)", ip, reason))
				continue
			}
			target := net.JoinHostPort(ip.String(), port)
			dialNetwork := network
			if ip.To4() != nil {
				dialNetwork = "tcp4"
			} else {
				dialNetwork = "tcp6"
			}
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			conn, err := dialer.DialContext(ctx, dialNetwork, target)
			if err != nil {
				errMsgs = append(errMsgs, fmt.Sprintf("%s (%s): %v", target, dialNetwork, err))
				continue
			}
			return conn, nil
		}
		if len(errMsgs) > 0 {
			return nil, fmt.Errorf("egress connection to %s failed: %s", host, strings.Join(errMsgs, " | "))
		}
		return nil, fmt.Errorf("egress: host %q resolved to no dialable addresses", host)
	}
}

func sortIPsIPv4First(ips []net.IP) {
	sort.SliceStable(ips, func(i, j int) bool {
		return ips[i].To4() != nil && ips[j].To4() == nil
	})
}

// resolveEgressIPs resolves a hostname with the caller's context, placing IPv4
// addresses first to ensure reliable dual-stack fallback on environments where
// IPv6 routing may be unavailable.
func resolveEgressIPs(ctx context.Context, host string) ([]net.IP, error) {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ips = append(ips, address.IP)
	}
	sortIPsIPv4First(ips)
	return ips, nil
}

// checkHostEgress rejects hosts that resolve to blocked addresses. Loopback and
// link-local addresses are always blocked; RFC1918/ULA ranges are blocked when
// MIGRATION_BLOCK_PRIVATE is enabled.
func checkHostEgress(ctx context.Context, host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if blocked, reason := isBlockedIP(ip); blocked {
			return fmt.Errorf("egress to %s is not allowed (%s)", host, reason)
		}
		return nil
	}

	// Hostname: resolve and inspect every address. This is defense in
	// depth alongside the per-connection re-validation in egressDialer.
	ips, err := resolveEgressIPsForDial(ctx, host)
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
	if blockPrivateEgress.Load() && ip.IsPrivate() {
		return true, "private"
	}
	return false, ""
}
