package middleware

import (
	"net/http"
	"testing"
)

// TestClientIPResistsSpoofing guards H2: a client that prepends forged
// X-Forwarded-For entries must not be able to change the IP we key rate limits on.
func TestClientIPResistsSpoofing(t *testing.T) {
	t.Cleanup(func() { SetTrustedProxies(1) })

	cases := []struct {
		name    string
		trusted int
		xff     string
		remote  string
		want    string
	}{
		// One LB appends the real peer to the client's forged value → rightmost wins.
		{"one proxy, forged prefix", 1, "1.2.3.4, 203.0.113.9", "10.0.0.1:5000", "203.0.113.9"},
		// LB replaces XFF with just the client.
		{"one proxy, single entry", 1, "203.0.113.9", "10.0.0.1:5000", "203.0.113.9"},
		// Two trusted proxies → second-from-right is the client.
		{"two proxies", 2, "9.9.9.9, 203.0.113.9, 10.0.0.2", "10.0.0.1:5000", "203.0.113.9"},
		// No XFF → direct peer, port stripped.
		{"no xff", 1, "", "198.51.100.7:44321", "198.51.100.7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetTrustedProxies(tc.trusted)
			r, _ := http.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := ClientIP(r); got != tc.want {
				t.Errorf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
