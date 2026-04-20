package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	"github.com/Prismer-AI/k8s4claw/internal/rules"
	"github.com/Prismer-AI/k8s4claw/internal/signet"
)

// ClawOpsConfig holds configurable parameters for ClawOpsController.
type ClawOpsConfig struct {
	MaxActionsPerClawPerHour int
	CircuitBreakerThreshold  int
	ClusterCircuitBreakerPct float64
	MetricPollInterval       time.Duration
	EscalationTTL            int32
}

// DefaultClawOpsConfig returns the default configuration.
func DefaultClawOpsConfig() ClawOpsConfig {
	return ClawOpsConfig{
		MaxActionsPerClawPerHour: 5,
		CircuitBreakerThreshold:  3,
		ClusterCircuitBreakerPct: 0.3,
		MetricPollInterval:       60 * time.Second,
		EscalationTTL:            604800, // 7 days
	}
}

// ClawOpsController reconciles Claw resources for autonomous ops.
type ClawOpsController struct {
	client.Client
	Scheme     *runtime.Scheme
	RuleEngine *rules.Engine
	Signer     signet.Signer
	Recorder   record.EventRecorder
	Config     ClawOpsConfig

	mu           sync.Mutex
	actionCounts map[string][]time.Time // clawName -> timestamps of recent actions
}

// InitActionCounts initializes the action counts map.
func (r *ClawOpsController) InitActionCounts() {
	r.actionCounts = make(map[string][]time.Time)
}

// SetupWithManager registers the controller with the manager.
func (r *ClawOpsController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("clawops").
		Watches(&v1alpha1.Claw{},
			&handler.EnqueueRequestForObject{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.podToClaw),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(&v1alpha1.ClawOpsEscalation{},
			handler.EnqueueRequestsFromMapFunc(r.escalationToClaw),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}

// Reconcile handles Claw ops events.
func (r *ClawOpsController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("controller", "clawops")

	var claw v1alpha1.Claw
	if err := r.Get(ctx, req.NamespacedName, &claw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 1. Process approved escalations → write intent annotations.
	if err := r.processApprovedEscalations(ctx, &claw); err != nil {
		logger.Error(err, "failed to process approved escalations")
	}

	// 2. GC expired terminal escalations.
	if err := r.gcExpiredEscalations(ctx, &claw); err != nil {
		logger.Error(err, "failed to GC escalations")
	}

	// 3. Collect signals from pods.
	signals, err := r.collectSignals(ctx, &claw)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to collect signals: %w", err)
	}
	if len(signals) == 0 {
		return ctrl.Result{}, nil
	}

	// 4. Deduplicate against active escalations.
	signals = r.deduplicateSignals(ctx, &claw, signals)
	if len(signals) == 0 {
		return ctrl.Result{}, nil
	}

	// 5. Check rate limits.
	if r.isRateLimited(claw.Name) {
		logger.Info("claw rate limited, escalating all signals", "claw", claw.Name)
		for _, sig := range signals {
			if err := r.escalate(ctx, &claw, sig); err != nil {
				logger.Error(err, "failed to escalate", "signal", sig.Type)
			}
		}
		return ctrl.Result{}, nil
	}

	// 6. Match highest priority signal.
	sig, rule, matched := r.RuleEngine.MatchHighestPriority(claw.Name, signals)
	if matched {
		if err := r.autoExecute(ctx, &claw, sig, rule); err != nil {
			logger.Error(err, "auto-execute failed, escalating", "rule", rule.ID)
			if escErr := r.escalate(ctx, &claw, sig); escErr != nil {
				logger.Error(escErr, "failed to escalate after auto-execute failure")
			}
		}
	}

	// 7. Escalate unmatched signals.
	for _, s := range signals {
		if matched && s.Type == sig.Type {
			continue
		}
		if err := r.escalate(ctx, &claw, s); err != nil {
			logger.Error(err, "failed to escalate", "signal", s.Type)
		}
	}

	return ctrl.Result{}, nil
}

// collectSignals gathers signals from all pods belonging to a Claw.
func (r *ClawOpsController) collectSignals(ctx context.Context, claw *v1alpha1.Claw) ([]rules.Signal, error) {
	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.InNamespace(claw.Namespace),
		client.MatchingLabels{"claw.prismer.ai/instance": claw.Name},
	); err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	var signals []rules.Signal
	for i := range podList.Items {
		signals = append(signals, ExtractPodSignals(&podList.Items[i])...)
	}
	return signals, nil
}

// deduplicateSignals removes signals that already have active escalations.
func (r *ClawOpsController) deduplicateSignals(ctx context.Context, claw *v1alpha1.Claw, signals []rules.Signal) []rules.Signal {
	var escList v1alpha1.ClawOpsEscalationList
	if err := r.List(ctx, &escList, client.InNamespace(claw.Namespace)); err != nil {
		return signals
	}

	activeTypes := make(map[v1alpha1.TriggerType]bool)
	for i := range escList.Items {
		if escList.Items[i].Spec.ClawRef.Name == claw.Name && !v1alpha1.IsTerminalPhase(escList.Items[i].Status.Phase) {
			activeTypes[escList.Items[i].Spec.Trigger.Type] = true
		}
	}

	var filtered []rules.Signal
	for _, sig := range signals {
		if !activeTypes[sig.Type] {
			filtered = append(filtered, sig)
		}
	}
	return filtered
}

// isRateLimited checks if a Claw has exceeded its action budget.
func (r *ClawOpsController) isRateLimited(clawName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-1 * time.Hour)
	var recent []time.Time
	for _, t := range r.actionCounts[clawName] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	r.actionCounts[clawName] = recent
	return len(recent) >= r.Config.MaxActionsPerClawPerHour
}

func (r *ClawOpsController) recordAction(clawName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actionCounts[clawName] = append(r.actionCounts[clawName], time.Now())
}

// escalate creates a ClawOpsEscalation CR for a signal.
func (r *ClawOpsController) escalate(ctx context.Context, claw *v1alpha1.Claw, sig rules.Signal) error {
	ttl := r.Config.EscalationTTL
	now := metav1.Now()
	esc := &v1alpha1.ClawOpsEscalation{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-ops-", claw.Name),
			Namespace:    claw.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       "Claw",
				Name:       claw.Name,
				UID:        claw.UID,
			}},
		},
		Spec: v1alpha1.ClawOpsEscalationSpec{
			ClawRef: corev1.LocalObjectReference{Name: claw.Name},
			Trigger: v1alpha1.TriggerInfo{
				Type:      sig.Type,
				Message:   sig.Message,
				FirstSeen: &now,
				Count:     sig.Count,
			},
			Severity:                sig.Severity,
			TTLSecondsAfterFinished: &ttl,
		},
	}
	if err := r.Create(ctx, esc); err != nil {
		return fmt.Errorf("failed to create escalation: %w", err)
	}

	esc.Status.Phase = v1alpha1.EscalationPhasePending
	return r.Status().Update(ctx, esc)
}

// autoExecute handles the fast path: rule matched → sign → intent → audit CR.
func (r *ClawOpsController) autoExecute(ctx context.Context, claw *v1alpha1.Claw, sig rules.Signal, rule rules.Rule) error {
	logger := log.FromContext(ctx)

	// Sign the action.
	receipt, err := r.Signer.Sign(signet.SignRequest{
		Key:    "rule-engine",
		Tool:   string(rule.Action.Type),
		Params: rule.Action.Params,
		Target: fmt.Sprintf("claw://%s/%s", claw.Namespace, claw.Name),
	})
	if err != nil {
		logger.Error(err, "signet signing failed, proceeding without signature")
		receipt = ""
	}

	// Build intent JSON.
	intentJSON, err := buildIntentJSON(rule)
	if err != nil {
		return fmt.Errorf("failed to build intent JSON: %w", err)
	}

	// Write intent annotation.
	patch := client.MergeFrom(claw.DeepCopy())
	if claw.Annotations == nil {
		claw.Annotations = make(map[string]string)
	}
	claw.Annotations[AnnotationOpsIntent] = intentJSON
	if err := r.Patch(ctx, claw, patch); err != nil {
		return fmt.Errorf("failed to write intent annotation: %w", err)
	}

	// Create audit record.
	ttl := r.Config.EscalationTTL
	now := metav1.Now()
	esc := &v1alpha1.ClawOpsEscalation{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-ops-", claw.Name),
			Namespace:    claw.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       "Claw",
				Name:       claw.Name,
				UID:        claw.UID,
			}},
		},
		Spec: v1alpha1.ClawOpsEscalationSpec{
			ClawRef: corev1.LocalObjectReference{Name: claw.Name},
			Trigger: v1alpha1.TriggerInfo{
				Type:      sig.Type,
				Message:   sig.Message,
				FirstSeen: &now,
				Count:     sig.Count,
			},
			Severity:                sig.Severity,
			TTLSecondsAfterFinished: &ttl,
		},
	}
	if err := r.Create(ctx, esc); err != nil {
		return fmt.Errorf("failed to create escalation record: %w", err)
	}

	esc.Status.Phase = v1alpha1.EscalationPhaseAutoExecuted
	esc.Status.MatchedRule = rule.ID
	esc.Status.ExecutedAction = intentJSON
	esc.Status.ExecutedAt = &now
	esc.Status.SignetReceipt = receipt
	if err := r.Status().Update(ctx, esc); err != nil {
		return fmt.Errorf("failed to update escalation status: %w", err)
	}

	r.recordAction(claw.Name)
	r.RuleEngine.RecordExecution(claw.Name, rule.ID)

	r.Recorder.Eventf(claw, corev1.EventTypeNormal, "AutoRemediation",
		"Rule %q triggered: %s", rule.ID, intentJSON)

	return nil
}

// processApprovedEscalations finds Approved escalations and writes intent annotations.
func (r *ClawOpsController) processApprovedEscalations(ctx context.Context, claw *v1alpha1.Claw) error {
	var escList v1alpha1.ClawOpsEscalationList
	if err := r.List(ctx, &escList, client.InNamespace(claw.Namespace)); err != nil {
		return fmt.Errorf("failed to list escalations: %w", err)
	}

	for i := range escList.Items {
		esc := &escList.Items[i]
		if esc.Spec.ClawRef.Name != claw.Name || esc.Status.Phase != v1alpha1.EscalationPhaseApproved {
			continue
		}
		if esc.Status.ProposedAction == "" {
			continue
		}

		patch := client.MergeFrom(claw.DeepCopy())
		if claw.Annotations == nil {
			claw.Annotations = make(map[string]string)
		}
		claw.Annotations[AnnotationOpsIntent] = esc.Status.ProposedAction
		if err := r.Patch(ctx, claw, patch); err != nil {
			return fmt.Errorf("failed to write approved intent: %w", err)
		}

		now := metav1.Now()
		esc.Status.Phase = v1alpha1.EscalationPhaseExecuted
		esc.Status.ExecutedAction = esc.Status.ProposedAction
		esc.Status.ExecutedAt = &now
		if err := r.Status().Update(ctx, esc); err != nil {
			return fmt.Errorf("failed to update escalation to Executed: %w", err)
		}

		r.Recorder.Eventf(claw, corev1.EventTypeNormal, "ApprovedRemediation",
			"Executed approved action from escalation %s", esc.Name)
	}
	return nil
}

// gcExpiredEscalations deletes terminal escalations past their TTL.
func (r *ClawOpsController) gcExpiredEscalations(ctx context.Context, claw *v1alpha1.Claw) error {
	var escList v1alpha1.ClawOpsEscalationList
	if err := r.List(ctx, &escList, client.InNamespace(claw.Namespace)); err != nil {
		return fmt.Errorf("failed to list escalations for GC: %w", err)
	}

	for i := range escList.Items {
		esc := &escList.Items[i]
		if esc.Spec.ClawRef.Name != claw.Name || !v1alpha1.IsTerminalPhase(esc.Status.Phase) {
			continue
		}
		ttl := r.Config.EscalationTTL
		if esc.Spec.TTLSecondsAfterFinished != nil {
			ttl = *esc.Spec.TTLSecondsAfterFinished
		}
		if time.Since(esc.CreationTimestamp.Time) > time.Duration(ttl)*time.Second {
			if err := r.Delete(ctx, esc); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete expired escalation %s: %w", esc.Name, err)
			}
		}
	}
	return nil
}

func buildIntentJSON(rule rules.Rule) (string, error) {
	intent := OpsIntent{
		Action:     ruleActionToIntentAction(rule),
		Params:     rule.Action.Params,
		Generation: time.Now().UnixMilli(),
		Source:     "rule-engine",
	}
	data, err := json.Marshal(intent)
	if err != nil {
		return "", fmt.Errorf("failed to marshal intent: %w", err)
	}
	return string(data), nil
}

func ruleActionToIntentAction(rule rules.Rule) string {
	switch rule.Action.Type {
	case rules.ActionPatchResource:
		if field, ok := rule.Action.Params["field"]; ok {
			switch field {
			case "memory-limit":
				return "bump-memory"
			case "cpu-request":
				return "bump-cpu"
			}
		}
		return "bump-memory"
	case rules.ActionRestartPod:
		return "restart-pod"
	case rules.ActionScaleReplicas:
		return "scale-replicas"
	default:
		return string(rule.Action.Type)
	}
}

// --- Mappers ---

func (r *ClawOpsController) podToClaw(_ context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	clawName, ok := pod.Labels["claw.prismer.ai/instance"]
	if !ok {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: clawName, Namespace: pod.Namespace},
	}}
}

func (r *ClawOpsController) escalationToClaw(_ context.Context, obj client.Object) []reconcile.Request {
	esc, ok := obj.(*v1alpha1.ClawOpsEscalation)
	if !ok {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: esc.Spec.ClawRef.Name, Namespace: esc.Namespace},
	}}
}
