package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Callers works out who a request came from, for the limits that count per
// address (the sign-in lockout, the TV pairing limit).
//
// The rule: the connection's own address, unless that address is one of
// TRUSTED_PROXY's, in which case the last X-Forwarded-For entry (the address
// the proxy itself saw: Caddy replaces or appends to the header, so its last
// entry is always Caddy's own observation, whatever a visitor sent).
//
// The Python had two different rules: the sign-in lockout trusted the
// header from the container's default gateway, and the pairing limit never
// trusted it at all. Behind Caddy the second meant every TV in the world
// shared Caddy's one address, so ten pair attempts anywhere blocked pairing
// for everyone for ten minutes. One rule, with the proxy named explicitly,
// fixes both.
type Callers struct {
	trusted []netip.Prefix
}

// NewCallers uses the TRUSTED_PROXY prefixes (none: direct connections only).
func NewCallers(trusted []netip.Prefix) *Callers { return &Callers{trusted: trusted} }

// Addr returns the caller's address; invalid when it cannot be told (a
// malformed RemoteAddr, which net/http never produces for TCP).
func (c *Callers) Addr(r *http.Request) netip.Addr {
	peer := remoteAddr(r)
	if !peer.IsValid() || !c.isTrusted(peer) {
		return peer
	}
	fwd := r.Header.Values("X-Forwarded-For")
	if len(fwd) == 0 {
		return peer
	}
	last := fwd[len(fwd)-1]
	if i := strings.LastIndexByte(last, ','); i >= 0 {
		last = last[i+1:]
	}
	a, err := netip.ParseAddr(strings.TrimSpace(last))
	if err != nil {
		return peer // a proxy that sent junk: count the proxy, never the junk
	}
	return a.Unmap()
}

// Bucket is the caller's address as a limit counts it: an IPv4 address as
// is, an IPv6 address by its /64 (one household or one server gets a whole
// /64, so counting single IPv6 addresses would be no limit at all).
func (c *Callers) Bucket(r *http.Request) string {
	a := c.Addr(r)
	if !a.IsValid() {
		return "unknown"
	}
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

func (c *Callers) isTrusted(a netip.Addr) bool {
	for _, p := range c.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}
