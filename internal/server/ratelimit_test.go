package server

import (
	"testing"
	"time"
)

// TestRateLimit_BurstThenBlock seeds the bucket, drains it, and verifies the
// next request is rejected.
func TestRateLimit_BurstThenBlock(t *testing.T) {
	rl := newRateLimiter(10, 3)
	rl.now = func() time.Time { return time.Unix(1_700_000_000, 0) }

	for i := 0; i < 3; i++ {
		if !rl.Allow("1.2.3.4") {
			t.Fatalf("burst slot %d should be allowed", i)
		}
	}
	if rl.Allow("1.2.3.4") {
		t.Fatal("4th request should be blocked")
	}
	// Different IP still gets a fresh bucket.
	if !rl.Allow("5.6.7.8") {
		t.Fatal("second IP should be allowed independently")
	}
}

// TestRateLimit_Refill: advancing time refills the bucket at `rate` tokens/sec.
func TestRateLimit_Refill(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	rl := newRateLimiter(2, 2) // 2 tokens/sec, burst 2
	rl.now = func() time.Time { return now }

	if !rl.Allow("x") || !rl.Allow("x") {
		t.Fatal("initial burst should be allowed")
	}
	if rl.Allow("x") {
		t.Fatal("third request should be blocked at t=0")
	}
	// Advance 0.6s → bucket gains 1.2 tokens → one more allowed.
	now = now.Add(600 * time.Millisecond)
	if !rl.Allow("x") {
		t.Fatal("should refill after 600ms")
	}
}
