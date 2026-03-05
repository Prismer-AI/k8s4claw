package channel

import (
	"encoding/json"
	"testing"
)

func TestBuffer_PushPop(t *testing.T) {
	b := newBuffer(4)
	msg := newMessage(typeMessage, "test", json.RawMessage(`{}`))

	if !b.push(msg) {
		t.Fatal("push to empty buffer should succeed")
	}
	if b.len() != 1 {
		t.Fatalf("len = %d, want 1", b.len())
	}

	got := b.pop()
	if got == nil {
		t.Fatal("pop should return message")
	}
	if got.ID != msg.ID {
		t.Errorf("ID = %q, want %q", got.ID, msg.ID)
	}
	if b.len() != 0 {
		t.Fatalf("len = %d, want 0", b.len())
	}
}

func TestBuffer_Full(t *testing.T) {
	b := newBuffer(2)
	b.push(newMessage(typeMessage, "a", nil))
	b.push(newMessage(typeMessage, "b", nil))

	if b.push(newMessage(typeMessage, "c", nil)) {
		t.Fatal("push to full buffer should fail")
	}
}

func TestBuffer_PopEmpty(t *testing.T) {
	b := newBuffer(4)
	if got := b.pop(); got != nil {
		t.Fatalf("pop on empty buffer returned %v", got)
	}
}

func TestBuffer_FIFO(t *testing.T) {
	b := newBuffer(4)
	m1 := newMessage(typeMessage, "ch", json.RawMessage(`"first"`))
	m2 := newMessage(typeMessage, "ch", json.RawMessage(`"second"`))
	b.push(m1)
	b.push(m2)

	got1 := b.pop()
	got2 := b.pop()
	if got1.ID != m1.ID {
		t.Errorf("first pop ID = %q, want %q", got1.ID, m1.ID)
	}
	if got2.ID != m2.ID {
		t.Errorf("second pop ID = %q, want %q", got2.ID, m2.ID)
	}
}

func TestBuffer_DrainAll(t *testing.T) {
	b := newBuffer(4)
	b.push(newMessage(typeMessage, "ch", nil))
	b.push(newMessage(typeMessage, "ch", nil))
	b.push(newMessage(typeMessage, "ch", nil))

	msgs := b.drainAll()
	if len(msgs) != 3 {
		t.Fatalf("drainAll returned %d, want 3", len(msgs))
	}
	if b.len() != 0 {
		t.Fatalf("len after drainAll = %d, want 0", b.len())
	}
}
