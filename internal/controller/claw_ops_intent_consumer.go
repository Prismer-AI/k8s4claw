package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const (
	// maxReplicasLimit is the maximum replica count allowed via ops intent.
	maxReplicasLimit = 10
)

// IntentPatch represents the validated, actionable patch derived from an OpsIntent.
type IntentPatch struct {
	Action      string
	MemoryLimit resource.Quantity
	CPULimit    resource.Quantity
	Replicas    int32
}

// parseOpsIntent reads the ops-intent annotation from a Claw, validates it,
// and checks the generation guard. Returns nil intent (no error) if:
//   - no annotation present
//   - intent generation <= last processed generation (stale/duplicate)
func parseOpsIntent(claw *clawv1alpha1.Claw) (*OpsIntent, error) {
	if claw.Annotations == nil {
		return nil, nil
	}
	raw, ok := claw.Annotations[AnnotationOpsIntent]
	if !ok || raw == "" {
		return nil, nil
	}

	intent, err := ValidateIntent(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid ops intent on %s/%s: %w", claw.Namespace, claw.Name, err)
	}

	// Generation guard: skip if already processed.
	lastGen := int64(0)
	if genStr, ok := claw.Annotations[AnnotationOpsIntentGen]; ok {
		parsed, err := strconv.ParseInt(genStr, 10, 64)
		if err == nil {
			lastGen = parsed
		}
		// Non-numeric values are treated as 0 (no guard).
	}
	if intent.Generation <= lastGen {
		return nil, nil
	}

	return intent, nil
}

// clearOpsIntentAnnotations removes the intent annotation and updates the
// generation counter to prevent re-execution. The counter is only advanced
// (never lowered) so replay protection is preserved across invalid intents.
// Returns the final generation value written.
func clearOpsIntentAnnotations(claw *clawv1alpha1.Claw, processedGen int64) int64 {
	delete(claw.Annotations, AnnotationOpsIntent)
	current := currentIntentGen(claw)
	if processedGen > current {
		claw.Annotations[AnnotationOpsIntentGen] = strconv.FormatInt(processedGen, 10)
		return processedGen
	}
	return current
}

// currentIntentGen reads the ops-intent-gen annotation and returns the parsed
// value, or 0 if missing/malformed.
func currentIntentGen(claw *clawv1alpha1.Claw) int64 {
	if claw.Annotations == nil {
		return 0
	}
	genStr, ok := claw.Annotations[AnnotationOpsIntentGen]
	if !ok {
		return 0
	}
	parsed, err := strconv.ParseInt(genStr, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

// buildIntentPatch validates intent params and builds an actionable patch.
func buildIntentPatch(intent *OpsIntent) (*IntentPatch, error) {
	patch := &IntentPatch{Action: intent.Action}

	switch intent.Action {
	case "bump-memory":
		target, ok := intent.Params["target"]
		if !ok || target == "" {
			return nil, fmt.Errorf("bump-memory requires 'target' param")
		}
		q, err := resource.ParseQuantity(target)
		if err != nil {
			return nil, fmt.Errorf("invalid memory quantity %q: %w", target, err)
		}
		patch.MemoryLimit = q

	case "bump-cpu":
		target, ok := intent.Params["target"]
		if !ok || target == "" {
			return nil, fmt.Errorf("bump-cpu requires 'target' param")
		}
		q, err := resource.ParseQuantity(target)
		if err != nil {
			return nil, fmt.Errorf("invalid CPU quantity %q: %w", target, err)
		}
		patch.CPULimit = q

	case "restart-pod":
		// No additional params required; strategy is informational.

	case "rollout-restart":
		// No params required.

	case "scale-replicas":
		repStr, ok := intent.Params["replicas"]
		if !ok || repStr == "" {
			return nil, fmt.Errorf("scale-replicas requires 'replicas' param")
		}
		n, err := strconv.ParseInt(repStr, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid replicas %q: %w", repStr, err)
		}
		if n < 1 || n > maxReplicasLimit {
			return nil, fmt.Errorf("replicas %d out of range [1, %d]", n, maxReplicasLimit)
		}
		patch.Replicas = int32(n)

	default:
		return nil, fmt.Errorf("unhandled intent action: %q", intent.Action)
	}

	return patch, nil
}

// consumeAndExecuteOpsIntent is the main entry point called from ClawReconciler.Reconcile().
// It parses the intent, applies all spec/annotation mutations in memory, then
// issues a single MergeFrom patch that commits everything atomically.
//
// The patch base MUST be captured before any mutation (including
// applyIntentPatch) so that spec changes (Replicas, Resources) are included in
// the diff. `clearOpsIntentAnnotations` and handler mutations both modify
// `claw` in-place; the patch base is a snapshot taken at entry.
func (r *ClawReconciler) consumeAndExecuteOpsIntent(ctx context.Context, claw *clawv1alpha1.Claw) error {
	logger := log.FromContext(ctx)

	// Snapshot before any mutation so MergeFrom computes a complete diff.
	patchBase := client.MergeFrom(claw.DeepCopy())

	intent, err := parseOpsIntent(claw)
	if err != nil {
		// Invalid intent — clear it to prevent infinite retry, log warning.
		// currentIntentGen preserves the replay-protection high-water mark;
		// clearOpsIntentAnnotations never lowers it.
		logger.Error(err, "clearing invalid ops intent")
		clearOpsIntentAnnotations(claw, currentIntentGen(claw))
		if patchErr := r.Patch(ctx, claw, patchBase); patchErr != nil {
			return fmt.Errorf("failed to clear invalid intent: %w", patchErr)
		}
		if r.Recorder != nil {
			r.Recorder.Event(claw, corev1.EventTypeWarning, EventOpsIntentRejected,
				fmt.Sprintf("Invalid ops intent rejected: %v", err))
		}
		return nil
	}
	if intent == nil {
		return nil // No intent or stale generation — nothing to commit.
	}

	logger.Info("processing ops intent", "action", intent.Action, "generation", intent.Generation, "source", intent.Source)

	ipatch, err := buildIntentPatch(intent)
	if err != nil {
		logger.Error(err, "invalid intent params, clearing")
		clearOpsIntentAnnotations(claw, intent.Generation)
		if patchErr := r.Patch(ctx, claw, patchBase); patchErr != nil {
			return fmt.Errorf("failed to clear rejected intent: %w", patchErr)
		}
		if r.Recorder != nil {
			r.Recorder.Event(claw, corev1.EventTypeWarning, EventOpsIntentRejected,
				fmt.Sprintf("Ops intent %q rejected: %v", intent.Action, err))
		}
		return nil
	}

	// Apply the intent to claw (spec + annotations). Handlers mutate in-place.
	// Only applyRestartPod returns an error, and it touches only external state
	// (deleting a Pod); it never mutates claw.Spec, so no rollback is needed.
	// The other handlers (bump-memory, bump-cpu, scale-replicas, rollout-restart)
	// are pure in-memory mutations and cannot fail.
	if err := r.applyIntentPatch(ctx, claw, intent, ipatch); err != nil {
		logger.Error(err, "failed to apply ops intent", "action", intent.Action)
		clearOpsIntentAnnotations(claw, intent.Generation)
		if patchErr := r.Patch(ctx, claw, patchBase); patchErr != nil {
			return fmt.Errorf("failed to clear intent after error: %w", patchErr)
		}
		if r.Recorder != nil {
			r.Recorder.Event(claw, corev1.EventTypeWarning, EventOpsIntentFailed,
				fmt.Sprintf("Ops intent %q failed: %v", intent.Action, err))
		}
		return nil
	}

	// Clear the intent annotation and bump the generation counter.
	clearOpsIntentAnnotations(claw, intent.Generation)

	// Commit spec + annotation mutations atomically.
	if err := r.Patch(ctx, claw, patchBase); err != nil {
		return fmt.Errorf("failed to commit intent patch: %w", err)
	}

	if r.Recorder != nil {
		r.Recorder.Event(claw, corev1.EventTypeNormal, EventOpsIntentExecuted,
			fmt.Sprintf("Ops intent %q executed (source: %s, gen: %d)", intent.Action, intent.Source, intent.Generation))
	}
	logger.Info("ops intent executed", "action", intent.Action, "generation", intent.Generation)
	return nil
}

// AnnotationRestartedAt is mutated by rollout-restart intents and applied to
// the StatefulSet pod template by the reconciler. Storing it on the Claw CR
// (rather than directly on the StatefulSet) means the change survives the
// adapter rebuild in ensureStatefulSet.
const AnnotationRestartedAt = "claw.prismer.ai/restartedAt"

// applyIntentPatch dispatches to action-specific handlers.
//
// IMPORTANT: Handlers for bump-memory, bump-cpu, scale-replicas, and
// rollout-restart mutate claw.Spec (or Claw annotations), NOT the StatefulSet
// directly. This is because ensureStatefulSet rebuilds the pod template from
// claw.Spec via the adapter, overwriting any direct StatefulSet patches.
// By persisting the change back into claw.Spec, the adapter rebuild picks it
// up and the change survives across reconciles. The caller (consumeAndExecuteOpsIntent)
// issues a single Patch at the end to commit all mutations atomically.
func (r *ClawReconciler) applyIntentPatch(ctx context.Context, claw *clawv1alpha1.Claw, intent *OpsIntent, patch *IntentPatch) error {
	switch patch.Action {
	case "bump-memory":
		mutateRuntimeResources(claw, func(res *corev1.ResourceRequirements) {
			if res.Limits == nil {
				res.Limits = corev1.ResourceList{}
			}
			res.Limits[corev1.ResourceMemory] = patch.MemoryLimit
			if res.Requests == nil {
				res.Requests = corev1.ResourceList{}
			}
			if req, ok := res.Requests[corev1.ResourceMemory]; !ok || patch.MemoryLimit.Cmp(req) < 0 {
				res.Requests[corev1.ResourceMemory] = patch.MemoryLimit
			}
		})
		return nil

	case "bump-cpu":
		mutateRuntimeResources(claw, func(res *corev1.ResourceRequirements) {
			if res.Limits == nil {
				res.Limits = corev1.ResourceList{}
			}
			res.Limits[corev1.ResourceCPU] = patch.CPULimit
			if res.Requests == nil {
				res.Requests = corev1.ResourceList{}
			}
			if req, ok := res.Requests[corev1.ResourceCPU]; !ok || patch.CPULimit.Cmp(req) < 0 {
				res.Requests[corev1.ResourceCPU] = patch.CPULimit
			}
		})
		return nil

	case "scale-replicas":
		claw.Spec.Replicas = ptr.To(patch.Replicas)
		return nil

	case "rollout-restart":
		if claw.Annotations == nil {
			claw.Annotations = make(map[string]string)
		}
		claw.Annotations[AnnotationRestartedAt] = time.Now().UTC().Format(time.RFC3339)
		return nil

	case "restart-pod":
		// restart-pod is not a spec mutation; delete the oldest pod directly.
		// StatefulSet controller recreates it with the same spec.
		return r.applyRestartPod(ctx, claw, intent)

	default:
		return fmt.Errorf("unhandled patch action: %q", patch.Action)
	}
}

// mutateRuntimeResources applies a mutation to claw.Spec.Resources, allocating
// it if nil. This is the single point where resource-mutation intents (bump-memory,
// bump-cpu) write into the spec.
func mutateRuntimeResources(claw *clawv1alpha1.Claw, mutate func(*corev1.ResourceRequirements)) {
	if claw.Spec.Resources == nil {
		claw.Spec.Resources = &corev1.ResourceRequirements{}
	}
	mutate(claw.Spec.Resources)
}

// applyResourceOverrides applies claw.Spec.Resources and the rollout-restart
// annotation to the pod template. Called from buildStatefulSet so the overrides
// are baked into the rebuilt pod template by every reconcile.
func applyResourceOverrides(claw *clawv1alpha1.Claw, podTemplate *corev1.PodTemplateSpec) {
	if claw.Spec.Resources != nil {
		for i := range podTemplate.Spec.Containers {
			if podTemplate.Spec.Containers[i].Name == "runtime" {
				mergeResourceRequirements(&podTemplate.Spec.Containers[i].Resources, claw.Spec.Resources)
				break
			}
		}
	}
	if stamp, ok := claw.Annotations[AnnotationRestartedAt]; ok && stamp != "" {
		if podTemplate.Annotations == nil {
			podTemplate.Annotations = make(map[string]string)
		}
		podTemplate.Annotations[AnnotationRestartedAt] = stamp
	}
}

// mergeResourceRequirements overlays src onto dst: for each resource present in
// src (memory, cpu), dst's corresponding entry is replaced. Missing entries fall
// through to the adapter default.
func mergeResourceRequirements(dst, src *corev1.ResourceRequirements) {
	if dst.Limits == nil && len(src.Limits) > 0 {
		dst.Limits = corev1.ResourceList{}
	}
	for k, v := range src.Limits {
		dst.Limits[k] = v
	}
	if dst.Requests == nil && len(src.Requests) > 0 {
		dst.Requests = corev1.ResourceList{}
	}
	for k, v := range src.Requests {
		dst.Requests[k] = v
	}
}

// applyRestartPod deletes the oldest pod managed by the StatefulSet.
func (r *ClawReconciler) applyRestartPod(ctx context.Context, claw *clawv1alpha1.Claw, _ *OpsIntent) error {
	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.InNamespace(claw.Namespace),
		client.MatchingLabels{
			"app.kubernetes.io/name":     "claw",
			"app.kubernetes.io/instance": claw.Name,
		},
	); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	if len(podList.Items) == 0 {
		return nil // No pods to restart.
	}

	oldest := &podList.Items[0]
	for i := 1; i < len(podList.Items); i++ {
		if podList.Items[i].CreationTimestamp.Before(&oldest.CreationTimestamp) {
			oldest = &podList.Items[i]
		}
	}

	log.FromContext(ctx).Info("deleting pod for restart", "pod", oldest.Name)
	return r.Delete(ctx, oldest)
}
