// Package rpc: ssrf_test.go is a table-driven test for IsAllowedURL.
// We exercise the full matrix of accept / reject cases so the SSRF guard
// stays solid as new operators bring new endpoint shapes.
package rpc

import (
	"strings"
	"testing"
)

func TestIsAllowedURL(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantError bool
		errSub    string // if non-empty, error must contain this substring
	}{
		// ---- Reject: bad scheme ----
		{"file scheme rejected", "file:///etc/passwd", true, "scheme"},
		{"javascript scheme rejected", "javascript:alert(1)", true, "scheme"},
		{"ftp scheme rejected", "ftp://example.org/", true, "scheme"},

		// ---- Reject: explicit IPs in private ranges ----
		{"loopback v4", "http://127.0.0.1/", true, "private/reserved"},
		{"loopback v4 explicit port", "http://127.0.0.1:8545/", true, "private/reserved"},
		{"loopback v6", "http://[::1]/", true, "private/reserved"},
		{"rfc1918 10/8", "http://10.0.0.5/", true, "private/reserved"},
		{"rfc1918 172.16/12", "http://172.16.5.10/", true, "private/reserved"},
		{"rfc1918 192.168", "http://192.168.1.1/", true, "private/reserved"},
		{"link-local 169.254", "http://169.254.169.254/", true, "private/reserved"},
		{"unique-local v6 fc00::", "http://[fc00::1]/", true, "private/reserved"},
		{"unspecified 0.0.0.0", "http://0.0.0.0/", true, "private/reserved"},
		{"carrier-grade NAT 100.64", "http://100.64.0.1/", true, "private/reserved"},

		// ---- Reject: well-known internal hostnames / suffixes ----
		{"localhost name", "http://localhost/", true, "local"},
		{"foo.local mDNS", "http://foo.local/", true, "internal"},
		{"corp suffix", "http://api.corp/", true, "internal"},
		{"intranet suffix", "https://node.intranet/", true, "internal"},
		{"home suffix", "http://router.home/", true, "internal"},
		{"lan suffix", "http://node.lan/", true, "internal"},

		// ---- Reject: malformed / missing ----
		{"missing host", "http:///path", true, "host"},

		// ---- Accept: public hosts (resolved at runtime) ----
		// We use 8.8.8.8 — Google public DNS, resolves to itself, well outside
		// any private range. Tests that depend on real DNS are flaky offline,
		// but a literal IP avoids that.
		{"public v4 literal 8.8.8.8", "http://8.8.8.8/", false, ""},
		{"public v4 literal with port", "https://1.1.1.1:443/", false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := IsAllowedURL(tc.input)
			if tc.wantError {
				if err == nil {
					t.Fatalf("IsAllowedURL(%q) = nil; want error containing %q", tc.input, tc.errSub)
				}
				if tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
					t.Errorf("IsAllowedURL(%q) error = %v; expected to contain %q", tc.input, err, tc.errSub)
				}
				return
			}
			if err != nil {
				t.Errorf("IsAllowedURL(%q) = %v; want nil", tc.input, err)
			}
		})
	}
}

// TestIsAllowedURL_AllowPrivateOverride confirms ALLOW_PRIVATE_RPC=1 disables
// the check (used in docker-compose dev environments).
func TestIsAllowedURL_AllowPrivateOverride(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_RPC", "1")
	if err := IsAllowedURL("http://127.0.0.1:8545/"); err != nil {
		t.Errorf("with ALLOW_PRIVATE_RPC=1, expected nil, got %v", err)
	}
}
