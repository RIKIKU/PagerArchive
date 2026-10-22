// Package live fans new messages out to live-update connections (R32).
// It carries only message IDs; each connection checks a new message against
// its own filter with the same query as the message list (A4).
package live

import "sync"

// Why a connection was dropped, for the stream to tell its client (ADR 0010 §2).
const (
	// Behind: the connection fell too far behind.
	Behind = "behind"
	// Account: the user was signed out, or their role changed, so what they
	// may see changed.
	Account = "account"
)

// Hub is the set of live connections.
type Hub struct {
	mu   sync.Mutex
	subs map[*Sub]struct{}
}

// Sub is one connection's subscription.
type Sub struct {
	C       chan int64    // new message IDs, in the order they were published
	Dropped chan struct{} // closed when the hub drops the connection; Reason says why
	user    int64
	once    sync.Once
	reason  string
}

func (s *Sub) drop(reason string) {
	s.once.Do(func() {
		s.reason = reason
		close(s.Dropped)
	})
}

// Reason is why the connection was dropped (Behind or Account), once
// Dropped is closed.
func (s *Sub) Reason() string {
	<-s.Dropped
	return s.reason
}

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
// dropped, so a slow viewer reconnects and reloads (or catches up) rather
// than silently missing pages (ADR 0001).
func (h *Hub) Publish(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.C <- id:
		default:
			s.drop(Behind)
			delete(h.subs, s)
		}
	}
}

// CloseUsers closes every connection of these users: they were deactivated
// or deleted (R43b), or their role changed (ADR 0010 §4).
func (h *Hub) CloseUsers(ids []int64) {
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if want[s.user] {
			s.drop(Account)
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
