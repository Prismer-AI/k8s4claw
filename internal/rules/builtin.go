package rules

import (
	"time"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// DefaultRules are the built-in auto-remediation rules.
var DefaultRules = []Rule{
	{
		ID: "oom-bump-memory",
		Match: MatchCriteria{
			SignalType:  v1alpha1.TriggerOOMKilled,
			MinSeverity: v1alpha1.SeverityHigh,
			MinCount:    2,
		},
		Action: ActionSpec{
			Type: ActionPatchResource,
			Params: map[string]string{
				"field":    "memory-limit",
				"strategy": "multiply-1.5",
				"max":      "4Gi",
			},
		},
		Cooldown: 30 * time.Minute,
	},
	{
		ID: "crashloop-restart-pod",
		Match: MatchCriteria{
			SignalType:  v1alpha1.TriggerCrashLoop,
			MinSeverity: v1alpha1.SeverityHigh,
			MinCount:    5,
		},
		Action: ActionSpec{
			Type:   ActionRestartPod,
			Params: map[string]string{"strategy": "delete-oldest"},
		},
		Cooldown: 10 * time.Minute,
	},
	{
		ID: "high-cpu-bump-request",
		Match: MatchCriteria{
			SignalType:  v1alpha1.TriggerHighCPU,
			MinSeverity: v1alpha1.SeverityMedium,
			MinCount:    1,
		},
		Action: ActionSpec{
			Type: ActionPatchResource,
			Params: map[string]string{
				"field":    "cpu-request",
				"strategy": "multiply-1.5",
				"max":      "4",
			},
		},
		Cooldown: 30 * time.Minute,
	},
}
