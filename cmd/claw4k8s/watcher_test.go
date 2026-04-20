package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	return s
}

// ---------------------------------------------------------------------------
// processEscalation — state machine transitions
// ---------------------------------------------------------------------------

func TestProcessEscalation_PendingToProposed(t *testing.T) {
	t.Parallel()
	scheme := testScheme()

	esc := &v1alpha1.ClawOpsEscalation{
		ObjectMeta: metav1.ObjectMeta{Name: "test-esc-1", Namespace: "default"},
		Spec: v1alpha1.ClawOpsEscalationSpec{
			ClawRef:  corev1.LocalObjectReference{Name: "my-claw"},
			Severity: v1alpha1.SeverityHigh,
			Trigger: v1alpha1.TriggerInfo{
				Type:    v1alpha1.TriggerOOMKilled,
				Message: "Container OOMKilled",
				Count:   3,
			},
		},
		Status: v1alpha1.ClawOpsEscalationStatus{
			Phase: v1alpha1.EscalationPhasePending,
		},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(esc).
		WithStatusSubresource(esc).
		Build()

	llm := &mockLLMClient{
		analysis: "Memory leak detected in handler goroutine",
		action:   `{"action":"bump-memory","params":{"target":"1Gi"},"generation":1,"source":"companion-claw"}`,
	}
	pipeline := &Pipeline{LLM: llm, MaxRetries: 1}
	w := &Watcher{Client: fc, Pipeline: pipeline, Namespace: "default"}

	err := w.processEscalation(context.Background(), esc)
	require.NoError(t, err)

	// Verify final status.
	var updated v1alpha1.ClawOpsEscalation
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{
		Name: "test-esc-1", Namespace: "default",
	}, &updated))

	assert.Equal(t, v1alpha1.EscalationPhaseAwaitingApproval, updated.Status.Phase)
	assert.Equal(t, "Memory leak detected in handler goroutine", updated.Status.Analysis)
	assert.Contains(t, updated.Status.ProposedAction, "bump-memory")
}

func TestProcessEscalation_LLMFallback(t *testing.T) {
	t.Parallel()
	scheme := testScheme()

	esc := &v1alpha1.ClawOpsEscalation{
		ObjectMeta: metav1.ObjectMeta{Name: "test-esc-fallback", Namespace: "default"},
		Spec: v1alpha1.ClawOpsEscalationSpec{
			ClawRef:  corev1.LocalObjectReference{Name: "my-claw"},
			Severity: v1alpha1.SeverityCritical,
			Trigger: v1alpha1.TriggerInfo{
				Type:    v1alpha1.TriggerCrashLoop,
				Message: "CrashLoopBackOff",
				Count:   5,
			},
		},
		Status: v1alpha1.ClawOpsEscalationStatus{
			Phase: v1alpha1.EscalationPhasePending,
		},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(esc).
		WithStatusSubresource(esc).
		Build()

	// LLM fails → fallback.
	llm := &mockLLMClient{err: assert.AnError}
	pipeline := &Pipeline{LLM: llm, MaxRetries: 1}
	w := &Watcher{Client: fc, Pipeline: pipeline, Namespace: "default"}

	err := w.processEscalation(context.Background(), esc)
	require.NoError(t, err)

	var updated v1alpha1.ClawOpsEscalation
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{
		Name: "test-esc-fallback", Namespace: "default",
	}, &updated))

	// Fallback: analysis present but no proposed action → AwaitingApproval for human review.
	assert.Equal(t, v1alpha1.EscalationPhaseAwaitingApproval, updated.Status.Phase)
	assert.Contains(t, updated.Status.Analysis, "LLM analysis unavailable")
	assert.Empty(t, updated.Status.ProposedAction)
}

func TestProcessEscalation_SkipsNonPending(t *testing.T) {
	t.Parallel()
	scheme := testScheme()

	phases := []v1alpha1.EscalationPhase{
		v1alpha1.EscalationPhaseAnalyzing,
		v1alpha1.EscalationPhaseProposed,
		v1alpha1.EscalationPhaseAwaitingApproval,
		v1alpha1.EscalationPhaseAutoExecuted,
		v1alpha1.EscalationPhaseExecuted,
		v1alpha1.EscalationPhaseRejected,
		v1alpha1.EscalationPhaseFailed,
	}

	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			t.Parallel()
			esc := &v1alpha1.ClawOpsEscalation{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "esc-" + string(phase),
					Namespace: "default",
				},
				Spec: v1alpha1.ClawOpsEscalationSpec{
					ClawRef:  corev1.LocalObjectReference{Name: "my-claw"},
					Severity: v1alpha1.SeverityMedium,
					Trigger:  v1alpha1.TriggerInfo{Type: v1alpha1.TriggerOOMKilled},
				},
				Status: v1alpha1.ClawOpsEscalationStatus{Phase: phase},
			}

			fc := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(esc).
				WithStatusSubresource(esc).
				Build()

			llm := &mockLLMClient{analysis: "should not be called"}
			pipeline := &Pipeline{LLM: llm, MaxRetries: 1}
			w := &Watcher{Client: fc, Pipeline: pipeline, Namespace: "default"}

			err := w.processEscalation(context.Background(), esc)
			require.NoError(t, err)

			// Phase should be unchanged.
			var updated v1alpha1.ClawOpsEscalation
			require.NoError(t, fc.Get(context.Background(), types.NamespacedName{
				Name: esc.Name, Namespace: "default",
			}, &updated))
			assert.Equal(t, phase, updated.Status.Phase)
		})
	}
}

// ---------------------------------------------------------------------------
// reconcilePending — list + process
// ---------------------------------------------------------------------------

func TestReconcilePending_ProcessesPendingOnly(t *testing.T) {
	t.Parallel()
	scheme := testScheme()

	pending := &v1alpha1.ClawOpsEscalation{
		ObjectMeta: metav1.ObjectMeta{Name: "pending-esc", Namespace: "default"},
		Spec: v1alpha1.ClawOpsEscalationSpec{
			ClawRef:  corev1.LocalObjectReference{Name: "claw-a"},
			Severity: v1alpha1.SeverityHigh,
			Trigger:  v1alpha1.TriggerInfo{Type: v1alpha1.TriggerOOMKilled, Message: "OOM", Count: 1},
		},
		Status: v1alpha1.ClawOpsEscalationStatus{Phase: v1alpha1.EscalationPhasePending},
	}
	executed := &v1alpha1.ClawOpsEscalation{
		ObjectMeta: metav1.ObjectMeta{Name: "executed-esc", Namespace: "default"},
		Spec: v1alpha1.ClawOpsEscalationSpec{
			ClawRef:  corev1.LocalObjectReference{Name: "claw-b"},
			Severity: v1alpha1.SeverityLow,
			Trigger:  v1alpha1.TriggerInfo{Type: v1alpha1.TriggerOOMKilled},
		},
		Status: v1alpha1.ClawOpsEscalationStatus{Phase: v1alpha1.EscalationPhaseExecuted},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(pending, executed).
		WithStatusSubresource(pending, executed).
		Build()

	llm := &mockLLMClient{
		analysis: "OOM analysis",
		action:   `{"action":"bump-memory","params":{"target":"512Mi"},"generation":1,"source":"companion-claw"}`,
	}
	pipeline := &Pipeline{LLM: llm, MaxRetries: 1}
	w := &Watcher{Client: fc, Pipeline: pipeline, Namespace: "default"}

	processed := w.reconcilePending(context.Background())
	assert.Equal(t, 1, processed, "should process exactly 1 Pending escalation")

	// Verify the pending one was processed.
	var updatedPending v1alpha1.ClawOpsEscalation
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{
		Name: "pending-esc", Namespace: "default",
	}, &updatedPending))
	assert.Equal(t, v1alpha1.EscalationPhaseAwaitingApproval, updatedPending.Status.Phase)

	// Verify the executed one was NOT touched.
	var updatedExecuted v1alpha1.ClawOpsEscalation
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{
		Name: "executed-esc", Namespace: "default",
	}, &updatedExecuted))
	assert.Equal(t, v1alpha1.EscalationPhaseExecuted, updatedExecuted.Status.Phase)
}

func TestReconcilePending_EmptyList(t *testing.T) {
	t.Parallel()
	scheme := testScheme()

	fc := fake.NewClientBuilder().WithScheme(scheme).Build()

	llm := &mockLLMClient{}
	pipeline := &Pipeline{LLM: llm, MaxRetries: 1}
	w := &Watcher{Client: fc, Pipeline: pipeline, Namespace: "default"}

	processed := w.reconcilePending(context.Background())
	assert.Equal(t, 0, processed)
}
