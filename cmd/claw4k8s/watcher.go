package main

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const defaultPollInterval = 10 * time.Second

// Watcher polls for Pending ClawOpsEscalation CRs and processes them
// through the LLM analysis pipeline.
type Watcher struct {
	Client    client.Client
	Pipeline  *Pipeline
	Namespace string
	Interval  time.Duration
}

// Run starts the polling loop. Blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("escalation-watcher")
	logger.Info("starting escalation watcher", "namespace", w.Namespace, "interval", w.pollInterval())

	// Process once immediately on startup.
	w.reconcilePending(ctx)

	ticker := time.NewTicker(w.pollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("escalation watcher stopping")
			return nil
		case <-ticker.C:
			w.reconcilePending(ctx)
		}
	}
}

func (w *Watcher) pollInterval() time.Duration {
	if w.Interval > 0 {
		return w.Interval
	}
	return defaultPollInterval
}

// reconcilePending lists all ClawOpsEscalation CRs in the namespace
// and processes any that are in Pending phase. Returns the count of processed items.
func (w *Watcher) reconcilePending(ctx context.Context) int {
	logger := log.FromContext(ctx).WithName("escalation-watcher")

	var escList v1alpha1.ClawOpsEscalationList
	if err := w.Client.List(ctx, &escList, client.InNamespace(w.Namespace)); err != nil {
		logger.Error(err, "failed to list ClawOpsEscalations")
		return 0
	}

	processed := 0
	for i := range escList.Items {
		esc := &escList.Items[i]
		if esc.Status.Phase != v1alpha1.EscalationPhasePending {
			continue
		}
		if err := w.processEscalation(ctx, esc); err != nil {
			logger.Error(err, "failed to process escalation",
				"name", esc.Name, "claw", esc.Spec.ClawRef.Name)
			continue
		}
		processed++
	}

	if processed > 0 {
		logger.Info("processed pending escalations", "count", processed)
	}
	return processed
}

// processEscalation drives the state machine for a single escalation:
//
//	Pending → Analyzing → (LLM pipeline) → AwaitingApproval
//
// Only processes Pending escalations; all other phases are skipped.
func (w *Watcher) processEscalation(ctx context.Context, esc *v1alpha1.ClawOpsEscalation) error {
	if esc.Status.Phase != v1alpha1.EscalationPhasePending {
		return nil
	}

	logger := log.FromContext(ctx).WithName("escalation-watcher")
	logger.Info("processing escalation",
		"name", esc.Name, "claw", esc.Spec.ClawRef.Name,
		"trigger", esc.Spec.Trigger.Type, "severity", esc.Spec.Severity)

	// Transition: Pending → Analyzing.
	if err := w.updatePhase(ctx, esc, v1alpha1.EscalationPhaseAnalyzing); err != nil {
		return fmt.Errorf("failed to set phase Analyzing: %w", err)
	}

	// Run LLM analysis pipeline.
	analysis, action, err := w.Pipeline.analyze(ctx, esc)
	if err != nil {
		// Pipeline returned a hard error (e.g., context cancelled).
		if failErr := w.setFailed(ctx, esc, fmt.Sprintf("pipeline error: %v", err)); failErr != nil {
			logger.Error(failErr, "failed to set phase Failed")
		}
		return fmt.Errorf("pipeline error: %w", err)
	}

	// Write analysis results to status.
	esc.Status.Analysis = analysis
	esc.Status.ProposedAction = action

	// Transition: Analyzing → AwaitingApproval.
	// All proposals go to human review (Phase D). Phase A may add auto-execute
	// for low-severity actions based on Signet policy.
	esc.Status.Phase = v1alpha1.EscalationPhaseAwaitingApproval
	if err := w.Client.Status().Update(ctx, esc); err != nil {
		return fmt.Errorf("failed to update status to AwaitingApproval: %w", err)
	}

	logger.Info("escalation analyzed",
		"name", esc.Name, "phase", esc.Status.Phase,
		"hasProposedAction", action != "")
	return nil
}

// updatePhase sets the escalation phase via status subresource update.
func (w *Watcher) updatePhase(ctx context.Context, esc *v1alpha1.ClawOpsEscalation, phase v1alpha1.EscalationPhase) error {
	esc.Status.Phase = phase
	return w.Client.Status().Update(ctx, esc)
}

// setFailed transitions an escalation to the Failed terminal phase.
func (w *Watcher) setFailed(ctx context.Context, esc *v1alpha1.ClawOpsEscalation, reason string) error {
	esc.Status.Phase = v1alpha1.EscalationPhaseFailed
	esc.Status.Analysis = reason
	return w.Client.Status().Update(ctx, esc)
}
