package server

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/32"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("10.0.0.0/8"),
	}
	cases := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"untrusted remote, spoofed XFF", "203.0.113.9:1234", []string{"1.2.3.4"}, "203.0.113.9"},
		{"untrusted remote, no XFF", "203.0.113.9:1234", nil, "203.0.113.9"},
		{"trusted single hop", "127.0.0.1:5555", []string{"198.51.100.7"}, "198.51.100.7"},
		{"trusted, no XFF", "127.0.0.1:5555", nil, "127.0.0.1"},
		{"multiple hops, rightmost untrusted wins", "127.0.0.1:1", []string{"1.1.1.1, 198.51.100.7, 10.0.0.5"}, "198.51.100.7"},
		{"spoof left of real client", "127.0.0.1:1", []string{"6.6.6.6", "198.51.100.7"}, "198.51.100.7"},
		{"multiple headers", "127.0.0.1:1", []string{"1.1.1.1", "2.2.2.2, 10.1.1.1"}, "2.2.2.2"},
		{"all trusted -> leftmost", "127.0.0.1:1", []string{"10.0.0.1, 10.0.0.2"}, "10.0.0.1"},
		{"IPv6 client", "[::1]:443", []string{"2001:db8::7"}, "2001:db8::7"},
		{"IPv6 remote untrusted", "[2001:db8::9]:443", []string{"1.2.3.4"}, "2001:db8::9"},
		{"mapped XFF", "127.0.0.1:1", []string{"::ffff:198.51.100.8"}, "198.51.100.8"},
		{"mapped remote", "[::ffff:203.0.113.4]:80", nil, "203.0.113.4"},
		{"mapped trusted remote", "[::ffff:127.0.0.1]:80", []string{"198.51.100.1"}, "198.51.100.1"},
		{"garbage entries ignored", "127.0.0.1:1", []string{"garbage, 198.51.100.3, , unknown"}, "198.51.100.3"},
		{"only garbage -> remote", "127.0.0.1:1", []string{"nope, also-nope"}, "127.0.0.1"},
		{"XFF entry with port", "127.0.0.1:1", []string{"198.51.100.4:999"}, "198.51.100.4"},
		{"bracketed IPv6 XFF", "127.0.0.1:1", []string{"[2001:db8::1]"}, "2001:db8::1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			for _, x := range c.xff {
				r.Header.Add("X-Forwarded-For", x)
			}
			got := ClientIP(r, trusted)
			if got.String() != c.want {
				t.Fatalf("got %s want %s", got, c.want)
			}
		})
	}
}

func TestIPFamily(t *testing.T) {
	if ipFamily(netip.MustParseAddr("1.2.3.4")) != "ipv4" || ipFamily(netip.MustParseAddr("::1")) != "ipv6" {
		t.Fatal("family")
	}
}
