package controller

import (
	"encoding/json"
	"fmt"
)

const (
	// AnnotationOpsIntent is the annotation key for ops intent JSON.
	AnnotationOpsIntent = "claw.prismer.ai/ops-intent"
	// AnnotationOpsIntentGen is the annotation key for intent generation counter.
	AnnotationOpsIntentGen = "claw.prismer.ai/ops-intent-gen"
)

// OpsIntent represents an ops action to be executed by ClawReconciler.
type OpsIntent struct {
	Action        string            `json:"action"`
	Params        map[string]string `json:"params"`
	Generation    int64             `json:"generation"`
	Source        string            `json:"source"`
	EscalationRef string           `json:"escalationRef,omitempty"`
}

// allowedIntentActions is the whitelist of allowed intent actions.
var allowedIntentActions = map[string]bool{
	"bump-memory":     true,
	"bump-cpu":        true,
	"restart-pod":     true,
	"rollout-restart": true,
	"scale-replicas":  true,
}

// ValidateIntent parses and validates an ops intent JSON string.
func ValidateIntent(raw string) (*OpsIntent, error) {
	var intent OpsIntent
	if err := json.Unmarshal([]byte(raw), &intent); err != nil {
		return nil, fmt.Errorf("malformed intent JSON: %w", err)
	}
	if intent.Action == "" {
		return nil, fmt.Errorf("intent action is empty")
	}
	if !allowedIntentActions[intent.Action] {
		return nil, fmt.Errorf("unknown intent action: %q", intent.Action)
	}
	return &intent, nil
}
