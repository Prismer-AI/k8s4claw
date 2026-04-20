package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateIntent_Valid(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"bump-memory", `{"action":"bump-memory","params":{"target":"768Mi"},"generation":1,"source":"rule-engine"}`},
		{"bump-cpu", `{"action":"bump-cpu","params":{"target":"500m"},"generation":2,"source":"rule-engine"}`},
		{"restart-pod", `{"action":"restart-pod","params":{"strategy":"delete-oldest"},"generation":3,"source":"companion-claw"}`},
		{"rollout-restart", `{"action":"rollout-restart","params":{},"generation":4,"source":"rule-engine"}`},
		{"scale-replicas", `{"action":"scale-replicas","params":{"replicas":"3"},"generation":5,"source":"rule-engine"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent, err := ValidateIntent(tt.raw)
			assert.NoError(t, err)
			assert.NotNil(t, intent)
			assert.Equal(t, tt.name, intent.Action)
		})
	}
}

func TestValidateIntent_Rejected(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"unknown action", `{"action":"delete-namespace","params":{},"generation":1,"source":"rule-engine"}`},
		{"malformed JSON", `{invalid`},
		{"empty action", `{"action":"","params":{},"generation":1,"source":"rule-engine"}`},
		{"image injection", `{"action":"set-image","params":{"image":"evil:latest"},"generation":1,"source":"rule-engine"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent, err := ValidateIntent(tt.raw)
			assert.Error(t, err)
			assert.Nil(t, intent)
		})
	}
}

func TestValidateIntent_GenerationCheck(t *testing.T) {
	raw := `{"action":"bump-memory","params":{"target":"768Mi"},"generation":5,"source":"rule-engine"}`
	intent, err := ValidateIntent(raw)
	assert.NoError(t, err)
	assert.Equal(t, int64(5), intent.Generation)
}

func TestValidateIntent_SourcePreserved(t *testing.T) {
	raw := `{"action":"bump-memory","params":{"target":"768Mi"},"generation":1,"source":"companion-claw","escalationRef":"my-claw-ops-abc"}`
	intent, err := ValidateIntent(raw)
	assert.NoError(t, err)
	assert.Equal(t, "companion-claw", intent.Source)
	assert.Equal(t, "my-claw-ops-abc", intent.EscalationRef)
}
