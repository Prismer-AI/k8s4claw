package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func newFakeClient(escs ...*v1alpha1.ClawOpsEscalation) client.Client {
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)
	objs := make([]client.Object, 0, len(escs))
	statusObjs := make([]client.Object, 0, len(escs))
	for _, e := range escs {
		objs = append(objs, e)
		statusObjs = append(statusObjs, e)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(statusObjs...).
		Build()
}

// approveOne is a test-only wrapper around the same logic as runApprove,
// taking an injected client (avoiding kubeconfig discovery).
func approveOne(c client.Client, name, ns, by string) error {
	ctx := context.Background()
	var esc v1alpha1.ClawOpsEscalation
	if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, &esc); err != nil {
		return err
	}
	if esc.Status.Phase != v1alpha1.EscalationPhaseAwaitingApproval {
		return phaseErr(esc.Status.Phase, v1alpha1.EscalationPhaseAwaitingApproval)
	}
	if esc.Status.ProposedAction == "" {
		return emptyActionErr()
	}
	now := metav1.Now()
	esc.Status.Phase = v1alpha1.EscalationPhaseApproved
	esc.Status.ApprovedBy = by
	esc.Status.ApprovedAt = &now
	return c.Status().Update(ctx, &esc)
}

func rejectOne(c client.Client, name, ns, reason string) error {
	ctx := context.Background()
	var esc v1alpha1.ClawOpsEscalation
	if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, &esc); err != nil {
		return err
	}
	if v1alpha1.IsTerminalPhase(esc.Status.Phase) {
		return terminalErr(esc.Status.Phase)
	}
	esc.Status.Phase = v1alpha1.EscalationPhaseRejected
	esc.Status.RejectionReason = reason
	return c.Status().Update(ctx, &esc)
}

// Sentinel errors so tests can assert behavior without parsing strings.
type phaseError struct{ got, want v1alpha1.EscalationPhase }

func (e phaseError) Error() string { return "wrong phase: got " + string(e.got) }
func phaseErr(got, want v1alpha1.EscalationPhase) error {
	return phaseError{got: got, want: want}
}

type emptyActionError struct{}

func (emptyActionError) Error() string { return "empty proposedAction" }
func emptyActionErr() error            { return emptyActionError{} }

type terminalError struct{ phase v1alpha1.EscalationPhase }

func (e terminalError) Error() string              { return "already terminal: " + string(e.phase) }
func terminalErr(p v1alpha1.EscalationPhase) error { return terminalError{phase: p} }

// ---------------------------------------------------------------------------
// approve
// ---------------------------------------------------------------------------

func TestApprove_HappyPath(t *testing.T) {
	t.Parallel()
	esc := &v1alpha1.ClawOpsEscalation{
		ObjectMeta: metav1.ObjectMeta{Name: "esc-1", Namespace: "default"},
		Spec: v1alpha1.ClawOpsEscalationSpec{
			ClawRef:  corev1.LocalObjectReference{Name: "claw-a"},
			Severity: v1alpha1.SeverityHigh,
			Trigger:  v1alpha1.TriggerInfo{Type: v1alpha1.TriggerOOMKilled},
		},
		Status: v1alpha1.ClawOpsEscalationStatus{
			Phase:          v1alpha1.EscalationPhaseAwaitingApproval,
			ProposedAction: `{"action":"bump-memory","params":{"target":"1Gi"},"generation":100,"source":"companion-claw"}`,
		},
	}
	c := newFakeClient(esc)
	require.NoError(t, approveOne(c, "esc-1", "default", "sre@corp.com"))

	var updated v1alpha1.ClawOpsEscalation
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: "esc-1", Namespace: "default"}, &updated))
	assert.Equal(t, v1alpha1.EscalationPhaseApproved, updated.Status.Phase)
	assert.Equal(t, "sre@corp.com", updated.Status.ApprovedBy)
	assert.NotNil(t, updated.Status.ApprovedAt)
}

func TestApprove_RejectsWrongPhase(t *testing.T) {
	t.Parallel()
	esc := &v1alpha1.ClawOpsEscalation{
		ObjectMeta: metav1.ObjectMeta{Name: "esc-2", Namespace: "default"},
		Spec: v1alpha1.ClawOpsEscalationSpec{
			ClawRef:  corev1.LocalObjectReference{Name: "claw-a"},
			Severity: v1alpha1.SeverityHigh,
			Trigger:  v1alpha1.TriggerInfo{Type: v1alpha1.TriggerOOMKilled},
		},
		Status: v1alpha1.ClawOpsEscalationStatus{
			Phase:          v1alpha1.EscalationPhasePending, // not AwaitingApproval
			ProposedAction: `{"action":"bump-memory","params":{},"generation":1,"source":"x"}`,
		},
	}
	c := newFakeClient(esc)
	err := approveOne(c, "esc-2", "default", "x")
	require.Error(t, err)
	assert.IsType(t, phaseError{}, err)
}

func TestApprove_RejectsEmptyProposedAction(t *testing.T) {
	t.Parallel()
	esc := &v1alpha1.ClawOpsEscalation{
		ObjectMeta: metav1.ObjectMeta{Name: "esc-3", Namespace: "default"},
		Spec: v1alpha1.ClawOpsEscalationSpec{
			ClawRef:  corev1.LocalObjectReference{Name: "claw-a"},
			Severity: v1alpha1.SeverityHigh,
			Trigger:  v1alpha1.TriggerInfo{Type: v1alpha1.TriggerOOMKilled},
		},
		Status: v1alpha1.ClawOpsEscalationStatus{
			Phase: v1alpha1.EscalationPhaseAwaitingApproval,
			// ProposedAction empty — nothing to approve
		},
	}
	c := newFakeClient(esc)
	err := approveOne(c, "esc-3", "default", "x")
	require.Error(t, err)
	assert.IsType(t, emptyActionError{}, err)
}

// ---------------------------------------------------------------------------
// reject
// ---------------------------------------------------------------------------

func TestReject_HappyPath(t *testing.T) {
	t.Parallel()
	esc := &v1alpha1.ClawOpsEscalation{
		ObjectMeta: metav1.ObjectMeta{Name: "esc-4", Namespace: "default"},
		Spec: v1alpha1.ClawOpsEscalationSpec{
			ClawRef:  corev1.LocalObjectReference{Name: "claw-a"},
			Severity: v1alpha1.SeverityHigh,
			Trigger:  v1alpha1.TriggerInfo{Type: v1alpha1.TriggerOOMKilled},
		},
		Status: v1alpha1.ClawOpsEscalationStatus{Phase: v1alpha1.EscalationPhaseAwaitingApproval},
	}
	c := newFakeClient(esc)
	require.NoError(t, rejectOne(c, "esc-4", "default", "manual fix already applied"))

	var updated v1alpha1.ClawOpsEscalation
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: "esc-4", Namespace: "default"}, &updated))
	assert.Equal(t, v1alpha1.EscalationPhaseRejected, updated.Status.Phase)
	assert.Equal(t, "manual fix already applied", updated.Status.RejectionReason)
}

func TestReject_AlreadyTerminal(t *testing.T) {
	t.Parallel()
	esc := &v1alpha1.ClawOpsEscalation{
		ObjectMeta: metav1.ObjectMeta{Name: "esc-5", Namespace: "default"},
		Spec: v1alpha1.ClawOpsEscalationSpec{
			ClawRef:  corev1.LocalObjectReference{Name: "claw-a"},
			Severity: v1alpha1.SeverityHigh,
			Trigger:  v1alpha1.TriggerInfo{Type: v1alpha1.TriggerOOMKilled},
		},
		Status: v1alpha1.ClawOpsEscalationStatus{Phase: v1alpha1.EscalationPhaseExecuted},
	}
	c := newFakeClient(esc)
	err := rejectOne(c, "esc-5", "default", "x")
	require.Error(t, err)
	assert.IsType(t, terminalError{}, err)
}
