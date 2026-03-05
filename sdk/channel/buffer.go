package channel

import "sync"

// buffer is a thread-safe FIFO queue for bus-down message buffering.
type buffer struct {
	mu    sync.Mutex
	items []*message
	cap   int
}

func newBuffer(capacity int) *buffer {
	if capacity <= 0 {
		capacity = 256
	}
	return &buffer{
		items: make([]*message, 0, capacity),
		cap:   capacity,
	}
}

func (b *buffer) push(msg *message) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) >= b.cap {
		return false
	}
	b.items = append(b.items, msg)
	return true
}

func (b *buffer) pop() *message {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) == 0 {
		return nil
	}
	msg := b.items[0]
	b.items = b.items[1:]
	return msg
}

func (b *buffer) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

func (b *buffer) drainAll() []*message {
	b.mu.Lock()
	defer b.mu.Unlock()
	msgs := b.items
	b.items = make([]*message, 0, b.cap)
	return msgs
}
