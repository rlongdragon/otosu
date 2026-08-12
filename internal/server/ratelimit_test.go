package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/rlongdragon/otosu/internal/store"
)

func serverWithProxies(t *testing.T, cidrs ...string) *Server {
	t.Helper()

	var prefixes []netip.Prefix
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			t.Fatalf("parse %q: %v", c, err)
		}
		prefixes = append(prefixes, p)
	}

	srv, err := New(Options{
		Token:          testToken,
		BaseURL:        "https://otosu.example.com",
		Store:          store.New(store.Options{}),
		TrustedProxies: prefixes,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

func request(remote, forwarded string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/d/ABCDEFGH", nil)
	r.RemoteAddr = remote
	if forwarded != "" {
		r.Header.Set("X-Forwarded-For", forwarded)
	}
	return r
}

// TestClientIPWithoutProxies pins the direct case: the header must be ignored
// entirely, or anyone could forge an address and bypass the rate limiter.
func TestClientIPWithoutProxies(t *testing.T) {
	srv := serverWithProxies(t)

	if got := srv.clientIP(request("203.0.113.9:5000", "1.2.3.4")); got != "203.0.113.9" {
		t.Errorf("clientIP = %q, want the peer address 203.0.113.9", got)
	}
}

func TestClientIPBehindTrustedProxy(t *testing.T) {
	srv := serverWithProxies(t, "127.0.0.1/32")

	for _, tc := range []struct {
		name      string
		remote    string
		forwarded string
		want      string
	}{
		{
			name:      "single hop",
			remote:    "127.0.0.1:44000",
			forwarded: "203.0.113.9",
			want:      "203.0.113.9",
		},
		{
			name:      "client supplied entries are not believed",
			remote:    "127.0.0.1:44000",
			forwarded: "9.9.9.9, 203.0.113.9",
			want:      "203.0.113.9",
		},
		{
			name:      "untrusted peer keeps its own address",
			remote:    "198.51.100.7:44000",
			forwarded: "203.0.113.9",
			want:      "198.51.100.7",
		},
		{
			name:      "trusted peer with no header",
			remote:    "127.0.0.1:44000",
			forwarded: "",
			want:      "127.0.0.1",
		},
		{
			name:      "garbage header falls back to the peer",
			remote:    "127.0.0.1:44000",
			forwarded: "not-an-ip",
			want:      "127.0.0.1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := srv.clientIP(request(tc.remote, tc.forwarded)); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClientIPChainedProxies checks that a request passing through two trusted
// hops still resolves to the original client.
func TestClientIPChainedProxies(t *testing.T) {
	srv := serverWithProxies(t, "127.0.0.1/32", "10.0.0.0/8")

	got := srv.clientIP(request("127.0.0.1:44000", "203.0.113.9, 10.0.0.5"))
	if got != "203.0.113.9" {
		t.Errorf("clientIP = %q, want 203.0.113.9", got)
	}
}

func TestLimiterBlocksAfterBurst(t *testing.T) {
	l := newLimiter(1, 3)

	for i := 0; i < 3; i++ {
		if !l.allow("203.0.113.9") {
			t.Fatalf("request %d was rejected inside the burst", i+1)
		}
	}
	if l.allow("203.0.113.9") {
		t.Error("request past the burst was allowed")
	}
	// A different address has its own bucket.
	if !l.allow("198.51.100.7") {
		t.Error("an unrelated address was rate limited")
	}
}
