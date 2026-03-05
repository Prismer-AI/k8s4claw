package ipcbus

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const (
	MaxMessageSize  = 16 * 1024 * 1024
	FrameHeaderSize = 4
)

func WriteMessage(w io.Writer, msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	if len(data) > MaxMessageSize {
		return fmt.Errorf("message size %d exceeds maximum %d", len(data), MaxMessageSize)
	}

	frame := make([]byte, FrameHeaderSize+len(data))
	binary.BigEndian.PutUint32(frame, uint32(len(data)))
	copy(frame[FrameHeaderSize:], data)

	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("failed to write frame: %w", err)
	}
	return nil
}

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
