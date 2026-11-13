package httpx

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestCallersBelieveForwardedForOnlyFromTheProxy(t *testing.T) {
	c := NewCallers([]netip.Prefix{netip.MustParsePrefix("172.18.0.1/32")})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.18.0.1:5000"
	r.Header.Add("X-Forwarded-For", "6.6.6.6, 203.0.113.9") // a visitor's own entry, then Caddy's
	if got := c.Addr(r).String(); got != "203.0.113.9" {
		t.Fatal(got)
	}
	r.Header.Set("X-Forwarded-For", "not an address")
	if got := c.Addr(r).String(); got != "172.18.0.1" {
		t.Fatal("junk:", got)
	}
	r.RemoteAddr = "172.18.0.7:5000" // another container: never believed
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := c.Addr(r).String(); got != "172.18.0.7" {
		t.Fatal(got)
	}
	r.RemoteAddr = "[2001:db8:1:2::5]:5000"
	if got := c.Bucket(r); got != "2001:db8:1:2::/64" {
		t.Fatal(got)
	}
}

func TestBadPath(t *testing.T) {
	for _, p := range []string{"/a/../b", "/a/./b", "/a/..", "/a//b", "/a%2fb", "/a%2Eb", `/a\b`, "/a%5cb"} {
		if !BadPath(p, p) {
			t.Error("allowed:", p)
		}
	}
	for _, p := range []string{"/api/v1/me", "/static/js/app.js", "/x?next=//evil"} {
		path := p
		if i := len("/x"); p[:i] == "/x" {
			path = "/x"
		}
		if BadPath(path, p) {
			t.Error("refused:", p)
		}
	}
}

func TestRouteOf(t *testing.T) {
	for pattern, want := range map[string]string{"GET /api/v1/me/devices/{device_id}": "/api/v1/me/devices/{device_id}", "GET /": "/lists/…"} {
		if got := routeOf(pattern, "/lists/42"); got != want {
			t.Errorf("%s: %s", pattern, got)
		}
	}
}
