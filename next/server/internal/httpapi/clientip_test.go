package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestLimiterKey(t *testing.T) {
	cases := []struct {
		name    string
		trusted []string
		peer    string
		xff     []string
		want    string
	}{
		{name: "no proxies configured ignores header", peer: "203.0.113.9:4000", xff: []string{"198.51.100.7"}, want: "203.0.113.9"},
		{name: "untrusted peer header ignored", trusted: []string{"10.0.0.0/8"}, peer: "203.0.113.9:4000", xff: []string{"198.51.100.7"}, want: "203.0.113.9"},
		{name: "trusted peer uses appended hop", trusted: []string{"192.168.123.20"}, peer: "192.168.123.20:5000", xff: []string{"198.51.100.7"}, want: "198.51.100.7"},
		{name: "client spoofed left part ignored", trusted: []string{"192.168.123.20"}, peer: "192.168.123.20:5000", xff: []string{"1.2.3.4, 198.51.100.7"}, want: "198.51.100.7"},
		{name: "skips chained trusted proxies", trusted: []string{"192.168.123.0/24", "10.1.1.1"}, peer: "192.168.123.20:5000", xff: []string{"198.51.100.7, 10.1.1.1"}, want: "198.51.100.7"},
		{name: "multiple header lines are joined", trusted: []string{"192.168.123.20"}, peer: "192.168.123.20:5000", xff: []string{"9.9.9.9", "198.51.100.8"}, want: "198.51.100.8"},
		{name: "all hops trusted falls back to peer", trusted: []string{"192.168.123.0/24"}, peer: "192.168.123.20:5000", xff: []string{"192.168.123.21"}, want: "192.168.123.20"},
		{name: "malformed hop falls back to peer", trusted: []string{"192.168.123.20"}, peer: "192.168.123.20:5000", xff: []string{"not-an-ip"}, want: "192.168.123.20"},
		{name: "trusted peer without header", trusted: []string{"192.168.123.20"}, peer: "192.168.123.20:5000", want: "192.168.123.20"},
		{name: "ipv6 client", trusted: []string{"192.168.123.20"}, peer: "192.168.123.20:5000", xff: []string{"2001:db8::5"}, want: "2001:db8::5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if invalid := SetTrustedProxies(tc.trusted); len(invalid) > 0 {
				t.Fatalf("unexpected invalid entries %v", invalid)
			}
			t.Cleanup(func() { SetTrustedProxies(nil) })
			req := httptest.NewRequest("POST", "/api/auth/login", nil)
			req.RemoteAddr = tc.peer
			for _, value := range tc.xff {
				req.Header.Add("X-Forwarded-For", value)
			}
			if got := clientIP(req); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSetTrustedProxiesReportsInvalid(t *testing.T) {
	t.Cleanup(func() { SetTrustedProxies(nil) })
	invalid := SetTrustedProxies([]string{"10.0.0.0/8", "nope", " ", "192.168.1.1"})
	if len(invalid) != 1 || invalid[0] != "nope" {
		t.Fatalf("invalid = %v", invalid)
	}
}
