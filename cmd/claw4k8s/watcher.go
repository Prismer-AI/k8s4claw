package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const defaultPollInterval = 10 * time.Second

// Watcher polls for Pending ClawOpsEscalation CRs and processes them
// through the LLM analysis pipeline.
//
// Namespace selection:
//
//	Namespaces is empty            → cluster-wide watch (requires ClusterRole)
//	Namespaces == ["foo"]          → single-namespace watch (requires Role in foo)
//	Namespaces == ["foo", "bar"]   → multi-namespace watch (Role in each)
//
// For backwards compatibility, callers may set Namespace (singular); it is
// merged into Namespaces on Run().
//
// NOTE: Multi-namespace and cluster-wide modes only widen the *consumer* of
// escalations (this watcher). The producer side (operator's
// ClawOpsController) still detects pod issues per-Claw within
// claw.Namespace, so a single companion pod will only see escalations from
// namespaces where Claw CRs already exist. Use these wider scopes when you
// deploy claw4k8s as a plain Deployment to consolidate escalation review
// across multiple Claw-bearing namespaces; do not expect them to extend
// pod-level detection on their own.
type Watcher struct {
	Client     client.Client
	Pipeline   *Pipeline
	Notifier   Notifier // optional; defaults to noopNotifier
	Namespaces []string // namespaces to watch; empty = all
	Namespace  string   // deprecated: use Namespaces. Kept for compat.
	Interval   time.Duration
}

// Run starts the polling loop. Blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("escalation-watcher")
	w.normalizeNamespaces()
	scope := w.scopeLabel()
	logger.Info("starting escalation watcher", "scope", scope, "interval", w.pollInterval())

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

// normalizeNamespaces folds the deprecated singular Namespace field into the
// Namespaces slice. Idempotent.
func (w *Watcher) normalizeNamespaces() {
	if w.Namespace != "" {
		if !slices.Contains(w.Namespaces, w.Namespace) {
			w.Namespaces = append(w.Namespaces, w.Namespace)
		}
		w.Namespace = ""
	}
}

func (w *Watcher) scopeLabel() string {
	if len(w.Namespaces) == 0 {
		return "cluster-wide"
	}
	return fmt.Sprintf("namespaces=%v", w.Namespaces)
}

func (w *Watcher) pollInterval() time.Duration {
	if w.Interval > 0 {
		return w.Interval
	}
	return defaultPollInterval
}

// reconcilePending lists ClawOpsEscalation CRs across the configured
// namespaces (cluster-wide if Namespaces is empty) and processes any that
// are in Pending phase. Returns the count of processed items.
func (w *Watcher) reconcilePending(ctx context.Context) int {
	logger := log.FromContext(ctx).WithName("escalation-watcher")
	w.normalizeNamespaces()

	processed := 0
	if len(w.Namespaces) == 0 {
		processed += w.reconcileNamespace(ctx, logger, "")
	} else {
		for _, ns := range w.Namespaces {
			processed += w.reconcileNamespace(ctx, logger, ns)
		}
	}

	if processed > 0 {
		logger.Info("processed pending escalations", "count", processed)
	}
	return processed
}

// reconcileNamespace lists Pending escalations in a single namespace
// (or cluster-wide if ns is empty) and processes them.
func (w *Watcher) reconcileNamespace(ctx context.Context, logger logr.Logger, ns string) int {
	var escList v1alpha1.ClawOpsEscalationList
	listOpts := []client.ListOption{}
	if ns != "" {
		listOpts = append(listOpts, client.InNamespace(ns))
	}
	if err := w.Client.List(ctx, &escList, listOpts...); err != nil {
		logger.Error(err, "failed to list ClawOpsEscalations", "namespace", ns)
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
				"name", esc.Name, "namespace", esc.Namespace, "claw", esc.Spec.ClawRef.Name)
			continue
		}
		processed++
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

	// Notify external channels (Slack, Discord) — best-effort. A webhook
	// failure must not block the escalation from being approved manually.
	if n := w.notifier(); n != nil {
		notifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if nerr := n.NotifyAwaitingApproval(notifyCtx, esc); nerr != nil {
			logger.Error(nerr, "failed to send escalation notification",
				"name", esc.Name, "namespace", esc.Namespace)
		}
	}
	return nil
}

// notifier returns the configured Notifier, or a noop if none is set.
func (w *Watcher) notifier() Notifier {
	if w.Notifier == nil {
		return noopNotifier{}
	}
	return w.Notifier
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
