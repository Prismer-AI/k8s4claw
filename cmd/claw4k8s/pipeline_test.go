package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

type mockLLMClient struct {
	analysis string
	action   string
	err      error
}

func (m *mockLLMClient) Analyze(_ context.Context, _ string) (string, string, error) {
	return m.analysis, m.action, m.err
}

func TestPipeline_AnalyzeEscalation(t *testing.T) {
	llm := &mockLLMClient{
		analysis: "Pod OOMKilled due to memory leak in handler",
		action:   `{"action":"bump-memory","params":{"target":"768Mi"},"generation":1,"source":"companion-claw"}`,
	}
	pipeline := &Pipeline{LLM: llm}

	now := metav1.Now()
	esc := &v1alpha1.ClawOpsEscalation{
		Spec: v1alpha1.ClawOpsEscalationSpec{
			Severity: v1alpha1.SeverityHigh,
			Trigger: v1alpha1.TriggerInfo{
				Type:      v1alpha1.TriggerOOMKilled,
				Message:   "Container runtime OOMKilled",
				FirstSeen: &now,
				Count:     3,
			},
		},
	}

	analysis, action, err := pipeline.analyze(context.Background(), esc)
	require.NoError(t, err)
	assert.Equal(t, "Pod OOMKilled due to memory leak in handler", analysis)
	assert.Contains(t, action, "bump-memory")
}

func TestPipeline_LLMFailureFallback(t *testing.T) {
	llm := &mockLLMClient{err: assert.AnError}
	pipeline := &Pipeline{LLM: llm, MaxRetries: 1}

	now := metav1.Now()
	esc := &v1alpha1.ClawOpsEscalation{
		Spec: v1alpha1.ClawOpsEscalationSpec{
			Severity: v1alpha1.SeverityHigh,
			Trigger: v1alpha1.TriggerInfo{
				Type:      v1alpha1.TriggerOOMKilled,
				Message:   "OOM",
				FirstSeen: &now,
				Count:     1,
			},
		},
	}

	analysis, action, err := pipeline.analyze(context.Background(), esc)
	assert.NoError(t, err, "should not error on fallback")
	assert.Contains(t, analysis, "LLM analysis unavailable")
	assert.Empty(t, action)
}

// countingLLMClient records how many times Analyze is invoked.
type countingLLMClient struct {
	calls    int
	analysis string
	action   string
	err      error
}

func (c *countingLLMClient) Analyze(_ context.Context, _ string) (string, string, error) {
	c.calls++
	return c.analysis, c.action, c.err
}

// TestPipeline_NoopFallback verifies that an LLM client returning ("", "", nil)
// (e.g. noopLLMClient when LLM_GATEWAY_URL is unset) skips retries and produces
// a synthetic fallback analysis. Without this, the pipeline returned empty
// analysis, leaving operators with no escalation context.
func TestPipeline_NoopFallback(t *testing.T) {
	llm := &countingLLMClient{} // returns ("", "", nil)
	pipeline := &Pipeline{LLM: llm, MaxRetries: 3}

	now := metav1.Now()
	esc := &v1alpha1.ClawOpsEscalation{
		Spec: v1alpha1.ClawOpsEscalationSpec{
			Severity: v1alpha1.SeverityHigh,
			Trigger: v1alpha1.TriggerInfo{
				Type:      v1alpha1.TriggerOOMKilled,
				Message:   "OOM",
				FirstSeen: &now,
				Count:     2,
			},
		},
	}

	analysis, action, err := pipeline.analyze(context.Background(), esc)
	require.NoError(t, err)
	assert.Contains(t, analysis, "LLM analysis unavailable",
		"empty (no err) result must trigger synthetic fallback")
	assert.Empty(t, action)
	assert.Equal(t, 1, llm.calls, "noop signal must skip retries (1 call, not MaxRetries)")
}
