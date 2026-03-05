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
	_, err := ReadMessage(&buf)
	if err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestReadMessage_OversizedFrame(t *testing.T) {
	header := []byte{0x01, 0x40, 0x00, 0x00}
	r := io.MultiReader(bytes.NewReader(header), strings.NewReader(""))
	_, err := ReadMessage(r)
	if err == nil {
		t.Fatal("expected error for oversized frame")
	}
}
