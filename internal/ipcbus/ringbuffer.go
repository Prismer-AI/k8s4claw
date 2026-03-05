package ipcbus

import (
	"fmt"
	"sync"
)

// BackpressureState represents the current backpressure state of a ring buffer.
type BackpressureState int

const (
	// StateNormal indicates the buffer has capacity and producers may continue.
	StateNormal BackpressureState = iota
	// StateSlowDown indicates the buffer is filling up and producers should throttle.
	StateSlowDown
)

// String returns a human-readable label for the backpressure state.
func (s BackpressureState) String() string {
	switch s {
	case StateNormal:
		return "normal"
	case StateSlowDown:
		return "slow_down"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// RingBuffer is a thread-safe circular buffer for *Message with backpressure
// watermark transitions. When the fill ratio reaches highMark the state
// transitions to StateSlowDown; when it drops to lowMark it transitions back
// to StateNormal.
type RingBuffer struct {
	mu       sync.Mutex
	buf      []*Message
	size     int
	head     int // next read position
	tail     int // next write position
	count    int
	highMark float64
	lowMark  float64
	state    BackpressureState
}

// NewRingBuffer creates a ring buffer with the given capacity and watermark
// thresholds. Zero/invalid values are replaced with defaults: size=1024,
// highWatermark=0.8, lowWatermark=0.3.
func NewRingBuffer(size int, highWatermark, lowWatermark float64) *RingBuffer {
	if size <= 0 {
		size = 1024
	}
	if highWatermark <= 0 || highWatermark > 1.0 {
		highWatermark = 0.8
	}
	if lowWatermark <= 0 || lowWatermark >= highWatermark {
		lowWatermark = 0.3
	}
	return &RingBuffer{
		buf:      make([]*Message, size),
		size:     size,
		highMark: highWatermark,
		lowMark:  lowWatermark,
	}
}

// Push adds a message to the buffer. It returns ok=false if the buffer is full.
// stateChanged is true when the backpressure state transitions (Normal -> SlowDown).
func (rb *RingBuffer) Push(msg *Message) (ok bool, stateChanged bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.count == rb.size {
		return false, false
	}

	rb.buf[rb.tail] = msg
	rb.tail = (rb.tail + 1) % rb.size
	rb.count++

	ratio := float64(rb.count) / float64(rb.size)
	if rb.state == StateNormal && ratio >= rb.highMark {
		rb.state = StateSlowDown
		stateChanged = true
	}

	return true, stateChanged
}

// Pop removes and returns the oldest message. It returns nil if the buffer is
// empty. stateChanged is true when the backpressure state transitions
// (SlowDown -> Normal).
func (rb *RingBuffer) Pop() (msg *Message, stateChanged bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.count == 0 {
		return nil, false
	}

	msg = rb.buf[rb.head]
	rb.buf[rb.head] = nil // avoid holding reference
	rb.head = (rb.head + 1) % rb.size
	rb.count--

	ratio := float64(rb.count) / float64(rb.size)
	if rb.state == StateSlowDown && ratio <= rb.lowMark {
		rb.state = StateNormal
		stateChanged = true
	}

	return msg, stateChanged
}

// Len returns the number of messages currently in the buffer.
func (rb *RingBuffer) Len() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.count
}

// Cap returns the total capacity of the buffer.
func (rb *RingBuffer) Cap() int {
	return rb.size
}

// FillRatio returns the current fill ratio (count / capacity) as a value
// between 0.0 and 1.0.
func (rb *RingBuffer) FillRatio() float64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return float64(rb.count) / float64(rb.size)
}

// State returns the current backpressure state.
func (rb *RingBuffer) State() BackpressureState {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.state
}
