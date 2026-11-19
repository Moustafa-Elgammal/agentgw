package connector

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlockedAddress is returned (wrapped) when the backend host resolves to an address the
// gateway refuses to connect to.
var ErrBlockedAddress = errors.New("connector: backend address is not allowed")

// blocked lists ranges a studio backend must never be reached on unless the operator opts in
// with AllowPrivate. Loopback, link-local, multicast and unspecified are covered by the
// netip predicates in BlockedIP; these are the ranges those predicates miss.
var blocked = mustPrefixes(
	"0.0.0.0/8",       // "this network"
	"100.64.0.0/10",   // carrier-grade NAT (also used by some cloud metadata services)
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"240.0.0.0/4",     // reserved, includes the broadcast address
	"64:ff9b::/96",    // NAT64: embeds an IPv4 address
	"100::/64",        // discard-only
	"2001::/32",       // Teredo
	"2001:db8::/32",   // documentation
	"2002::/16",       // 6to4: embeds an IPv4 address
)

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, p := range s {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

// BlockedIP reports whether the gateway refuses to connect to ip: loopback, private (RFC 1918
// and unique local), link-local (this includes the 169.254.169.254 metadata address),
// multicast, unspecified, carrier-grade NAT and other special-purpose ranges. IPv4-mapped
// IPv6 addresses are judged as the IPv4 address they carry.
func BlockedIP(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// safeControl runs on the address the dialer is about to connect to, after DNS resolution, so a
// hostname that resolves to a public address when checked and a private one when used (DNS
// rebinding) is still refused: the check is on the connected IP itself.
func safeControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBlockedAddress, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %q is not an IP address", ErrBlockedAddress, host)
	}
	if BlockedIP(ip) {
		return fmt.Errorf("%w: %s (%s)", ErrBlockedAddress, ip.Unmap(), network)
	}
	return nil
}

// Option configures NewHTTP.
type Option func(*HTTP)

// AllowPrivate lets the connector reach loopback and private addresses. Use it only when the
// backend legitimately lives on a private network (a compose network, a cluster service) and
// never for a hostname an untrusted party controls. Tests that use httptest need it too.
func AllowPrivate() Option {
	return func(c *HTTP) { c.Client = newClient(false) }
}

// newClient builds the HTTP client. It never uses a proxy from the environment, and it never
// follows a redirect: a 3xx from the backend is returned as is (the connector then reports a
// backend error), so a compromised or misconfigured backend cannot bounce a signed request to
// another host.
func newClient(safe bool) *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	if safe {
		d.Control = safeControl
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	return &http.Client{
		Timeout:       10 * time.Second,
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
