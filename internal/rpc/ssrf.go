// Package rpc: ssrf.go enforces an SSRF allow-list on operator-supplied RPC
// endpoints. The RegisterNode handler calls IsAllowedURL before persisting
// the endpoint so a malicious operator cannot register a node whose
// "RPC endpoint" points at an internal service (cloud metadata, the local
// store, an admin port, etc).
//
// Caveats:
//   - DNS rebinding is not fully prevented. We resolve at validation time
//     and reject if any IP in the answer falls in a private / loopback /
//     link-local / unique-local range, but a hostile DNS can return a
//     different IP at challenge-time. Defense-in-depth: the http.Client used
//     for outbound calls would also need a custom DialContext to block a
//     resolved-private IP. That follow-up is tracked in the Phase 3 summary.
//   - Operators sometimes legitimately run the service against an internal
//     RPC during local development. ALLOW_PRIVATE_RPC=1 disables the check
//     entirely for that environment.
//
// References: OWASP SSRF cheat sheet; RFC 1918, RFC 4193, RFC 3927, RFC 6890.
package rpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// dnsLookupTimeout caps the per-call DNS resolution budget. Without this, a
// host whose authoritative resolver is slow (or a host string that triggers
// the resolver's search-list expansion) can hang the registration handler
// for seconds — found by the FuzzIsAllowedURL fuzzer with input "http://A".
const dnsLookupTimeout = 1 * time.Second

// privateSuffixes lists DNS labels that historically refer to local /
// corporate networks. Hosts that end with one of these are rejected without
// even resolving — this catches the common ".internal" / ".local" cases.
var privateSuffixes = []string{
	".local",
	".localhost",
	".internal",
	".intranet",
	".corp",
	".home",
	".lan",
}

// privateExactHosts rejects bare hostnames that are universally local.
var privateExactHosts = map[string]struct{}{
	"localhost":     {},
	"localhost.":    {},
	"ip6-localhost": {},
	"ip6-loopback":  {},
}

// IsAllowedURL returns nil if rawURL points at a public http(s) endpoint and
// a non-nil error describing the rejection reason otherwise. The check is
// best-effort: pass ALLOW_PRIVATE_RPC=1 in env to disable for dev.
func IsAllowedURL(rawURL string) error {
	if strings.TrimSpace(os.Getenv("ALLOW_PRIVATE_RPC")) == "1" {
		return nil
	}

	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("scheme %q not allowed (must be http or https)", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return errors.New("missing host")
	}
	hostLower := strings.ToLower(host)

	if _, isLocal := privateExactHosts[hostLower]; isLocal {
		return fmt.Errorf("host %q is local", host)
	}
	for _, suffix := range privateSuffixes {
		if strings.HasSuffix(hostLower, suffix) {
			return fmt.Errorf("host suffix %q is internal", suffix)
		}
	}

	// If the host is already an IP literal, evaluate it directly.
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("ip %s is in a private/reserved range", ip)
		}
		return nil
	}

	// Otherwise resolve and reject if ANY answer falls into a private range.
	// This is a best-effort guard — see package doc comment about DNS
	// rebinding. The lookup is bounded by dnsLookupTimeout so a slow or
	// malicious DNS authority can't hang the registration handler.
	resolveCtx, cancel := context.WithTimeout(context.Background(), dnsLookupTimeout)
	defer cancel()
	addrs, lookupErr := net.DefaultResolver.LookupIPAddr(resolveCtx, host)
	if lookupErr != nil {
		return fmt.Errorf("dns lookup failed: %w", lookupErr)
	}
	if len(addrs) == 0 {
		return errors.New("dns lookup returned no addresses")
	}
	for _, addr := range addrs {
		if isPrivateIP(addr.IP) {
			return fmt.Errorf("host %s resolves to private/reserved ip %s", host, addr.IP)
		}
	}
	return nil
}

// isPrivateIP reports whether ip is in a range we consider non-public:
//   - loopback (127/8, ::1)
//   - link-local (169.254/16, fe80::/10)
//   - RFC1918 private (10/8, 172.16/12, 192.168/16)
//   - RFC4193 unique-local IPv6 (fc00::/7)
//   - the 0.0.0.0/0 unspecified address
//   - 100.64/10 carrier-grade NAT (RFC 6598)
func isPrivateIP(ip net.IP) bool {
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return true
	}
	// RFC 6598 carrier-grade NAT (100.64.0.0/10) — Go's net.IP.IsPrivate
	// covers RFC1918 + RFC4193 but not 6598.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1]&0xC0 == 64 {
			return true
		}
	}
	return false
}
