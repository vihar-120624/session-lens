package server

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a tiny per-IP token bucket used to cap the ingest endpoint.
// The threat model is a misconfigured loop hammering /v1/sessions, not a
// distributed attack — exact precision is not required.
//
// Each IP gets a bucket holding up to `burst` tokens that refills at `rate`
// tokens per second. A POST consumes one token; an empty bucket yields 429.
//
// Buckets are pruned lazily after `pruneAfter` of inactivity to bound memory.
type rateLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*bucket
	rate       float64       // tokens per second
	burst      float64       // bucket capacity
	pruneAfter time.Duration // evict IPs unused for this long
	now        func() time.Time
}

type bucket struct {
	tokens   float64
	updated  time.Time
	lastSeen time.Time
}

func newRateLimiter(ratePerSec, burst float64) *rateLimiter {
	return &rateLimiter{
		buckets:    make(map[string]*bucket),
		rate:       ratePerSec,
		burst:      burst,
		pruneAfter: 10 * time.Minute,
		now:        time.Now,
	}
}

// Allow returns true if the request from key may proceed.
func (rl *rateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: rl.burst, updated: now}
		rl.buckets[key] = b
	} else {
		elapsed := now.Sub(b.updated).Seconds()
		b.tokens += elapsed * rl.rate
		if b.tokens > rl.burst {
			b.tokens = rl.burst
		}
		b.updated = now
	}
	b.lastSeen = now

	if b.tokens >= 1 {
		b.tokens -= 1
		rl.maybePrune(now)
		return true
	}
	rl.maybePrune(now)
	return false
}

// maybePrune drops idle buckets. Called under mu.
func (rl *rateLimiter) maybePrune(now time.Time) {
	if len(rl.buckets) < 64 {
		return
	}
	for k, b := range rl.buckets {
		if now.Sub(b.lastSeen) > rl.pruneAfter {
			delete(rl.buckets, k)
		}
	}
}

// clientIP returns the remote IP for r, stripping the port.
// Trusts r.RemoteAddr only — the server binds to 127.0.0.1 by default so
// proxy headers are not honoured.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
