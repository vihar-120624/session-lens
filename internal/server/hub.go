package server

import (
	"sync"

	"github.com/viharshah/session-lens/internal/db"
)

// Hub fans out newly-ingested sessions to all active SSE subscribers.
// Design: mutex-guarded map of channels.  Each subscriber gets a buffered
// channel (size 8) so a slow reader cannot block Broadcast.
type Hub struct {
	mu          sync.Mutex
	subscribers map[chan db.Session]struct{}
}

// NewHub allocates and returns a ready-to-use Hub.
func NewHub() *Hub {
	return &Hub{
		subscribers: make(map[chan db.Session]struct{}),
	}
}

// Subscribe registers a new subscriber and returns a receive-only channel.
// The caller must call Unsubscribe when done.
func (h *Hub) Subscribe() <-chan db.Session {
	ch := make(chan db.Session, 8)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes the channel and closes it.
func (h *Hub) Unsubscribe(ch <-chan db.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// We stored the bidirectional chan as the map key, so we need to cast.
	for k := range h.subscribers {
		if k == ch {
			delete(h.subscribers, k)
			close(k)
			return
		}
	}
}

// Broadcast sends session s to every registered subscriber.
// Non-blocking: if a subscriber's buffer is full the event is dropped for
// that subscriber only (SSE clients that fall behind are skipped, not
// blocking the ingest path).
func (h *Hub) Broadcast(s db.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers {
		select {
		case ch <- s:
		default:
			// Subscriber is slow; drop this event for them.
		}
	}
}
