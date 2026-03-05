package ipcbus

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

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

type Message struct {
	ID            string          `json:"id"`
	Type          MessageType     `json:"type"`
	Channel       string          `json:"channel,omitempty"`
	CorrelationID string          `json:"correlationId,omitempty"`
	ReplyTo       string          `json:"replyTo,omitempty"`
	Timestamp     time.Time       `json:"timestamp"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

func NewMessage(msgType MessageType, channel string, payload json.RawMessage) *Message {
	return &Message{
		ID:        uuid.Must(uuid.NewV7()).String(),
		Type:      msgType,
		Channel:   channel,
		Timestamp: time.Now(),
		Payload:   payload,
	}
}

func NewAck(id string) *Message {
	ref, _ := json.Marshal(map[string]string{"ref": id})
	return &Message{
		ID:            uuid.Must(uuid.NewV7()).String(),
		Type:          TypeAck,
		CorrelationID: id,
		Timestamp:     time.Now(),
		Payload:       json.RawMessage(ref),
	}
}

func (m *Message) IsControl() bool {
	switch m.Type {
	case TypeAck, TypeNack, TypeSlowDown, TypeResume, TypeShutdown, TypeRegister, TypeHeartbeat:
		return true
	}
	return false
}
