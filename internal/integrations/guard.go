package integrations

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// AllowList is what an admin allows on top of public addresses (R51):
// addresses and ranges, and host names matched exactly.
type AllowList struct {
	Ranges []netip.Prefix
	Hosts  []string // lower case, no trailing dot
}

// ParseAllowList reads entries such as "192.168.1.10", "192.168.1.0/24" and
// "homeassistant.lan".
func ParseAllowList(entries []string) (AllowList, error) {
	var a AllowList
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if p, err := netip.ParsePrefix(e); err == nil {
			a.Ranges = append(a.Ranges, p.Masked())
			continue
		}
		if ip, err := netip.ParseAddr(e); err == nil {
			a.Ranges = append(a.Ranges, netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen()))
			continue
		}
		h := normHost(e)
		if !validHostName(h) {
			return AllowList{}, fmt.Errorf("%q isn't an address, a range (192.168.1.0/24) or a host name", e)
		}
		a.Hosts = append(a.Hosts, h)
	}
	return a, nil
}

// Entries is the list as an admin wrote it, normalised.
func (a AllowList) Entries() []string {
	out := []string{}
	for _, p := range a.Ranges {
		if p.IsSingleIP() {
			out = append(out, p.Addr().String())
		} else {
			out = append(out, p.String())
		}
	}
	return append(out, a.Hosts...)
}

func (a AllowList) hostAllowed(host string) bool { return slices.Contains(a.Hosts, normHost(host)) }

// addrAllowed also allows an IPv6 address that carries an allowed IPv4
// one, since that's where it goes (see embeddedIPv4).
func (a AllowList) addrAllowed(ip netip.Addr) bool {
	v4, embedded := embeddedIPv4(ip)
	for _, p := range a.Ranges {
		if p.Contains(ip) || embedded && p.Contains(v4) {
			return true
		}
	}
	return false
}

func normHost(h string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".") }

func validHostName(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

var (
	sharedSpace   = netip.MustParsePrefix("100.64.0.0/10") // carrier-grade NAT, and Tailscale
	protocolSpace = netip.MustParsePrefix("192.0.0.0/24")  // IETF protocol assignments
	benchmarking  = netip.MustParsePrefix("198.18.0.0/15") // network benchmarking
	reservedSpace = netip.MustParsePrefix("240.0.0.0/4")   // reserved for future use
	nat64         = netip.MustParsePrefix("64:ff9b::/96")  // NAT64: IPv4 in the last 32 bits
	sixToFour     = netip.MustParsePrefix("2002::/16")     // 6to4: IPv4 in bits 16-47
	v4Compatible  = netip.MustParsePrefix("::/96")         // IPv4-compatible: IPv4 in the last 32 bits
)

// embeddedIPv4 is the IPv4 address that a NAT64, 6to4 or IPv4-compatible
// IPv6 address reaches, if ip is one.
func embeddedIPv4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() || ip.Is4In6() {
		return netip.Addr{}, false
	}
	b := ip.As16()
	switch {
	case nat64.Contains(ip), v4Compatible.Contains(ip):
		return netip.AddrFrom4([4]byte(b[12:16])), true
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}
	return netip.Addr{}, false
}

// blockedReason says why an address is refused by default, or "" if it's
// public (R51, ADR 0006 §5).
func blockedReason(ip netip.Addr) string {
	ip = ip.Unmap()
	switch {
	case ip.IsLoopback():
		return "a loopback address"
	case ip.IsPrivate():
		return "a private address"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "a link-local address"
	case ip.IsUnspecified(), ip.Is4() && ip.As4()[0] == 0:
		return "an unspecified address"
	case ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return "a multicast address"
	case ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
		return "a broadcast address"
	case sharedSpace.Contains(ip):
		return "a shared (carrier-grade NAT) address"
	case protocolSpace.Contains(ip):
		return "an IETF protocol address"
	case benchmarking.Contains(ip):
		return "a benchmarking address"
	case reservedSpace.Contains(ip):
		return "a reserved address"
	}
	// An IPv6 address that carries an IPv4 one reaches that IPv4 address, so
	// it's judged by it.
	if v4, ok := embeddedIPv4(ip); ok {
		if reason := blockedReason(v4); reason != "" {
			return reason + " (" + v4.String() + " inside an IPv6 address)"
		}
	}
	return ""
}

// AddressError is a destination that isn't allowed. Its message names the
// address and what to do; it never holds a URL (which can carry secrets).
type AddressError struct {
	Addr   netip.Addr
	Reason string
}

func (e *AddressError) Error() string {
	return fmt.Sprintf("%s is %s. Ask an admin to allow it.", e.Addr, e.Reason)
}

// Resolver looks host names up.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Guard checks every address an integration would reach, when it's saved
// and again when connecting, so DNS can't be changed afterwards to point
// at an internal host (ADR 0006 §5).
type Guard struct {
	// Allow reads the admins' allow list.
	Allow func(ctx context.Context) (AllowList, error)
	// Resolver defaults to net.DefaultResolver.
	Resolver Resolver
	// Restrict, if set, refuses any address it returns an error for, even an
	// allowed one. Tests use it to make sure nothing leaves 127.0.0.0/8.
	Restrict func(netip.Addr) error
}

// ErrLookup is a host name that doesn't resolve.
var ErrLookup = errors.New("the host name doesn't resolve")

// Resolve looks host up and returns its addresses if every one is allowed.
func (g *Guard) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip.Unmap()}
	} else {
		r := g.Resolver
		if r == nil {
			r = net.DefaultResolver
		}
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		found, err := r.LookupNetIP(lctx, "ip", host)
		if err != nil || len(found) == 0 {
			return nil, ErrLookup
		}
		for _, a := range found {
			addrs = append(addrs, a.Unmap())
		}
	}
	var allow AllowList
	if g.Allow != nil {
		var err error
		if allow, err = g.Allow(ctx); err != nil {
			return nil, err
		}
	}
	byName := allow.hostAllowed(host)
	for _, a := range addrs {
		if byName || allow.addrAllowed(a) {
			continue
		}
		if reason := blockedReason(a); reason != "" {
			return nil, &AddressError{Addr: a, Reason: reason}
		}
	}
	if g.Restrict != nil {
		for _, a := range addrs {
			if err := g.Restrict(a); err != nil {
				return nil, err
			}
		}
	}
	return addrs, nil
}

// DialContext connects to addr ("host:port") through a checked address.
// TLS on top still verifies the original host name.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs, err := g.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	var last error
	for _, a := range addrs {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// OnlyLoopback is a Restrict for tests: anything outside 127.0.0.0/8 (or
// ::1) fails, so no test can reach a real service.
func OnlyLoopback(a netip.Addr) error {
	if a.Unmap().IsLoopback() {
		return nil
	}
	return fmt.Errorf("test guard: %s is outside the loopback range", a)
}
