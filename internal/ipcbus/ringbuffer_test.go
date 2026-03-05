package ipcbus

import (
	"encoding/json"
	"testing"
)

func testMsg(channel string) *Message {
	return NewMessage(TypeMessage, channel, json.RawMessage("{}"))
}

func TestRingBuffer_PushPop(t *testing.T) {
	rb := NewRingBuffer(4, 0.8, 0.3)

	if rb.Len() != 0 {
		t.Fatalf("expected len 0, got %d", rb.Len())
	}

	msg := testMsg("ch1")
	ok, _ := rb.Push(msg)
	if !ok {
		t.Fatal("push should succeed")
	}
	if rb.Len() != 1 {
		t.Fatalf("expected len 1, got %d", rb.Len())
	}

	got, _ := rb.Pop()
	if got == nil {
		t.Fatal("pop should return a message")
	}
	if got.ID != msg.ID {
		t.Fatalf("expected message ID %s, got %s", msg.ID, got.ID)
	}
	if rb.Len() != 0 {
		t.Fatalf("expected len 0 after pop, got %d", rb.Len())
	}

	// Pop from empty buffer.
	got, _ = rb.Pop()
	if got != nil {
		t.Fatal("pop on empty buffer should return nil")
	}
}

func TestRingBuffer_Full(t *testing.T) {
	rb := NewRingBuffer(3, 0.8, 0.3)

	for i := range 3 {
		ok, _ := rb.Push(testMsg("ch"))
		if !ok {
			t.Fatalf("push %d should succeed", i)
		}
	}

	ok, _ := rb.Push(testMsg("ch"))
	if ok {
		t.Fatal("push to full buffer should fail")
	}
	if rb.Len() != 3 {
		t.Fatalf("expected len 3, got %d", rb.Len())
	}
}

func TestRingBuffer_WrapAround(t *testing.T) {
	const size = 4
	rb := NewRingBuffer(size, 0.8, 0.3)

	// Fill and drain twice to exercise wrap-around.
	for round := range 2 {
		msgs := make([]*Message, size)
		for i := range size {
			msgs[i] = testMsg("wrap")
			ok, _ := rb.Push(msgs[i])
			if !ok {
				t.Fatalf("round %d push %d should succeed", round, i)
			}
		}
		if rb.Len() != size {
			t.Fatalf("round %d expected len %d, got %d", round, size, rb.Len())
		}

		for i := range size {
			got, _ := rb.Pop()
			if got == nil || got.ID != msgs[i].ID {
				t.Fatalf("round %d pop %d: expected %v, got %v", round, i, msgs[i].ID, got)
			}
		}
		if rb.Len() != 0 {
			t.Fatalf("round %d expected len 0 after drain, got %d", round, rb.Len())
		}
	}
}

func TestRingBuffer_BackpressureTransitions(t *testing.T) {
	// size=10, high=0.8, low=0.3
	// SlowDown triggers at count=8 (8/10 = 0.8)
	// Normal triggers at count=3 (3/10 = 0.3)
	rb := NewRingBuffer(10, 0.8, 0.3)

	if rb.State() != StateNormal {
		t.Fatalf("initial state should be Normal, got %s", rb.State())
	}

	// Fill to 7 — should stay Normal.
	for i := range 7 {
		_, stateChanged := rb.Push(testMsg("bp"))
		if stateChanged {
			t.Fatalf("push %d should not trigger state change", i)
		}
	}
	if rb.State() != StateNormal {
		t.Fatalf("expected Normal at count=7, got %s", rb.State())
	}

	// Push 8th message — ratio = 0.8 >= highMark, transition to SlowDown.
	_, stateChanged := rb.Push(testMsg("bp"))
	if !stateChanged {
		t.Fatal("push to count=8 should trigger state change")
	}
	if rb.State() != StateSlowDown {
		t.Fatalf("expected SlowDown at count=8, got %s", rb.State())
	}

	// Push more — no additional transition.
	_, stateChanged = rb.Push(testMsg("bp"))
	if stateChanged {
		t.Fatal("push at count=9 should not trigger state change")
	}

	// Drain to count=4 — should stay SlowDown (4/10 = 0.4 > 0.3).
	for rb.Len() > 4 {
		_, sc := rb.Pop()
		if sc {
			t.Fatal("should not transition yet")
		}
	}
	if rb.State() != StateSlowDown {
		t.Fatalf("expected SlowDown at count=4, got %s", rb.State())
	}

	// Pop to count=3 — ratio = 0.3 <= lowMark, transition to Normal.
	_, stateChanged = rb.Pop()
	if !stateChanged {
		t.Fatal("pop to count=3 should trigger state change")
	}
	if rb.State() != StateNormal {
		t.Fatalf("expected Normal at count=3, got %s", rb.State())
	}
}

func TestRingBuffer_FillRatio(t *testing.T) {
	rb := NewRingBuffer(10, 0.8, 0.3)

	if rb.FillRatio() != 0.0 {
		t.Fatalf("expected ratio 0.0, got %f", rb.FillRatio())
	}

	for range 5 {
		rb.Push(testMsg("fr"))
	}

	got := rb.FillRatio()
	want := 0.5
	if got != want {
		t.Fatalf("expected ratio %f, got %f", want, got)
	}
}

func TestRingBuffer_Defaults(t *testing.T) {
	rb := NewRingBuffer(0, 0, 0)

	if rb.Cap() != 1024 {
		t.Fatalf("expected default cap 1024, got %d", rb.Cap())
	}
	// Verify watermarks are set to defaults by filling to 80%.
	for range 819 {
		rb.Push(testMsg("def"))
	}
	if rb.State() != StateNormal {
		t.Fatalf("expected Normal at 819/1024, got %s", rb.State())
	}

	// Push one more to reach 820/1024 = 0.80078... >= 0.8
	_, sc := rb.Push(testMsg("def"))
	if !sc {
		t.Fatal("expected state change at 820/1024")
	}
	if rb.State() != StateSlowDown {
		t.Fatalf("expected SlowDown, got %s", rb.State())
	}
}

func TestBackpressureState_String(t *testing.T) {
	tests := []struct {
		state BackpressureState
		want  string
	}{
		{StateNormal, "normal"},
		{StateSlowDown, "slow_down"},
		{BackpressureState(99), "unknown(99)"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("BackpressureState(%d).String() = %q, want %q", int(tt.state), got, tt.want)
		}
	}
}
