package server

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// limiter applies a token bucket per client IP.
//
// This is the primary defence against someone enumerating drop codes. It runs
// in process so it reacts immediately, unlike a log scraping tool such as
// fail2ban, which cannot act inside the short window a code stays live.
type limiter struct {
	rate  rate.Limit
	burst int
	idle  time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

func newLimiter(perSecond float64, burst int) *limiter {
	return &limiter{
		rate:    rate.Limit(perSecond),
		burst:   burst,
		idle:    10 * time.Minute,
		buckets: make(map[string]*bucket),
	}
}

func (l *limiter) allow(ip string) bool {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(l.rate, l.burst)}
		l.buckets[ip] = b
	}
	b.seen = now

	// Opportunistic cleanup: the map would otherwise grow with every scanner
	// that ever touches the server.
	if len(l.buckets) > 1024 {
		for k, v := range l.buckets {
			if now.Sub(v.seen) > l.idle {
				delete(l.buckets, k)
			}
		}
	}
	return b.lim.Allow()
}

// middleware rejects requests from clients that exceed the bucket.
func (l *limiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the peer address.
//
// It deliberately ignores X-Forwarded-For. otosu terminates TLS itself, so the
// peer is the real client; trusting a header here would let anyone spoof their
// way around the rate limiter. If you later put a proxy in front, add an
// explicit allowlist of trusted proxy addresses before honouring the header.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
