package server

import (
	"sync"
	"testing"
	"time"

	"github.com/viharshah/session-lens/internal/db"
)

// TestHubBroadcastZeroSubscribers verifies that Broadcast with no subscribers
// does not panic and returns immediately.
func TestHubBroadcastZeroSubscribers(t *testing.T) {
	h := NewHub()
	// Must not panic.
	h.Broadcast(db.Session{ID: "no-subs"})
}

// TestHubSubscriberReceivesBroadcast verifies that a registered subscriber
// receives the session sent by Broadcast within a reasonable time.
func TestHubSubscriberReceivesBroadcast(t *testing.T) {
	h := NewHub()
	ch := h.Subscribe()
	defer h.Unsubscribe(ch)

	want := db.Session{ID: "sub-recv-test", Model: "claude-sonnet"}
	h.Broadcast(want)

	select {
	case got := <-ch:
		if got.ID != want.ID {
			t.Errorf("received ID %q, want %q", got.ID, want.ID)
		}
		if got.Model != want.Model {
			t.Errorf("received Model %q, want %q", got.Model, want.Model)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("subscriber did not receive broadcast within 100ms")
	}
}

// TestHubSlowSubscriberDropsBeyondBuffer verifies the non-blocking drop
// behaviour (hub.go default: branch). After filling the 8-slot buffer, any
// further broadcasts are silently dropped for the slow subscriber; the sender
// does not block and the subscriber's effective backlog never grows past
// capacity.
func TestHubSlowSubscriberDropsBeyondBuffer(t *testing.T) {
	const bufSize = 8
	h := NewHub()
	ch := h.Subscribe()
	defer h.Unsubscribe(ch)

	// Broadcast bufSize+5 events without draining.
	const total = bufSize + 5
	for i := 0; i < total; i++ {
		done := make(chan struct{})
		go func(i int) {
			defer close(done)
			h.Broadcast(db.Session{ID: "slow-" + string(rune('0'+i))})
		}(i)
		select {
		case <-done:
		case <-time.After(10 * time.Millisecond):
			t.Fatalf("Broadcast blocked on event %d — should be non-blocking", i)
		}
	}

	// The channel must not hold more than bufSize items.
	if queued := len(ch); queued > bufSize {
		t.Errorf("subscriber channel has %d queued items, max is %d", queued, bufSize)
	}
}

// TestHubUnsubscribeTwiceIsNoop verifies that calling Unsubscribe a second time
// on the same channel does not panic.
func TestHubUnsubscribeTwiceIsNoop(t *testing.T) {
	h := NewHub()
	ch := h.Subscribe()
	h.Unsubscribe(ch)
	// Second unsubscribe must be a no-op (not panic).
	h.Unsubscribe(ch)
}

// TestHubConcurrentSubscribeUnsubscribe stress-tests concurrent Subscribe and
// Unsubscribe from many goroutines. The test itself will fail with a data-race
// report when run with -race if the Hub's locking is broken.
func TestHubConcurrentSubscribeUnsubscribe(t *testing.T) {
	h := NewHub()
	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ch := h.Subscribe()
			h.Broadcast(db.Session{ID: "concurrent"})
			h.Unsubscribe(ch)
		}()
	}
	wg.Wait()
}
