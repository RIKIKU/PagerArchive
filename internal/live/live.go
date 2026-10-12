// Package live fans new messages out to live-update connections (R32).
// It carries only message IDs; each connection checks a new message against
// its own filter with the same query as the message list (A4).
package live

import "sync"

// Hub is the set of live connections.
type Hub struct {
	mu   sync.Mutex
	subs map[*Sub]struct{}
}

// Sub is one connection's subscription.
type Sub struct {
	C       chan int64    // new message IDs
	Dropped chan struct{} // closed if the connection fell too far behind or its user was signed out
	user    int64
	once    sync.Once
}

func (s *Sub) drop() { s.once.Do(func() { close(s.Dropped) }) }

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[*Sub]struct{}{}} }

// Subscribe adds a connection for a signed-in user.
func (h *Hub) Subscribe(userID int64) *Sub {
	s := &Sub{C: make(chan int64, 64), Dropped: make(chan struct{}), user: userID}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

// Unsubscribe removes a connection.
func (h *Hub) Unsubscribe(s *Sub) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
}

// Publish announces a new message. A connection whose buffer is full is
// dropped, so a slow viewer reconnects and reloads rather than silently
// missing pages (ADR 0001).
func (h *Hub) Publish(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.C <- id:
		default:
			s.drop()
			delete(h.subs, s)
		}
	}
}

// CloseUsers closes every connection of these users, e.g. when they are
// deactivated (R43b).
func (h *Hub) CloseUsers(ids []int64) {
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if want[s.user] {
			s.drop()
			delete(h.subs, s)
		}
	}
}

// Len is the number of connections.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
