// Package live fans out server events to connected browsers.
package live

import "sync"

// Event is one Server-Sent Event. Data is JSON, marshaled once and shared by all clients.
type Event struct {
	Type string
	Data []byte
	Seq  uint64 // story-state version; 0 for events outside that sequence (markets, status)
}

type Sub struct {
	C chan Event
}

type Hub struct {
	mu   sync.Mutex
	subs map[*Sub]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: make(map[*Sub]struct{})}
}

func (h *Hub) Subscribe() *Sub {
	s := &Sub{C: make(chan Event, 512)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

func (h *Hub) Unsubscribe(s *Sub) {
	h.mu.Lock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
		close(s.C)
	}
	h.mu.Unlock()
}

// Publish never blocks. A client too slow to keep up is dropped; its browser
// reconnects on its own and receives a fresh snapshot.
func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.C <- ev:
		default:
			delete(h.subs, s)
			close(s.C)
		}
	}
}

func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
