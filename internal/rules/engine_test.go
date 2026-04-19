package rules

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestEngine_Match_OOMBumpMemory(t *testing.T) {
	engine := NewEngine(DefaultRules)
	sig := Signal{
		Type:     v1alpha1.TriggerOOMKilled,
		Severity: v1alpha1.SeverityHigh,
		Count:    3,
	}
	matched, rule := engine.Match("test-claw", sig)
	assert.True(t, matched)
	assert.Equal(t, "oom-bump-memory", rule.ID)
}

func TestEngine_Match_Debounce(t *testing.T) {
	engine := NewEngine(DefaultRules)
	sig := Signal{
		Type:     v1alpha1.TriggerOOMKilled,
		Severity: v1alpha1.SeverityHigh,
		Count:    1, // below MinCount=2
	}
	matched, _ := engine.Match("test-claw", sig)
	assert.False(t, matched, "should not match with count below MinCount")
}

func TestEngine_Match_Cooldown(t *testing.T) {
	rules := []Rule{{
		ID: "test-rule",
		Match: MatchCriteria{
			SignalType:  v1alpha1.TriggerOOMKilled,
			MinSeverity: v1alpha1.SeverityLow,
			MinCount:    1,
		},
		Action:   ActionSpec{Type: ActionPatchResource},
		Cooldown: 1 * time.Hour,
	}}
	engine := NewEngine(rules)
	sig := Signal{
		Type:     v1alpha1.TriggerOOMKilled,
		Severity: v1alpha1.SeverityHigh,
		Count:    5,
	}

	matched, _ := engine.Match("claw-a", sig)
	assert.True(t, matched)
	engine.RecordExecution("claw-a", "test-rule")

	matched, _ = engine.Match("claw-a", sig)
	assert.False(t, matched, "should be in cooldown")

	matched, _ = engine.Match("claw-b", sig)
	assert.True(t, matched, "different claw should still match")
}

func TestEngine_Match_SeverityFilter(t *testing.T) {
	rules := []Rule{{
		ID: "high-only",
		Match: MatchCriteria{
			SignalType:  v1alpha1.TriggerCrashLoop,
			MinSeverity: v1alpha1.SeverityHigh,
			MinCount:    1,
		},
		Action: ActionSpec{Type: ActionRestartPod},
	}}
	engine := NewEngine(rules)

	medium := Signal{Type: v1alpha1.TriggerCrashLoop, Severity: v1alpha1.SeverityMedium, Count: 5}
	matched, _ := engine.Match("claw", medium)
	assert.False(t, matched, "medium should not match high-only rule")

	high := Signal{Type: v1alpha1.TriggerCrashLoop, Severity: v1alpha1.SeverityHigh, Count: 5}
	matched, _ = engine.Match("claw", high)
	assert.True(t, matched)
}

func TestEngine_Match_NoMatch(t *testing.T) {
	engine := NewEngine(DefaultRules)
	sig := Signal{
		Type:     v1alpha1.TriggerUnknown,
		Severity: v1alpha1.SeverityMedium,
		Count:    1,
	}
	matched, _ := engine.Match("claw", sig)
	assert.False(t, matched)
}

func TestEngine_HighestPriority(t *testing.T) {
	engine := NewEngine(DefaultRules)
	signals := []Signal{
		{Type: v1alpha1.TriggerHighCPU, Severity: v1alpha1.SeverityMedium, Count: 5},
		{Type: v1alpha1.TriggerOOMKilled, Severity: v1alpha1.SeverityHigh, Count: 3},
	}
	sig, rule, ok := engine.MatchHighestPriority("claw", signals)
	require.True(t, ok)
	assert.Equal(t, v1alpha1.TriggerOOMKilled, sig.Type)
	assert.Equal(t, "oom-bump-memory", rule.ID)
}
