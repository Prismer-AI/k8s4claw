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
