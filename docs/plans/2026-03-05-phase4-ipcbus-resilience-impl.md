# Phase 4: IPC Bus + Resilience Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement the IPC Bus binary that routes messages between channel sidecars and AI agent runtimes, with WAL-backed delivery, backpressure, DLQ, reconnection, and graceful shutdown.

**Architecture:** The IPC Bus is a standalone Go binary (`cmd/ipcbus/`) deployed as a native sidecar in each Claw pod. It listens on a Unix Domain Socket (`/var/run/claw/bus.sock`), routes JSON messages between connected channel sidecars and the runtime via protocol-specific RuntimeBridge adapters. Durability is provided by an append-only WAL on emptyDir; failed messages are stored in BoltDB DLQ. The operator injects the IPC Bus sidecar following the existing `claw_archiver.go` pattern.

**Tech Stack:** Go 1.25, `go.etcd.io/bbolt` (BoltDB), `nhooyr.io/websocket` (WebSocket bridge), length-prefix framing over UDS, Prometheus client_golang

---

## Task 1: Message types and length-prefix framing

**Files:**
- Create: `internal/ipcbus/message.go`
- Create: `internal/ipcbus/message_test.go`
- Create: `internal/ipcbus/framing.go`
- Create: `internal/ipcbus/framing_test.go`

### Step 1: Create message types

```go
// internal/ipcbus/message.go
package ipcbus

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// MessageType identifies the kind of message.
type MessageType string

const (
	TypeMessage   MessageType = "message"
	TypeAck       MessageType = "ack"
	TypeNack      MessageType = "nack"
	TypeSlowDown  MessageType = "slow_down"
	TypeResume    MessageType = "resume"
	TypeShutdown  MessageType = "shutdown"
	TypeRegister  MessageType = "register"
	TypeHeartbeat MessageType = "heartbeat"
)

// Message is the envelope for all IPC Bus communication.
type Message struct {
	ID            string          `json:"id"`
	Type          MessageType     `json:"type"`
	Channel       string          `json:"channel,omitempty"`
	CorrelationID string          `json:"correlationId,omitempty"`
	ReplyTo       string          `json:"replyTo,omitempty"`
	Timestamp     time.Time       `json:"timestamp"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

// NewMessage creates a new Message with a generated UUIDv7 ID and current timestamp.
func NewMessage(msgType MessageType, channel string, payload json.RawMessage) *Message {
	return &Message{
		ID:        uuid.Must(uuid.NewV7()).String(),
		Type:      msgType,
		Channel:   channel,
		Timestamp: time.Now(),
		Payload:   payload,
	}
}

// NewAck creates an ACK message for the given message ID.
func NewAck(id string) *Message {
	return &Message{
		ID:        uuid.Must(uuid.NewV7()).String(),
		Type:      TypeAck,
		Timestamp: time.Now(),
		Payload:   json.RawMessage(`{"ref":"` + id + `"}`),
	}
}

// IsControl returns true for control-plane message types.
func (m *Message) IsControl() bool {
	switch m.Type {
	case TypeAck, TypeNack, TypeSlowDown, TypeResume, TypeShutdown, TypeRegister, TypeHeartbeat:
		return true
	}
	return false
}
```

### Step 2: Create length-prefix framing

```go
// internal/ipcbus/framing.go
package ipcbus

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	// MaxMessageSize is the maximum allowed message size (16 MB).
	MaxMessageSize = 16 * 1024 * 1024
	// FrameHeaderSize is the size of the length prefix in bytes.
	FrameHeaderSize = 4
)

// WriteMessage serializes a Message and writes it with a 4-byte big-endian length prefix.
func WriteMessage(w io.Writer, msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	if len(data) > MaxMessageSize {
		return fmt.Errorf("message size %d exceeds maximum %d", len(data), MaxMessageSize)
	}

	header := make([]byte, FrameHeaderSize)
	binary.BigEndian.PutUint32(header, uint32(len(data)))

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("failed to write frame header: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("failed to write frame body: %w", err)
	}
	return nil
}

// ReadMessage reads a length-prefixed message from the reader.
func ReadMessage(r io.Reader) (*Message, error) {
	header := make([]byte, FrameHeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(header)
	if length > MaxMessageSize {
		return nil, fmt.Errorf("frame size %d exceeds maximum %d", length, MaxMessageSize)
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("failed to read frame body: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %w", err)
	}
	return &msg, nil
}
```

### Step 3: Write tests for message types and framing

```go
// internal/ipcbus/message_test.go
package ipcbus

import (
	"encoding/json"
	"testing"
)

func TestNewMessage(t *testing.T) {
	msg := NewMessage(TypeMessage, "slack", json.RawMessage(`{"text":"hello"}`))
	if msg.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if msg.Type != TypeMessage {
		t.Errorf("expected type %q, got %q", TypeMessage, msg.Type)
	}
	if msg.Channel != "slack" {
		t.Errorf("expected channel %q, got %q", "slack", msg.Channel)
	}
	if msg.Timestamp.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
}

func TestNewAck(t *testing.T) {
	ack := NewAck("test-id")
	if ack.Type != TypeAck {
		t.Errorf("expected type %q, got %q", TypeAck, ack.Type)
	}
}

func TestIsControl(t *testing.T) {
	tests := []struct {
		msgType MessageType
		want    bool
	}{
		{TypeMessage, false},
		{TypeAck, true},
		{TypeNack, true},
		{TypeSlowDown, true},
		{TypeResume, true},
		{TypeShutdown, true},
		{TypeRegister, true},
		{TypeHeartbeat, true},
	}
	for _, tt := range tests {
		msg := &Message{Type: tt.msgType}
		if got := msg.IsControl(); got != tt.want {
			t.Errorf("IsControl(%q) = %v, want %v", tt.msgType, got, tt.want)
		}
	}
}
```

```go
// internal/ipcbus/framing_test.go
package ipcbus

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestWriteReadMessage_Roundtrip(t *testing.T) {
	original := NewMessage(TypeMessage, "slack", json.RawMessage(`{"text":"hello"}`))

	var buf bytes.Buffer
	if err := WriteMessage(&buf, original); err != nil {
		t.Fatalf("WriteMessage failed: %v", err)
	}

	decoded, err := ReadMessage(&buf)
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID mismatch: got %q, want %q", decoded.ID, original.ID)
	}
	if decoded.Type != original.Type {
		t.Errorf("Type mismatch: got %q, want %q", decoded.Type, original.Type)
	}
	if decoded.Channel != original.Channel {
		t.Errorf("Channel mismatch: got %q, want %q", decoded.Channel, original.Channel)
	}
}

func TestWriteReadMessage_MultipleMessages(t *testing.T) {
	var buf bytes.Buffer
	msgs := []*Message{
		NewMessage(TypeMessage, "slack", json.RawMessage(`{"n":1}`)),
		NewMessage(TypeAck, "", nil),
		NewMessage(TypeMessage, "webhook", json.RawMessage(`{"n":2}`)),
	}
	for _, m := range msgs {
		if err := WriteMessage(&buf, m); err != nil {
			t.Fatalf("WriteMessage failed: %v", err)
		}
	}
	for i, want := range msgs {
		got, err := ReadMessage(&buf)
		if err != nil {
			t.Fatalf("ReadMessage[%d] failed: %v", i, err)
		}
		if got.ID != want.ID {
			t.Errorf("msg[%d] ID mismatch", i)
		}
	}
	// Should get EOF after all messages.
	_, err := ReadMessage(&buf)
	if err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestReadMessage_OversizedFrame(t *testing.T) {
	// Create a frame header claiming 20MB payload.
	header := []byte{0x01, 0x40, 0x00, 0x00} // ~20MB
	r := io.MultiReader(bytes.NewReader(header), strings.NewReader(""))
	_, err := ReadMessage(r)
	if err == nil {
		t.Fatal("expected error for oversized frame")
	}
}
```

### Step 4: Add dependency and verify build

Run:
```bash
cd /home/willamhou/codes/k8s4claw && go get github.com/google/uuid@latest
```

Then:
```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./internal/ipcbus/ -v -timeout 30s
```

### Step 5: Commit

```
feat: add IPC Bus message types and length-prefix framing
```

---

## Task 2: Ring buffer with backpressure

**Files:**
- Create: `internal/ipcbus/ringbuffer.go`
- Create: `internal/ipcbus/ringbuffer_test.go`

### Step 1: Implement ring buffer

```go
// internal/ipcbus/ringbuffer.go
package ipcbus

import (
	"fmt"
	"sync"
)

// BackpressureState represents the current flow control state.
type BackpressureState int

const (
	StateNormal   BackpressureState = iota
	StateSlowDown                   // high watermark crossed
)

// RingBuffer is a fixed-size, thread-safe circular buffer for messages.
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

// NewRingBuffer creates a ring buffer with the given capacity and watermarks.
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

// Push adds a message to the buffer. Returns false if the buffer is full.
// Also returns true in the second value if backpressure state changed.
func (rb *RingBuffer) Push(msg *Message) (ok bool, stateChanged bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.count >= rb.size {
		return false, false
	}

	rb.buf[rb.tail] = msg
	rb.tail = (rb.tail + 1) % rb.size
	rb.count++

	// Check high watermark transition.
	ratio := float64(rb.count) / float64(rb.size)
	if rb.state == StateNormal && ratio >= rb.highMark {
		rb.state = StateSlowDown
		return true, true
	}
	return true, false
}

// Pop removes and returns the oldest message. Returns nil if empty.
// Also returns true in the second value if backpressure state changed.
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

	// Check low watermark transition.
	ratio := float64(rb.count) / float64(rb.size)
	if rb.state == StateSlowDown && ratio <= rb.lowMark {
		rb.state = StateNormal
		return msg, true
	}
	return msg, false
}

// Len returns the current number of messages in the buffer.
func (rb *RingBuffer) Len() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.count
}

// Cap returns the buffer capacity.
func (rb *RingBuffer) Cap() int {
	return rb.size
}

// FillRatio returns the current fill ratio (0.0 to 1.0).
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

// String returns a human-readable representation of the backpressure state.
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
```

### Step 2: Write ring buffer tests

```go
// internal/ipcbus/ringbuffer_test.go
package ipcbus

import (
	"encoding/json"
	"testing"
)

func testMsg(channel string) *Message {
	return NewMessage(TypeMessage, channel, json.RawMessage(`{}`))
}

func TestRingBuffer_PushPop(t *testing.T) {
	rb := NewRingBuffer(4, 0.75, 0.25)

	m1 := testMsg("a")
	ok, _ := rb.Push(m1)
	if !ok {
		t.Fatal("push should succeed")
	}
	if rb.Len() != 1 {
		t.Fatalf("expected len 1, got %d", rb.Len())
	}

	got, _ := rb.Pop()
	if got.ID != m1.ID {
		t.Errorf("popped wrong message")
	}
	if rb.Len() != 0 {
		t.Fatalf("expected len 0, got %d", rb.Len())
	}
}

func TestRingBuffer_Full(t *testing.T) {
	rb := NewRingBuffer(2, 0.8, 0.3)

	rb.Push(testMsg("a"))
	rb.Push(testMsg("b"))

	ok, _ := rb.Push(testMsg("c"))
	if ok {
		t.Fatal("push to full buffer should fail")
	}
}

func TestRingBuffer_WrapAround(t *testing.T) {
	rb := NewRingBuffer(3, 0.8, 0.3)

	// Fill and drain twice to test wrap-around.
	for round := 0; round < 2; round++ {
		for i := 0; i < 3; i++ {
			ok, _ := rb.Push(testMsg("x"))
			if !ok {
				t.Fatalf("round %d push %d failed", round, i)
			}
		}
		for i := 0; i < 3; i++ {
			msg, _ := rb.Pop()
			if msg == nil {
				t.Fatalf("round %d pop %d returned nil", round, i)
			}
		}
	}
}

func TestRingBuffer_BackpressureTransitions(t *testing.T) {
	// Size=10, high=0.8 (8 msgs), low=0.3 (3 msgs)
	rb := NewRingBuffer(10, 0.8, 0.3)

	// Fill to 7 — should stay normal.
	for i := 0; i < 7; i++ {
		_, changed := rb.Push(testMsg("x"))
		if changed {
			t.Fatalf("unexpected state change at %d", i)
		}
	}
	if rb.State() != StateNormal {
		t.Fatal("expected normal state")
	}

	// Push 8th — crosses 0.8, should transition to SlowDown.
	_, changed := rb.Push(testMsg("x"))
	if !changed {
		t.Fatal("expected state change at high watermark")
	}
	if rb.State() != StateSlowDown {
		t.Fatal("expected slow_down state")
	}

	// Pop down to 4 — still above 0.3 low watermark.
	for i := 0; i < 4; i++ {
		_, changed := rb.Pop()
		if changed {
			t.Fatalf("unexpected state change during pop at count %d", rb.Len())
		}
	}
	if rb.State() != StateSlowDown {
		t.Fatal("expected still slow_down")
	}

	// Pop to 3 — crosses 0.3, should transition back to Normal.
	_, changed = rb.Pop()
	if !changed {
		t.Fatal("expected state change at low watermark")
	}
	if rb.State() != StateNormal {
		t.Fatal("expected normal state after crossing low watermark")
	}
}

func TestRingBuffer_FillRatio(t *testing.T) {
	rb := NewRingBuffer(10, 0.8, 0.3)
	if rb.FillRatio() != 0.0 {
		t.Errorf("expected 0.0, got %f", rb.FillRatio())
	}
	for i := 0; i < 5; i++ {
		rb.Push(testMsg("x"))
	}
	if rb.FillRatio() != 0.5 {
		t.Errorf("expected 0.5, got %f", rb.FillRatio())
	}
}

func TestRingBuffer_Defaults(t *testing.T) {
	rb := NewRingBuffer(0, 0, 0)
	if rb.Cap() != 1024 {
		t.Errorf("expected default cap 1024, got %d", rb.Cap())
	}
}
```

### Step 3: Run tests

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./internal/ipcbus/ -v -run TestRingBuffer -timeout 30s
```

### Step 4: Commit

```
feat: add ring buffer with backpressure watermark transitions
```

---

## Task 3: Write-Ahead Log (WAL)

**Files:**
- Create: `internal/ipcbus/wal.go`
- Create: `internal/ipcbus/wal_test.go`

### Step 1: Implement WAL

```go
// internal/ipcbus/wal.go
package ipcbus

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// WALState is the state of a WAL entry.
type WALState string

const (
	WALPending  WALState = "pending"
	WALComplete WALState = "complete"
	WALDLQ      WALState = "dlq"
)

// WALEntry is a single record in the WAL file.
type WALEntry struct {
	ID       string   `json:"id"`
	Channel  string   `json:"channel"`
	State    WALState `json:"state"`
	Attempts int      `json:"attempts"`
	TS       string   `json:"ts"`
	Msg      *Message `json:"msg"`
}

// WAL is an append-only write-ahead log stored as JSON-lines files.
type WAL struct {
	mu       sync.Mutex
	dir      string
	file     *os.File
	writer   *bufio.Writer
	entries  map[string]*WALEntry // in-memory index by message ID
	fileSize int64

	// Compaction thresholds.
	maxFileSize    int64
	compactInterval time.Duration
}

// NewWAL opens or creates a WAL in the given directory.
func NewWAL(dir string) (*WAL, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create WAL directory: %w", err)
	}

	w := &WAL{
		dir:             dir,
		entries:         make(map[string]*WALEntry),
		maxFileSize:     10 * 1024 * 1024, // 10MB
		compactInterval: 60 * time.Second,
	}

	if err := w.recover(); err != nil {
		return nil, fmt.Errorf("WAL recovery failed: %w", err)
	}

	if err := w.openFile(); err != nil {
		return nil, fmt.Errorf("failed to open WAL file: %w", err)
	}

	return w, nil
}

func (w *WAL) openFile() error {
	path := filepath.Join(w.dir, "wal.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.file = f
	w.writer = bufio.NewWriter(f)
	w.fileSize = info.Size()
	return nil
}

// Append writes a new pending entry to the WAL.
func (w *WAL) Append(msg *Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry := &WALEntry{
		ID:       msg.ID,
		Channel:  msg.Channel,
		State:    WALPending,
		Attempts: 0,
		TS:       time.Now().Format(time.RFC3339),
		Msg:      msg,
	}

	if err := w.writeEntry(entry); err != nil {
		return err
	}
	w.entries[msg.ID] = entry
	return nil
}

// Complete marks a WAL entry as successfully delivered.
func (w *WAL) Complete(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry, ok := w.entries[id]
	if !ok {
		return nil // already removed or never existed
	}
	entry.State = WALComplete
	return w.writeEntry(entry)
}

// MarkDLQ marks a WAL entry as moved to DLQ.
func (w *WAL) MarkDLQ(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry, ok := w.entries[id]
	if !ok {
		return nil
	}
	entry.State = WALDLQ
	return w.writeEntry(entry)
}

// IncrementAttempts bumps the retry count for a WAL entry.
func (w *WAL) IncrementAttempts(id string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry, ok := w.entries[id]
	if !ok {
		return 0, fmt.Errorf("WAL entry %q not found", id)
	}
	entry.Attempts++
	if err := w.writeEntry(entry); err != nil {
		return entry.Attempts, err
	}
	return entry.Attempts, nil
}

// PendingEntries returns all entries in pending state.
func (w *WAL) PendingEntries() []*WALEntry {
	w.mu.Lock()
	defer w.mu.Unlock()

	var pending []*WALEntry
	for _, e := range w.entries {
		if e.State == WALPending {
			pending = append(pending, e)
		}
	}
	return pending
}

// Compact rewrites the WAL file, removing completed and DLQ entries.
func (w *WAL) Compact() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Collect active entries.
	var active []*WALEntry
	for _, e := range w.entries {
		if e.State == WALPending {
			active = append(active, e)
		}
	}

	// Write to temp file.
	tmpPath := filepath.Join(w.dir, "wal.tmp.jsonl")
	tmp, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to create temp WAL: %w", err)
	}
	tmpWriter := bufio.NewWriter(tmp)
	for _, e := range active {
		data, err := json.Marshal(e)
		if err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("failed to marshal entry: %w", err)
		}
		tmpWriter.Write(data)
		tmpWriter.WriteByte('\n')
	}
	if err := tmpWriter.Flush(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	tmp.Close()

	// Close current file, rename temp over it.
	w.writer.Flush()
	w.file.Close()

	walPath := filepath.Join(w.dir, "wal.jsonl")
	if err := os.Rename(tmpPath, walPath); err != nil {
		return fmt.Errorf("failed to rename WAL: %w", err)
	}

	// Rebuild entries map.
	w.entries = make(map[string]*WALEntry)
	for _, e := range active {
		w.entries[e.ID] = e
	}

	// Reopen file.
	return w.openFile()
}

// Flush flushes the WAL to disk (fsync).
func (w *WAL) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.writer.Flush(); err != nil {
		return err
	}
	return w.file.Sync()
}

// NeedsCompaction returns true if the WAL file exceeds maxFileSize.
func (w *WAL) NeedsCompaction() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fileSize > w.maxFileSize
}

// Close flushes and closes the WAL.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.writer != nil {
		w.writer.Flush()
	}
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

func (w *WAL) writeEntry(entry *WALEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal WAL entry: %w", err)
	}
	n, err := w.writer.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write WAL entry: %w", err)
	}
	w.writer.WriteByte('\n')
	w.fileSize += int64(n + 1)

	return w.writer.Flush()
}

// recover reads the existing WAL file and rebuilds the in-memory index.
// Only the latest state for each message ID is kept.
func (w *WAL) recover() error {
	path := filepath.Join(w.dir, "wal.jsonl")
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry WALEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // skip corrupt lines
		}
		if entry.State == WALComplete || entry.State == WALDLQ {
			delete(w.entries, entry.ID)
		} else {
			w.entries[entry.ID] = &entry
		}
	}
	return scanner.Err()
}
```

### Step 2: Write WAL tests

```go
// internal/ipcbus/wal_test.go
package ipcbus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWAL_AppendAndPending(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL failed: %v", err)
	}
	defer w.Close()

	msg := NewMessage(TypeMessage, "slack", json.RawMessage(`{"text":"hi"}`))
	if err := w.Append(msg); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	pending := w.PendingEntries()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}
	if pending[0].ID != msg.ID {
		t.Errorf("wrong message ID")
	}
}

func TestWAL_Complete(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL failed: %v", err)
	}
	defer w.Close()

	msg := NewMessage(TypeMessage, "slack", json.RawMessage(`{}`))
	w.Append(msg)
	w.Complete(msg.ID)

	if len(w.PendingEntries()) != 0 {
		t.Fatal("expected no pending entries after Complete")
	}
}

func TestWAL_Recovery(t *testing.T) {
	dir := t.TempDir()

	// Write some entries and close.
	w1, _ := NewWAL(dir)
	msg1 := NewMessage(TypeMessage, "slack", json.RawMessage(`{"n":1}`))
	msg2 := NewMessage(TypeMessage, "slack", json.RawMessage(`{"n":2}`))
	w1.Append(msg1)
	w1.Append(msg2)
	w1.Complete(msg1.ID)
	w1.Close()

	// Reopen and check recovery.
	w2, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	defer w2.Close()

	pending := w2.PendingEntries()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending after recovery, got %d", len(pending))
	}
	if pending[0].ID != msg2.ID {
		t.Errorf("recovered wrong message")
	}
}

func TestWAL_Compact(t *testing.T) {
	dir := t.TempDir()
	w, _ := NewWAL(dir)
	defer w.Close()

	// Add 3 messages, complete 2.
	msgs := make([]*Message, 3)
	for i := range msgs {
		msgs[i] = NewMessage(TypeMessage, "ch", json.RawMessage(`{}`))
		w.Append(msgs[i])
	}
	w.Complete(msgs[0].ID)
	w.Complete(msgs[1].ID)

	if err := w.Compact(); err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	// After compaction, only 1 pending.
	if len(w.PendingEntries()) != 1 {
		t.Fatalf("expected 1 pending after compaction, got %d", len(w.PendingEntries()))
	}

	// WAL file should be small.
	info, _ := os.Stat(filepath.Join(dir, "wal.jsonl"))
	if info.Size() > 500 {
		t.Errorf("WAL file too large after compaction: %d bytes", info.Size())
	}
}

func TestWAL_IncrementAttempts(t *testing.T) {
	dir := t.TempDir()
	w, _ := NewWAL(dir)
	defer w.Close()

	msg := NewMessage(TypeMessage, "ch", json.RawMessage(`{}`))
	w.Append(msg)

	count, err := w.IncrementAttempts(msg.ID)
	if err != nil {
		t.Fatalf("IncrementAttempts failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 attempt, got %d", count)
	}

	count, _ = w.IncrementAttempts(msg.ID)
	if count != 2 {
		t.Errorf("expected 2 attempts, got %d", count)
	}
}
```

### Step 3: Run tests

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./internal/ipcbus/ -v -run TestWAL -timeout 30s
```

### Step 4: Commit

```
feat: add write-ahead log with recovery and compaction
```

---

## Task 4: Dead Letter Queue (BoltDB)

**Files:**
- Create: `internal/ipcbus/dlq.go`
- Create: `internal/ipcbus/dlq_test.go`

### Step 1: Add BoltDB dependency

```bash
cd /home/willamhou/codes/k8s4claw && go get go.etcd.io/bbolt@latest
```

### Step 2: Implement DLQ

```go
// internal/ipcbus/dlq.go
package ipcbus

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	dlqMessagesBucket = []byte("messages")
	dlqIndexBucket    = []byte("index")
)

// DLQEntry is a message stored in the dead letter queue.
type DLQEntry struct {
	ID        string   `json:"id"`
	Channel   string   `json:"channel"`
	Attempts  int      `json:"attempts"`
	CreatedAt string   `json:"createdAt"`
	Msg       *Message `json:"msg"`
}

// DLQ is a dead letter queue backed by BoltDB.
type DLQ struct {
	db        *bolt.DB
	maxSize   int
	ttl       time.Duration
}

// NewDLQ opens or creates a DLQ at the given path.
func NewDLQ(path string, maxSize int, ttl time.Duration) (*DLQ, error) {
	if maxSize <= 0 {
		maxSize = 10000
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("failed to open DLQ database: %w", err)
	}

	// Create buckets.
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(dlqMessagesBucket); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(dlqIndexBucket)
		return err
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create DLQ buckets: %w", err)
	}

	return &DLQ{db: db, maxSize: maxSize, ttl: ttl}, nil
}

// Put stores a message in the DLQ. If the DLQ is at capacity, the oldest entry is evicted.
func (d *DLQ) Put(msg *Message, attempts int) error {
	entry := &DLQEntry{
		ID:        msg.ID,
		Channel:   msg.Channel,
		Attempts:  attempts,
		CreatedAt: time.Now().Format(time.RFC3339),
		Msg:       msg,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal DLQ entry: %w", err)
	}

	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(dlqMessagesBucket)

		// Evict oldest if at capacity.
		if b.Stats().KeyN >= d.maxSize {
			c := b.Cursor()
			k, _ := c.First()
			if k != nil {
				b.Delete(k)
			}
		}

		return b.Put([]byte(msg.ID), data)
	})
}

// Get retrieves a DLQ entry by ID.
func (d *DLQ) Get(id string) (*DLQEntry, error) {
	var entry DLQEntry
	err := d.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(dlqMessagesBucket).Get([]byte(id))
		if data == nil {
			return fmt.Errorf("DLQ entry %q not found", id)
		}
		return json.Unmarshal(data, &entry)
	})
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

// Delete removes an entry from the DLQ.
func (d *DLQ) Delete(id string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(dlqMessagesBucket).Delete([]byte(id))
	})
}

// List returns all DLQ entries, ordered by insertion (BoltDB key order).
func (d *DLQ) List() ([]*DLQEntry, error) {
	var entries []*DLQEntry
	err := d.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(dlqMessagesBucket).ForEach(func(k, v []byte) error {
			var entry DLQEntry
			if err := json.Unmarshal(v, &entry); err != nil {
				return nil // skip corrupt entries
			}
			entries = append(entries, &entry)
			return nil
		})
	})
	return entries, err
}

// Size returns the number of entries in the DLQ.
func (d *DLQ) Size() int {
	var count int
	d.db.View(func(tx *bolt.Tx) error {
		count = tx.Bucket(dlqMessagesBucket).Stats().KeyN
		return nil
	})
	return count
}

// PurgeExpired removes entries older than the TTL.
func (d *DLQ) PurgeExpired() (int, error) {
	cutoff := time.Now().Add(-d.ttl)
	purged := 0

	err := d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(dlqMessagesBucket)
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var entry DLQEntry
			if err := json.Unmarshal(v, &entry); err != nil {
				continue
			}
			created, err := time.Parse(time.RFC3339, entry.CreatedAt)
			if err != nil {
				continue
			}
			if created.Before(cutoff) {
				b.Delete(k)
				purged++
			}
		}
		return nil
	})
	return purged, err
}

// Close closes the DLQ database.
func (d *DLQ) Close() error {
	return d.db.Close()
}
```

### Step 3: Write DLQ tests

```go
// internal/ipcbus/dlq_test.go
package ipcbus

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestDLQ_PutGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dlq.db")
	d, err := NewDLQ(path, 100, 24*time.Hour)
	if err != nil {
		t.Fatalf("NewDLQ failed: %v", err)
	}
	defer d.Close()

	msg := NewMessage(TypeMessage, "slack", json.RawMessage(`{"text":"failed"}`))
	if err := d.Put(msg, 5); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	entry, err := d.Get(msg.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if entry.Attempts != 5 {
		t.Errorf("expected 5 attempts, got %d", entry.Attempts)
	}
	if entry.Channel != "slack" {
		t.Errorf("expected channel slack, got %q", entry.Channel)
	}
}

func TestDLQ_Delete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dlq.db")
	d, _ := NewDLQ(path, 100, 24*time.Hour)
	defer d.Close()

	msg := NewMessage(TypeMessage, "ch", json.RawMessage(`{}`))
	d.Put(msg, 1)
	d.Delete(msg.ID)

	_, err := d.Get(msg.ID)
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestDLQ_List(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dlq.db")
	d, _ := NewDLQ(path, 100, 24*time.Hour)
	defer d.Close()

	for i := 0; i < 5; i++ {
		d.Put(NewMessage(TypeMessage, "ch", json.RawMessage(`{}`)), i)
	}

	entries, err := d.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}
}

func TestDLQ_Eviction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dlq.db")
	d, _ := NewDLQ(path, 3, 24*time.Hour)
	defer d.Close()

	msgs := make([]*Message, 4)
	for i := range msgs {
		msgs[i] = NewMessage(TypeMessage, "ch", json.RawMessage(`{}`))
		d.Put(msgs[i], 1)
	}

	if d.Size() != 3 {
		t.Fatalf("expected 3 entries after eviction, got %d", d.Size())
	}

	// First message should have been evicted.
	_, err := d.Get(msgs[0].ID)
	if err == nil {
		t.Fatal("expected first message to be evicted")
	}
}

func TestDLQ_Size(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dlq.db")
	d, _ := NewDLQ(path, 100, 24*time.Hour)
	defer d.Close()

	if d.Size() != 0 {
		t.Fatalf("expected size 0, got %d", d.Size())
	}
	d.Put(NewMessage(TypeMessage, "ch", json.RawMessage(`{}`)), 1)
	if d.Size() != 1 {
		t.Fatalf("expected size 1, got %d", d.Size())
	}
}
```

### Step 4: Run tests

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./internal/ipcbus/ -v -run TestDLQ -timeout 30s
```

### Step 5: Commit

```
feat: add BoltDB-backed dead letter queue
```

---

## Task 5: RuntimeBridge interface and WebSocket bridge (OpenClaw)

**Files:**
- Create: `internal/ipcbus/bridge.go`
- Create: `internal/ipcbus/bridge_ws.go`
- Create: `internal/ipcbus/bridge_ws_test.go`

### Step 1: Add WebSocket dependency

```bash
cd /home/willamhou/codes/k8s4claw && go get nhooyr.io/websocket@latest
```

### Step 2: Define RuntimeBridge interface

```go
// internal/ipcbus/bridge.go
package ipcbus

import (
	"context"
	"fmt"
)

// RuntimeBridge connects the IPC Bus to a specific runtime gateway protocol.
type RuntimeBridge interface {
	// Connect establishes a connection to the runtime gateway.
	Connect(ctx context.Context) error
	// Send delivers a message to the runtime.
	Send(ctx context.Context, msg *Message) error
	// Receive returns a channel of outbound messages from the runtime.
	Receive(ctx context.Context) (<-chan *Message, error)
	// Close gracefully disconnects.
	Close() error
}

// RuntimeType identifies the runtime for bridge selection.
type RuntimeType string

const (
	RuntimeOpenClaw RuntimeType = "openclaw"
	RuntimeNanoClaw RuntimeType = "nanoclaw"
	RuntimeZeroClaw RuntimeType = "zeroclaw"
	RuntimePicoClaw RuntimeType = "picoclaw"
)

// NewBridge creates the appropriate RuntimeBridge for the given runtime type.
func NewBridge(rt RuntimeType, gatewayPort int) (RuntimeBridge, error) {
	switch rt {
	case RuntimeOpenClaw:
		return NewWebSocketBridge(fmt.Sprintf("ws://localhost:%d", gatewayPort)), nil
	case RuntimeNanoClaw:
		return NewUDSBridge(fmt.Sprintf("localhost:%d", gatewayPort)), nil
	case RuntimeZeroClaw:
		return NewSSEBridge(fmt.Sprintf("http://localhost:%d", gatewayPort)), nil
	case RuntimePicoClaw:
		return NewTCPBridge(fmt.Sprintf("localhost:%d", gatewayPort)), nil
	default:
		return nil, fmt.Errorf("unsupported runtime type: %s", rt)
	}
}
```

### Step 3: Implement WebSocket bridge

```go
// internal/ipcbus/bridge_ws.go
package ipcbus

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"nhooyr.io/websocket"
)

// WebSocketBridge connects to an OpenClaw runtime via WebSocket.
type WebSocketBridge struct {
	url  string
	conn *websocket.Conn
	mu   sync.Mutex
}

// NewWebSocketBridge creates a WebSocket bridge for the given URL.
func NewWebSocketBridge(url string) *WebSocketBridge {
	return &WebSocketBridge{url: url}
}

// Connect establishes the WebSocket connection.
func (b *WebSocketBridge) Connect(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, b.url, nil)
	if err != nil {
		return fmt.Errorf("websocket dial failed: %w", err)
	}
	b.conn = conn
	return nil
}

// Send delivers a message over the WebSocket connection.
func (b *WebSocketBridge) Send(ctx context.Context, msg *Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	return b.conn.Write(ctx, websocket.MessageText, data)
}

// Receive returns a channel that emits messages from the runtime.
func (b *WebSocketBridge) Receive(ctx context.Context) (<-chan *Message, error) {
	ch := make(chan *Message, 64)
	go func() {
		defer close(ch)
		for {
			_, data, err := b.conn.Read(ctx)
			if err != nil {
				return
			}
			var msg Message
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			select {
			case ch <- &msg:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// Close gracefully closes the WebSocket connection.
func (b *WebSocketBridge) Close() error {
	if b.conn != nil {
		return b.conn.Close(websocket.StatusNormalClosure, "shutdown")
	}
	return nil
}
```

### Step 4: Create stub bridges for other runtimes

```go
// internal/ipcbus/bridge_uds.go
package ipcbus

import (
	"context"
	"fmt"
)

// UDSBridge connects to a NanoClaw runtime via Unix Domain Socket wrapper.
type UDSBridge struct {
	addr string
}

func NewUDSBridge(addr string) *UDSBridge {
	return &UDSBridge{addr: addr}
}

func (b *UDSBridge) Connect(ctx context.Context) error {
	return fmt.Errorf("UDS bridge not yet implemented")
}

func (b *UDSBridge) Send(ctx context.Context, msg *Message) error {
	return fmt.Errorf("UDS bridge not yet implemented")
}

func (b *UDSBridge) Receive(ctx context.Context) (<-chan *Message, error) {
	return nil, fmt.Errorf("UDS bridge not yet implemented")
}

func (b *UDSBridge) Close() error { return nil }
```

```go
// internal/ipcbus/bridge_sse.go
package ipcbus

import (
	"context"
	"fmt"
)

// SSEBridge connects to a ZeroClaw runtime via SSE + HTTP POST.
type SSEBridge struct {
	baseURL string
}

func NewSSEBridge(baseURL string) *SSEBridge {
	return &SSEBridge{baseURL: baseURL}
}

func (b *SSEBridge) Connect(ctx context.Context) error {
	return fmt.Errorf("SSE bridge not yet implemented")
}

func (b *SSEBridge) Send(ctx context.Context, msg *Message) error {
	return fmt.Errorf("SSE bridge not yet implemented")
}

func (b *SSEBridge) Receive(ctx context.Context) (<-chan *Message, error) {
	return nil, fmt.Errorf("SSE bridge not yet implemented")
}

func (b *SSEBridge) Close() error { return nil }
```

```go
// internal/ipcbus/bridge_tcp.go
package ipcbus

import (
	"context"
	"fmt"
)

// TCPBridge connects to a PicoClaw runtime via raw TCP with length-prefix framing.
type TCPBridge struct {
	addr string
}

func NewTCPBridge(addr string) *TCPBridge {
	return &TCPBridge{addr: addr}
}

func (b *TCPBridge) Connect(ctx context.Context) error {
	return fmt.Errorf("TCP bridge not yet implemented")
}

func (b *TCPBridge) Send(ctx context.Context, msg *Message) error {
	return fmt.Errorf("TCP bridge not yet implemented")
}

func (b *TCPBridge) Receive(ctx context.Context) (<-chan *Message, error) {
	return nil, fmt.Errorf("TCP bridge not yet implemented")
}

func (b *TCPBridge) Close() error { return nil }
```

### Step 5: Write WebSocket bridge test (with httptest WS server)

```go
// internal/ipcbus/bridge_ws_test.go
package ipcbus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestWebSocketBridge_SendReceive(t *testing.T) {
	// Echo WebSocket server: reads a message and writes it back.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Logf("accept error: %v", err)
			return
		}
		defer conn.CloseNow()

		ctx := r.Context()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			conn.Write(ctx, typ, data)
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	bridge := NewWebSocketBridge(wsURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := bridge.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer bridge.Close()

	recvCh, err := bridge.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}

	// Send a message.
	msg := NewMessage(TypeMessage, "test", json.RawMessage(`{"hello":"world"}`))
	if err := bridge.Send(ctx, msg); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Should get the echo back.
	select {
	case got := <-recvCh:
		if got.ID != msg.ID {
			t.Errorf("echoed message ID mismatch: got %q, want %q", got.ID, msg.ID)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for echo")
	}
}
```

### Step 6: Run tests and verify build

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./internal/ipcbus/ -v -run TestWebSocket -timeout 30s
```

### Step 7: Commit

```
feat: add RuntimeBridge interface with WebSocket bridge for OpenClaw
```

---

## Task 6: UDS server and message router

**Files:**
- Create: `internal/ipcbus/server.go`
- Create: `internal/ipcbus/router.go`
- Create: `internal/ipcbus/server_test.go`

### Step 1: Implement UDS server

```go
// internal/ipcbus/server.go
package ipcbus

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/go-logr/logr"
)

// SidecarConn represents a connected channel sidecar.
type SidecarConn struct {
	Channel string
	Mode    string
	conn    net.Conn
	mu      sync.Mutex
}

// Send writes a message to the sidecar connection.
func (sc *SidecarConn) Send(msg *Message) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return WriteMessage(sc.conn, msg)
}

// Server is the UDS listener that accepts sidecar connections.
type Server struct {
	socketPath string
	listener   net.Listener
	router     *Router
	logger     logr.Logger
	wg         sync.WaitGroup
}

// NewServer creates a new UDS server.
func NewServer(socketPath string, router *Router, logger logr.Logger) *Server {
	return &Server{
		socketPath: socketPath,
		router:     router,
		logger:     logger,
	}
}

// Start begins listening for connections. Blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	// Remove stale socket file.
	os.Remove(s.socketPath)

	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.socketPath, err)
	}
	s.listener = l

	// Make socket world-readable so other containers can connect.
	if err := os.Chmod(s.socketPath, 0o777); err != nil {
		s.logger.Error(err, "failed to chmod socket")
	}

	s.logger.Info("IPC Bus listening", "socket", s.socketPath)

	go func() {
		<-ctx.Done()
		l.Close()
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				s.wg.Wait()
				return nil
			default:
				s.logger.Error(err, "accept error")
				continue
			}
		}
		s.wg.Add(1)
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	// First message must be a register message.
	msg, err := ReadMessage(conn)
	if err != nil {
		s.logger.Error(err, "failed to read register message")
		return
	}
	if msg.Type != TypeRegister {
		s.logger.Info("expected register message, got", "type", msg.Type)
		return
	}

	sc := &SidecarConn{
		Channel: msg.Channel,
		Mode:    string(msg.Type),
		conn:    conn,
	}

	s.router.Register(sc)
	defer s.router.Unregister(sc)

	s.logger.Info("sidecar registered", "channel", msg.Channel)

	// Send ACK.
	ack := NewAck(msg.ID)
	if err := sc.Send(ack); err != nil {
		s.logger.Error(err, "failed to send register ACK")
		return
	}

	// Read loop: forward messages to router.
	for {
		msg, err := ReadMessage(conn)
		if err != nil {
			if err != io.EOF {
				s.logger.Error(err, "read error", "channel", sc.Channel)
			}
			return
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		s.router.HandleInbound(ctx, sc, msg)
	}
}

// ConnectedCount returns the number of currently connected sidecars.
func (s *Server) ConnectedCount() int {
	return s.router.ConnectedCount()
}
```

### Step 2: Implement message router

```go
// internal/ipcbus/router.go
package ipcbus

import (
	"context"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

const (
	maxRetries     = 5
	baseRetryDelay = 1 * time.Second
)

// Router handles message routing between sidecars and the runtime bridge.
type Router struct {
	mu        sync.RWMutex
	sidecars  map[string]*SidecarConn // channel name → connection
	bridge    RuntimeBridge
	wal       *WAL
	dlq       *DLQ
	buffers   map[string]*RingBuffer // channel name → ring buffer
	logger    logr.Logger

	bufferSize    int
	highWatermark float64
	lowWatermark  float64
}

// RouterConfig holds configuration for the router.
type RouterConfig struct {
	Bridge        RuntimeBridge
	WAL           *WAL
	DLQ           *DLQ
	Logger        logr.Logger
	BufferSize    int
	HighWatermark float64
	LowWatermark  float64
}

// NewRouter creates a new message router.
func NewRouter(cfg RouterConfig) *Router {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 1024
	}
	if cfg.HighWatermark <= 0 {
		cfg.HighWatermark = 0.8
	}
	if cfg.LowWatermark <= 0 {
		cfg.LowWatermark = 0.3
	}
	return &Router{
		sidecars:      make(map[string]*SidecarConn),
		bridge:        cfg.Bridge,
		wal:           cfg.WAL,
		dlq:           cfg.DLQ,
		buffers:       make(map[string]*RingBuffer),
		logger:        cfg.Logger,
		bufferSize:    cfg.BufferSize,
		highWatermark: cfg.HighWatermark,
		lowWatermark:  cfg.LowWatermark,
	}
}

// Register adds a sidecar connection.
func (r *Router) Register(sc *SidecarConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sidecars[sc.Channel] = sc
	if _, ok := r.buffers[sc.Channel]; !ok {
		r.buffers[sc.Channel] = NewRingBuffer(r.bufferSize, r.highWatermark, r.lowWatermark)
	}
}

// Unregister removes a sidecar connection.
func (r *Router) Unregister(sc *SidecarConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sidecars, sc.Channel)
}

// ConnectedCount returns the number of connected sidecars.
func (r *Router) ConnectedCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sidecars)
}

// HandleInbound processes a message from a sidecar (inbound to runtime).
func (r *Router) HandleInbound(ctx context.Context, from *SidecarConn, msg *Message) {
	if msg.IsControl() {
		r.handleControl(from, msg)
		return
	}

	// Write to WAL before delivery.
	if r.wal != nil {
		if err := r.wal.Append(msg); err != nil {
			r.logger.Error(err, "WAL append failed", "msgID", msg.ID)
		}
	}

	// Enqueue in ring buffer.
	r.mu.RLock()
	buf, ok := r.buffers[from.Channel]
	r.mu.RUnlock()

	if ok {
		pushed, stateChanged := buf.Push(msg)
		if !pushed {
			// Buffer full — spill to WAL (already written above).
			r.logger.Info("ring buffer full, message spilled to WAL", "channel", from.Channel, "msgID", msg.ID)
			return
		}
		if stateChanged && buf.State() == StateSlowDown {
			from.Send(NewMessage(TypeSlowDown, from.Channel, nil))
		}
	}

	// Forward to runtime bridge.
	if r.bridge != nil {
		if err := r.bridge.Send(ctx, msg); err != nil {
			r.logger.Error(err, "bridge send failed", "msgID", msg.ID)
			r.scheduleRetry(ctx, msg)
			return
		}
	}

	// Mark WAL complete on successful delivery.
	if r.wal != nil {
		r.wal.Complete(msg.ID)
	}

	// Pop from buffer.
	if ok {
		_, stateChanged := buf.Pop()
		if stateChanged && buf.State() == StateNormal {
			from.Send(NewMessage(TypeResume, from.Channel, nil))
		}
	}
}

// HandleOutbound processes a message from the runtime (outbound to sidecar).
func (r *Router) HandleOutbound(ctx context.Context, msg *Message) {
	r.mu.RLock()
	sc, ok := r.sidecars[msg.Channel]
	r.mu.RUnlock()

	if !ok {
		r.logger.Info("no sidecar for channel, dropping outbound message", "channel", msg.Channel, "msgID", msg.ID)
		return
	}

	if err := sc.Send(msg); err != nil {
		r.logger.Error(err, "failed to send to sidecar", "channel", msg.Channel, "msgID", msg.ID)
	}
}

// StartOutboundLoop reads messages from the runtime bridge and routes them to sidecars.
func (r *Router) StartOutboundLoop(ctx context.Context) error {
	if r.bridge == nil {
		return nil
	}

	recvCh, err := r.bridge.Receive(ctx)
	if err != nil {
		return err
	}

	go func() {
		for msg := range recvCh {
			r.HandleOutbound(ctx, msg)
		}
	}()
	return nil
}

// ReplayWAL re-enqueues pending WAL entries for delivery.
func (r *Router) ReplayWAL(ctx context.Context) {
	if r.wal == nil {
		return
	}
	pending := r.wal.PendingEntries()
	r.logger.Info("replaying WAL entries", "count", len(pending))
	for _, entry := range pending {
		r.HandleInbound(ctx, &SidecarConn{Channel: entry.Channel}, entry.Msg)
	}
}

// SendShutdown sends shutdown messages to all connected sidecars.
func (r *Router) SendShutdown() {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, sc := range r.sidecars {
		msg := NewMessage(TypeShutdown, sc.Channel, nil)
		if err := sc.Send(msg); err != nil {
			r.logger.Error(err, "failed to send shutdown", "channel", sc.Channel)
		}
	}
}

func (r *Router) handleControl(from *SidecarConn, msg *Message) {
	switch msg.Type {
	case TypeAck:
		// ACK from sidecar for outbound message — no-op for now.
	case TypeHeartbeat:
		from.Send(NewAck(msg.ID))
	default:
		r.logger.Info("unhandled control message", "type", msg.Type, "channel", from.Channel)
	}
}

func (r *Router) scheduleRetry(ctx context.Context, msg *Message) {
	if r.wal == nil {
		return
	}
	attempts, err := r.wal.IncrementAttempts(msg.ID)
	if err != nil {
		r.logger.Error(err, "failed to increment WAL attempts", "msgID", msg.ID)
		return
	}
	if attempts >= maxRetries {
		r.logger.Info("max retries reached, moving to DLQ", "msgID", msg.ID, "attempts", attempts)
		r.wal.MarkDLQ(msg.ID)
		if r.dlq != nil {
			r.dlq.Put(msg, attempts)
		}
		return
	}
	// Retry will happen on next WAL replay or compaction cycle.
	r.logger.Info("message queued for retry", "msgID", msg.ID, "attempts", attempts)
}
```

### Step 3: Write server integration test (over real UDS)

```go
// internal/ipcbus/server_test.go
package ipcbus

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func TestServer_RegisterAndSend(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "bus.sock")

	wal, _ := NewWAL(filepath.Join(dir, "wal"))
	defer wal.Close()

	router := NewRouter(RouterConfig{
		WAL:    wal,
		Logger: logr.Discard(),
	})
	server := NewServer(socketPath, router, logr.Discard())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start server in background.
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start(ctx)
	}()

	// Wait for socket to appear.
	for i := 0; i < 50; i++ {
		if _, err := net.Dial("unix", socketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Connect as a sidecar client.
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	// Send register message.
	reg := NewMessage(TypeRegister, "test-channel", nil)
	if err := WriteMessage(conn, reg); err != nil {
		t.Fatalf("write register failed: %v", err)
	}

	// Read ACK.
	ack, err := ReadMessage(conn)
	if err != nil {
		t.Fatalf("read ACK failed: %v", err)
	}
	if ack.Type != TypeAck {
		t.Fatalf("expected ACK, got %q", ack.Type)
	}

	// Verify connected count.
	time.Sleep(50 * time.Millisecond)
	if server.ConnectedCount() != 1 {
		t.Errorf("expected 1 connected, got %d", server.ConnectedCount())
	}

	// Send a data message.
	dataMsg := NewMessage(TypeMessage, "test-channel", json.RawMessage(`{"hello":"world"}`))
	if err := WriteMessage(conn, dataMsg); err != nil {
		t.Fatalf("write data failed: %v", err)
	}

	// Give router time to process.
	time.Sleep(100 * time.Millisecond)

	// Cancel to stop server.
	cancel()
	<-errCh
}
```

### Step 4: Run tests

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./internal/ipcbus/ -v -run TestServer -timeout 30s
```

### Step 5: Commit

```
feat: add UDS server and message router with WAL-backed delivery
```

---

## Task 7: IPC Bus binary entrypoint

**Files:**
- Create: `cmd/ipcbus/main.go`

### Step 1: Implement CLI entrypoint

```go
// cmd/ipcbus/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"

	"github.com/Prismer-AI/k8s4claw/internal/ipcbus"
)

func main() {
	var (
		socketPath  string
		walDir      string
		runtimeType string
		gatewayPort int
		bufferSize  int
		highWM      float64
		lowWM       float64
	)

	flag.StringVar(&socketPath, "socket-path", envOr("IPC_SOCKET_PATH", "/var/run/claw/bus.sock"), "UDS socket path")
	flag.StringVar(&walDir, "wal-dir", envOr("WAL_DIR", "/var/run/claw/wal"), "WAL and DLQ storage directory")
	flag.StringVar(&runtimeType, "runtime", envOr("CLAW_RUNTIME", "openclaw"), "Runtime type")
	flag.IntVar(&gatewayPort, "gateway-port", envOrInt("CLAW_GATEWAY_PORT", 18900), "Runtime gateway port")
	flag.IntVar(&bufferSize, "buffer-size", envOrInt("BUFFER_SIZE", 1024), "Ring buffer size per channel")
	flag.Float64Var(&highWM, "high-watermark", 0.8, "Backpressure high watermark (0.0-1.0)")
	flag.Float64Var(&lowWM, "low-watermark", 0.3, "Backpressure low watermark (0.0-1.0)")
	flag.Parse()

	// Handle shutdown subcommand.
	args := flag.Args()
	if len(args) > 0 && args[0] == "shutdown" {
		runShutdown(socketPath)
		return
	}

	// Setup logger.
	zapLog, _ := zap.NewProduction()
	defer zapLog.Sync()
	logger := zapr.NewLogger(zapLog)

	logger.Info("starting IPC Bus",
		"socket", socketPath,
		"runtime", runtimeType,
		"gatewayPort", gatewayPort,
	)

	// Initialize WAL.
	wal, err := ipcbus.NewWAL(walDir)
	if err != nil {
		logger.Error(err, "failed to create WAL")
		os.Exit(1)
	}
	defer wal.Close()

	// Initialize DLQ.
	dlqPath := filepath.Join(walDir, "dlq.db")
	dlq, err := ipcbus.NewDLQ(dlqPath, 10000, 24*time.Hour)
	if err != nil {
		logger.Error(err, "failed to create DLQ")
		os.Exit(1)
	}
	defer dlq.Close()

	// Create runtime bridge.
	bridge, err := ipcbus.NewBridge(ipcbus.RuntimeType(runtimeType), gatewayPort)
	if err != nil {
		logger.Error(err, "failed to create runtime bridge")
		os.Exit(1)
	}

	// Create router.
	router := ipcbus.NewRouter(ipcbus.RouterConfig{
		Bridge:        bridge,
		WAL:           wal,
		DLQ:           dlq,
		Logger:        logger,
		BufferSize:    bufferSize,
		HighWatermark: highWM,
		LowWatermark:  lowWM,
	})

	// Create server.
	server := ipcbus.NewServer(socketPath, router, logger)

	// Setup signal handling.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig)

		// Graceful shutdown sequence.
		router.SendShutdown()
		time.Sleep(5 * time.Second) // Wait for sidecars to drain.
		wal.Flush()
		bridge.Close()
		cancel()
	}()

	// Connect runtime bridge.
	bridgeCtx, bridgeCancel := context.WithTimeout(ctx, 30*time.Second)
	if err := bridge.Connect(bridgeCtx); err != nil {
		logger.Info("runtime bridge connection deferred (runtime may not be ready yet)", "error", err)
		// Non-fatal: the runtime may start after the IPC Bus.
	}
	bridgeCancel()

	// Start outbound loop (runtime → sidecars).
	if err := router.StartOutboundLoop(ctx); err != nil {
		logger.Info("outbound loop deferred", "error", err)
	}

	// Replay pending WAL entries.
	router.ReplayWAL(ctx)

	// Start WAL compaction loop.
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if wal.NeedsCompaction() {
					if err := wal.Compact(); err != nil {
						logger.Error(err, "WAL compaction failed")
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Start DLQ purge loop.
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				purged, err := dlq.PurgeExpired()
				if err != nil {
					logger.Error(err, "DLQ purge failed")
				} else if purged > 0 {
					logger.Info("purged expired DLQ entries", "count", purged)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Start UDS server (blocks until ctx cancelled).
	if err := server.Start(ctx); err != nil {
		logger.Error(err, "server error")
		os.Exit(1)
	}

	logger.Info("IPC Bus stopped")
}

// runShutdown sends a shutdown signal to the running IPC Bus via UDS.
func runShutdown(socketPath string) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to bus: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Send register as "shutdown-client" then shutdown.
	reg := ipcbus.NewMessage(ipcbus.TypeRegister, "__shutdown__", nil)
	ipcbus.WriteMessage(conn, reg)
	ipcbus.ReadMessage(conn) // ACK

	shutdown := ipcbus.NewMessage(ipcbus.TypeShutdown, "", nil)
	ipcbus.WriteMessage(conn, shutdown)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}
```

### Step 2: Verify build

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go build ./cmd/ipcbus/
```

### Step 3: Commit

```
feat: add IPC Bus binary entrypoint with serve and shutdown commands
```

---

## Task 8: Operator-side IPC Bus sidecar injection

**Files:**
- Create: `internal/controller/claw_ipcbus.go`
- Modify: `internal/controller/claw_controller.go` (wire injection into `buildStatefulSet`)
- Modify: `internal/runtime/pod_builder.go` (add preStop hooks)
- Test: `internal/controller/claw_controller_coverage_test.go`

### Step 1: Create IPC Bus sidecar injector

```go
// internal/controller/claw_ipcbus.go
package controller

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

// IPCBusImage is the default IPC Bus sidecar image.
var IPCBusImage = "ghcr.io/prismer-ai/claw-ipcbus:latest"

// shouldInjectIPCBus returns true when the IPC Bus sidecar should be injected.
// The Bus is injected whenever at least one channel is configured.
func shouldInjectIPCBus(claw *clawv1alpha1.Claw) bool {
	return len(claw.Spec.Channels) > 0
}

// ipcBusSidecarName returns the container name for the IPC Bus sidecar.
func ipcBusSidecarName() string {
	return "ipc-bus"
}

// injectIPCBusSidecar adds the IPC Bus native sidecar to the pod template.
// It should be injected before channel sidecars in init container order.
func injectIPCBusSidecar(claw *clawv1alpha1.Claw, podTemplate *corev1.PodTemplateSpec, runtimeType string, gatewayPort int) {
	env := []corev1.EnvVar{
		{Name: "CLAW_NAME", Value: claw.Name},
		{Name: "CLAW_NAMESPACE", Value: claw.Namespace},
		{Name: "CLAW_RUNTIME", Value: runtimeType},
		{Name: "CLAW_GATEWAY_PORT", Value: intToStr(gatewayPort)},
		{Name: "IPC_SOCKET_PATH", Value: "/var/run/claw/bus.sock"},
		{Name: "WAL_DIR", Value: "/var/run/claw/wal"},
	}

	// Backpressure config from CRD (if any channel has it).
	// Use first channel's config or defaults.
	for _, ch := range claw.Spec.Channels {
		if ch.Backpressure != nil {
			if ch.Backpressure.BufferSize > 0 {
				env = append(env, corev1.EnvVar{Name: "BUFFER_SIZE", Value: intToStr(ch.Backpressure.BufferSize)})
			}
			if ch.Backpressure.HighWatermark != "" {
				env = append(env, corev1.EnvVar{Name: "HIGH_WATERMARK", Value: ch.Backpressure.HighWatermark})
			}
			if ch.Backpressure.LowWatermark != "" {
				env = append(env, corev1.EnvVar{Name: "LOW_WATERMARK", Value: ch.Backpressure.LowWatermark})
			}
			break
		}
	}

	sidecar := corev1.Container{
		Name:  ipcBusSidecarName(),
		Image: IPCBusImage,
		Env:   env,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "ipc-socket", MountPath: "/var/run/claw"},
			{Name: "wal-data", MountPath: "/var/run/claw/wal"},
		},
		SecurityContext: clawruntime.ContainerSecurityContext(),
		Lifecycle: &corev1.Lifecycle{
			PreStop: &corev1.LifecycleHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"/claw-ipcbus", "shutdown"},
				},
			},
		},
		RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways),
	}

	// Prepend to init containers (before channel sidecars, after claw-init).
	// claw-init is always the first init container.
	if len(podTemplate.Spec.InitContainers) > 0 {
		// Insert after claw-init (index 1).
		updated := make([]corev1.Container, 0, len(podTemplate.Spec.InitContainers)+1)
		updated = append(updated, podTemplate.Spec.InitContainers[0]) // claw-init
		updated = append(updated, sidecar)                             // ipc-bus
		updated = append(updated, podTemplate.Spec.InitContainers[1:]...) // rest
		podTemplate.Spec.InitContainers = updated
	} else {
		podTemplate.Spec.InitContainers = append(podTemplate.Spec.InitContainers, sidecar)
	}
}

// injectIPCBusIfNeeded checks and injects the IPC Bus sidecar.
func injectIPCBusIfNeeded(claw *clawv1alpha1.Claw, podTemplate *corev1.PodTemplateSpec, runtimeType string, gatewayPort int) {
	if !shouldInjectIPCBus(claw) {
		return
	}

	// Check if already injected.
	for _, c := range podTemplate.Spec.InitContainers {
		if c.Name == ipcBusSidecarName() {
			return
		}
	}

	injectIPCBusSidecar(claw, podTemplate, runtimeType, gatewayPort)
}

func intToStr(i int) string {
	return fmt.Sprintf("%d", i)
}
```

Note: `intToStr` requires adding `"fmt"` to the imports.

### Step 2: Wire injection into `buildStatefulSet` in `claw_controller.go`

In `claw_controller.go`, add IPC Bus injection **before** channel sidecar injection (around line 339):

```go
	// Inject IPC Bus sidecar (must come before channel sidecars).
	cfg := adapter.DefaultConfig()
	injectIPCBusIfNeeded(claw, podTemplate, string(claw.Spec.Runtime), cfg.GatewayPort)

	// Inject channel sidecars.
	if len(claw.Spec.Channels) > 0 {
```

Also update the `terminationGracePeriodSeconds` calculation (line 378):

```go
	gracePeriod := int64(adapter.GracefulShutdownSeconds()) + 15
```

### Step 3: Add preStop hook to runtime container in `pod_builder.go`

In `buildRuntimeContainer()` (around line 281), add a `Lifecycle` field to the returned container:

```go
	return corev1.Container{
		Name:            "runtime",
		...
		SecurityContext: ContainerSecurityContext(),
		Lifecycle: &corev1.Lifecycle{
			PreStop: &corev1.LifecycleHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"sh", "-c", "sleep 2"},
				},
			},
		},
	}
```

### Step 4: Write operator-side unit tests

Add to `claw_controller_coverage_test.go`:

```go
func TestShouldInjectIPCBus(t *testing.T) {
	tests := []struct {
		name     string
		channels []clawv1alpha1.ChannelRef
		want     bool
	}{
		{"no channels", nil, false},
		{"one channel", []clawv1alpha1.ChannelRef{{Name: "slack"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claw := &clawv1alpha1.Claw{
				Spec: clawv1alpha1.ClawSpec{
					Channels: tt.channels,
				},
			}
			if got := shouldInjectIPCBus(claw); got != tt.want {
				t.Errorf("shouldInjectIPCBus() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInjectIPCBusSidecar(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Channels: []clawv1alpha1.ChannelRef{{Name: "slack"}},
		},
	}
	podTemplate := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{Name: "claw-init"},
			},
		},
	}
	injectIPCBusSidecar(claw, podTemplate, "openclaw", 18900)

	if len(podTemplate.Spec.InitContainers) != 2 {
		t.Fatalf("expected 2 init containers, got %d", len(podTemplate.Spec.InitContainers))
	}
	if podTemplate.Spec.InitContainers[0].Name != "claw-init" {
		t.Error("claw-init should be first")
	}
	if podTemplate.Spec.InitContainers[1].Name != "ipc-bus" {
		t.Error("ipc-bus should be second")
	}

	bus := podTemplate.Spec.InitContainers[1]
	if bus.Image != IPCBusImage {
		t.Errorf("wrong image: %s", bus.Image)
	}
	if bus.Lifecycle == nil || bus.Lifecycle.PreStop == nil {
		t.Error("expected preStop lifecycle hook")
	}
	if bus.RestartPolicy == nil || *bus.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Error("expected restartPolicy Always")
	}

	// Check env vars.
	envMap := make(map[string]string)
	for _, e := range bus.Env {
		envMap[e.Name] = e.Value
	}
	if envMap["CLAW_RUNTIME"] != "openclaw" {
		t.Errorf("CLAW_RUNTIME = %q, want openclaw", envMap["CLAW_RUNTIME"])
	}
	if envMap["CLAW_GATEWAY_PORT"] != "18900" {
		t.Errorf("CLAW_GATEWAY_PORT = %q, want 18900", envMap["CLAW_GATEWAY_PORT"])
	}
}

func TestInjectIPCBusIfNeeded_Idempotent(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{
			Channels: []clawv1alpha1.ChannelRef{{Name: "slack"}},
		},
	}
	podTemplate := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "claw-init"}},
		},
	}

	injectIPCBusIfNeeded(claw, podTemplate, "openclaw", 18900)
	injectIPCBusIfNeeded(claw, podTemplate, "openclaw", 18900) // second call

	busCount := 0
	for _, c := range podTemplate.Spec.InitContainers {
		if c.Name == "ipc-bus" {
			busCount++
		}
	}
	if busCount != 1 {
		t.Errorf("expected exactly 1 ipc-bus container, got %d", busCount)
	}
}
```

### Step 5: Run all tests

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./internal/controller/ -v -run "TestShouldInjectIPCBus|TestInjectIPCBus" -timeout 120s
```

Then full build:

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go build ./...
```

### Step 6: Commit

```
feat: inject IPC Bus sidecar and add preStop hooks for graceful shutdown
```

---

## Task 9: Graceful shutdown orchestration

**Files:**
- Create: `internal/ipcbus/shutdown.go`
- Create: `internal/ipcbus/shutdown_test.go`

### Step 1: Implement shutdown orchestrator

```go
// internal/ipcbus/shutdown.go
package ipcbus

import (
	"context"
	"time"

	"github.com/go-logr/logr"
)

// ShutdownOrchestrator handles the graceful shutdown sequence.
type ShutdownOrchestrator struct {
	router *Router
	wal    *WAL
	bridge RuntimeBridge
	logger logr.Logger
}

// NewShutdownOrchestrator creates a shutdown orchestrator.
func NewShutdownOrchestrator(router *Router, wal *WAL, bridge RuntimeBridge, logger logr.Logger) *ShutdownOrchestrator {
	return &ShutdownOrchestrator{
		router: router,
		wal:    wal,
		bridge: bridge,
		logger: logger,
	}
}

// Execute runs the graceful shutdown sequence:
// 1. Send shutdown to all sidecars
// 2. Wait for drain (up to drainTimeout)
// 3. Flush WAL
// 4. Close bridge
func (s *ShutdownOrchestrator) Execute(ctx context.Context, drainTimeout time.Duration) {
	s.logger.Info("starting graceful shutdown")

	// Step 1: Notify sidecars.
	s.router.SendShutdown()
	s.logger.Info("shutdown signal sent to sidecars")

	// Step 2: Wait for sidecars to drain.
	drainCtx, cancel := context.WithTimeout(ctx, drainTimeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-drainCtx.Done():
			s.logger.Info("drain timeout reached", "connected", s.router.ConnectedCount())
			goto flush
		case <-ticker.C:
			if s.router.ConnectedCount() == 0 {
				s.logger.Info("all sidecars disconnected")
				goto flush
			}
		}
	}

flush:
	// Step 3: Flush WAL.
	if s.wal != nil {
		if err := s.wal.Flush(); err != nil {
			s.logger.Error(err, "WAL flush failed during shutdown")
		} else {
			s.logger.Info("WAL flushed")
		}
	}

	// Step 4: Close bridge.
	if s.bridge != nil {
		if err := s.bridge.Close(); err != nil {
			s.logger.Error(err, "bridge close failed during shutdown")
		}
	}

	s.logger.Info("graceful shutdown complete")
}
```

### Step 2: Write shutdown test

```go
// internal/ipcbus/shutdown_test.go
package ipcbus

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func TestShutdownOrchestrator_Execute(t *testing.T) {
	dir := t.TempDir()
	wal, _ := NewWAL(dir)
	defer wal.Close()

	router := NewRouter(RouterConfig{
		WAL:    wal,
		Logger: logr.Discard(),
	})

	orchestrator := NewShutdownOrchestrator(router, wal, nil, logr.Discard())

	ctx := context.Background()
	// Should complete quickly with no connected sidecars.
	start := time.Now()
	orchestrator.Execute(ctx, 1*time.Second)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("shutdown took too long: %v", elapsed)
	}
}
```

### Step 3: Run tests

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./internal/ipcbus/ -v -run TestShutdown -timeout 30s
```

### Step 4: Commit

```
feat: add graceful shutdown orchestration for IPC Bus
```

---

## Task 10: Prometheus metrics

**Files:**
- Create: `internal/ipcbus/metrics.go`
- Create: `internal/ipcbus/metrics_test.go`

### Step 1: Define metrics

```go
// internal/ipcbus/metrics.go
package ipcbus

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	messagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "claw_ipcbus_messages_total",
		Help: "Total messages routed by the IPC Bus.",
	}, []string{"channel", "direction"})

	messagesInflight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "claw_ipcbus_messages_inflight",
		Help: "Currently unACKed messages.",
	})

	bufferUsageRatio = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claw_ipcbus_buffer_usage_ratio",
		Help: "Ring buffer fill ratio per channel.",
	}, []string{"channel"})

	spillTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "claw_ipcbus_spill_total",
		Help: "Messages spilled to disk due to full buffer.",
	})

	dlqTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "claw_ipcbus_dlq_total",
		Help: "Messages sent to the dead letter queue.",
	})

	dlqSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "claw_ipcbus_dlq_size",
		Help: "Current number of entries in the DLQ.",
	})

	retryTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "claw_ipcbus_retry_total",
		Help: "Delivery retry attempts.",
	})

	bridgeConnected = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "claw_ipcbus_bridge_connected",
		Help: "RuntimeBridge connection status (0=disconnected, 1=connected).",
	})

	sidecarConnections = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "claw_ipcbus_sidecar_connections",
		Help: "Number of connected sidecar clients.",
	})

	walEntries = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "claw_ipcbus_wal_entries",
		Help: "Number of pending WAL entries.",
	})
)

// RecordInbound increments the inbound message counter.
func RecordInbound(channel string) {
	messagesTotal.WithLabelValues(channel, "inbound").Inc()
}

// RecordOutbound increments the outbound message counter.
func RecordOutbound(channel string) {
	messagesTotal.WithLabelValues(channel, "outbound").Inc()
}

// RecordSpill increments the spill-to-disk counter.
func RecordSpill() {
	spillTotal.Inc()
}

// RecordDLQ increments the DLQ counter and updates size gauge.
func RecordDLQ(currentSize int) {
	dlqTotal.Inc()
	dlqSize.Set(float64(currentSize))
}

// RecordRetry increments the retry counter.
func RecordRetry() {
	retryTotal.Inc()
}

// SetBridgeConnected sets the bridge connection status.
func SetBridgeConnected(connected bool) {
	if connected {
		bridgeConnected.Set(1)
	} else {
		bridgeConnected.Set(0)
	}
}

// SetSidecarConnections sets the sidecar connection count.
func SetSidecarConnections(count int) {
	sidecarConnections.Set(float64(count))
}

// SetWALEntries sets the pending WAL entry count.
func SetWALEntries(count int) {
	walEntries.Set(float64(count))
}

// SetBufferUsage updates the buffer usage gauge for a channel.
func SetBufferUsage(channel string, ratio float64) {
	bufferUsageRatio.WithLabelValues(channel).Set(ratio)
}
```

### Step 2: Write metrics test

```go
// internal/ipcbus/metrics_test.go
package ipcbus

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordInbound(t *testing.T) {
	RecordInbound("slack")
	RecordInbound("slack")
	RecordInbound("webhook")

	count := testutil.ToFloat64(messagesTotal.WithLabelValues("slack", "inbound"))
	if count != 2 {
		t.Errorf("expected 2, got %f", count)
	}
}

func TestSetBridgeConnected(t *testing.T) {
	SetBridgeConnected(true)
	if v := testutil.ToFloat64(bridgeConnected); v != 1 {
		t.Errorf("expected 1, got %f", v)
	}
	SetBridgeConnected(false)
	if v := testutil.ToFloat64(bridgeConnected); v != 0 {
		t.Errorf("expected 0, got %f", v)
	}
}
```

### Step 3: Run tests

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./internal/ipcbus/ -v -run TestRecord -timeout 30s
```

### Step 4: Commit

```
feat: add Prometheus metrics for IPC Bus monitoring
```

---

## Task 11: Full integration test and final verification

**Files:**
- Modify: `internal/ipcbus/server_test.go` (add end-to-end test)

### Step 1: Write end-to-end integration test

Add to `server_test.go`:

```go
func TestIntegration_SidecarToRouterWithWALAndDLQ(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "bus.sock")

	wal, _ := NewWAL(filepath.Join(dir, "wal"))
	defer wal.Close()

	dlqPath := filepath.Join(dir, "dlq.db")
	dlq, _ := NewDLQ(dlqPath, 100, 24*time.Hour)
	defer dlq.Close()

	router := NewRouter(RouterConfig{
		WAL:           wal,
		DLQ:           dlq,
		Logger:        logr.Discard(),
		BufferSize:    10,
		HighWatermark: 0.8,
		LowWatermark:  0.3,
	})
	server := NewServer(socketPath, router, logr.Discard())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go server.Start(ctx)

	// Wait for socket.
	for i := 0; i < 50; i++ {
		if _, err := net.Dial("unix", socketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Connect sidecar.
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	// Register.
	WriteMessage(conn, NewMessage(TypeRegister, "test-ch", nil))
	ack, _ := ReadMessage(conn)
	if ack.Type != TypeAck {
		t.Fatalf("expected ACK, got %s", ack.Type)
	}

	// Send 5 messages.
	for i := 0; i < 5; i++ {
		msg := NewMessage(TypeMessage, "test-ch", json.RawMessage(`{"n":`+fmt.Sprintf("%d", i)+`}`))
		WriteMessage(conn, msg)
	}

	// Allow processing.
	time.Sleep(200 * time.Millisecond)

	// WAL should have entries (complete since no bridge to fail delivery).
	if len(wal.PendingEntries()) != 0 {
		t.Errorf("expected 0 pending WAL entries, got %d", len(wal.PendingEntries()))
	}

	cancel()
}
```

### Step 2: Run full test suite

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./internal/ipcbus/ -v -timeout 60s
```

Then:

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./... -timeout 120s
```

### Step 3: Verify build of all binaries

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go build ./...
```

### Step 4: Commit

```
test: add end-to-end integration test for IPC Bus
```
