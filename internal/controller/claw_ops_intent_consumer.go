package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

// RestartAnnotation returns a JSON annotation value that triggers a rollout restart
// by changing the pod template annotation (kubectl rollout restart equivalent).
func (p *IntentPatch) RestartAnnotation() string {
	m := map[string]string{"restartedAt": time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.Marshal(m)
	return string(b)
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
// generation counter to prevent re-execution. Returns the new generation value.
func clearOpsIntentAnnotations(claw *clawv1alpha1.Claw, processedGen int64) int64 {
	delete(claw.Annotations, AnnotationOpsIntent)
	claw.Annotations[AnnotationOpsIntentGen] = strconv.FormatInt(processedGen, 10)
	return processedGen
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
// It parses the intent, builds and applies the patch, then clears the annotation.
func (r *ClawReconciler) consumeAndExecuteOpsIntent(ctx context.Context, claw *clawv1alpha1.Claw) error {
	logger := log.FromContext(ctx)

	intent, err := parseOpsIntent(claw)
	if err != nil {
		// Invalid intent — clear it to prevent infinite retry, log warning.
		logger.Error(err, "clearing invalid ops intent")
		if clearErr := r.clearIntentAnnotation(ctx, claw, 0); clearErr != nil {
			return fmt.Errorf("failed to clear invalid intent: %w", clearErr)
		}
		if r.Recorder != nil {
			r.Recorder.Event(claw, corev1.EventTypeWarning, EventOpsIntentRejected,
				fmt.Sprintf("Invalid ops intent rejected: %v", err))
		}
		return nil
	}
	if intent == nil {
		return nil // No intent or stale generation.
	}

	logger.Info("processing ops intent", "action", intent.Action, "generation", intent.Generation, "source", intent.Source)

	patch, err := buildIntentPatch(intent)
	if err != nil {
		logger.Error(err, "invalid intent params, clearing")
		if clearErr := r.clearIntentAnnotation(ctx, claw, intent.Generation); clearErr != nil {
			return fmt.Errorf("failed to clear rejected intent: %w", clearErr)
		}
		if r.Recorder != nil {
			r.Recorder.Event(claw, corev1.EventTypeWarning, EventOpsIntentRejected,
				fmt.Sprintf("Ops intent %q rejected: %v", intent.Action, err))
		}
		return nil
	}

	// Execute the patch.
	if err := r.applyIntentPatch(ctx, claw, intent, patch); err != nil {
		logger.Error(err, "failed to apply ops intent", "action", intent.Action)
		// Clear annotation even on failure to prevent infinite retry.
		// The error is recorded via Event.
		if clearErr := r.clearIntentAnnotation(ctx, claw, intent.Generation); clearErr != nil {
			return fmt.Errorf("failed to clear intent after error: %w", clearErr)
		}
		if r.Recorder != nil {
			r.Recorder.Event(claw, corev1.EventTypeWarning, EventOpsIntentFailed,
				fmt.Sprintf("Ops intent %q failed: %v", intent.Action, err))
		}
		return nil
	}

	// Clear the intent annotation and bump generation.
	if err := r.clearIntentAnnotation(ctx, claw, intent.Generation); err != nil {
		return fmt.Errorf("failed to clear intent annotation: %w", err)
	}

	if r.Recorder != nil {
		r.Recorder.Event(claw, corev1.EventTypeNormal, EventOpsIntentExecuted,
			fmt.Sprintf("Ops intent %q executed (source: %s, gen: %d)", intent.Action, intent.Source, intent.Generation))
	}
	logger.Info("ops intent executed", "action", intent.Action, "generation", intent.Generation)
	return nil
}

// clearIntentAnnotation patches the Claw to remove the intent annotation and update the generation counter.
func (r *ClawReconciler) clearIntentAnnotation(ctx context.Context, claw *clawv1alpha1.Claw, processedGen int64) error {
	p := client.MergeFrom(claw.DeepCopy())
	clearOpsIntentAnnotations(claw, processedGen)
	return r.Patch(ctx, claw, p)
}

// applyIntentPatch dispatches to action-specific handlers.
func (r *ClawReconciler) applyIntentPatch(ctx context.Context, claw *clawv1alpha1.Claw, intent *OpsIntent, patch *IntentPatch) error {
	switch patch.Action {
	case "bump-memory":
		return r.applyBumpMemory(ctx, claw, patch)
	case "bump-cpu":
		return r.applyBumpCPU(ctx, claw, patch)
	case "restart-pod":
		return r.applyRestartPod(ctx, claw, intent)
	case "rollout-restart":
		return r.applyRolloutRestart(ctx, claw, patch)
	case "scale-replicas":
		return r.applyScaleReplicas(ctx, claw, patch)
	default:
		return fmt.Errorf("unhandled patch action: %q", patch.Action)
	}
}

// applyBumpMemory patches the runtime container's memory limit on the StatefulSet.
func (r *ClawReconciler) applyBumpMemory(ctx context.Context, claw *clawv1alpha1.Claw, patch *IntentPatch) error {
	return r.patchRuntimeResources(ctx, claw, func(res *corev1.ResourceRequirements) {
		if res.Limits == nil {
			res.Limits = corev1.ResourceList{}
		}
		res.Limits[corev1.ResourceMemory] = patch.MemoryLimit
		// Also bump request to match if limit is lower.
		if res.Requests != nil {
			if req, ok := res.Requests[corev1.ResourceMemory]; ok && patch.MemoryLimit.Cmp(req) < 0 {
				res.Requests[corev1.ResourceMemory] = patch.MemoryLimit
			}
		}
	})
}

// applyBumpCPU patches the runtime container's CPU limit on the StatefulSet.
func (r *ClawReconciler) applyBumpCPU(ctx context.Context, claw *clawv1alpha1.Claw, patch *IntentPatch) error {
	return r.patchRuntimeResources(ctx, claw, func(res *corev1.ResourceRequirements) {
		if res.Limits == nil {
			res.Limits = corev1.ResourceList{}
		}
		res.Limits[corev1.ResourceCPU] = patch.CPULimit
		if res.Requests != nil {
			if req, ok := res.Requests[corev1.ResourceCPU]; ok && patch.CPULimit.Cmp(req) < 0 {
				res.Requests[corev1.ResourceCPU] = patch.CPULimit
			}
		}
	})
}

// patchRuntimeResources fetches the StatefulSet, finds the "runtime" container,
// applies the resource mutation, and updates the StatefulSet.
func (r *ClawReconciler) patchRuntimeResources(ctx context.Context, claw *clawv1alpha1.Claw, mutate func(*corev1.ResourceRequirements)) error {
	var sts appsv1.StatefulSet
	key := types.NamespacedName{Name: claw.Name, Namespace: claw.Namespace}
	if err := r.Get(ctx, key, &sts); err != nil {
		return fmt.Errorf("failed to get StatefulSet %s: %w", key, err)
	}

	found := false
	for i := range sts.Spec.Template.Spec.Containers {
		if sts.Spec.Template.Spec.Containers[i].Name == "runtime" {
			mutate(&sts.Spec.Template.Spec.Containers[i].Resources)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("runtime container not found in StatefulSet %s", key)
	}

	return r.Update(ctx, &sts)
}

// applyRestartPod deletes the oldest pod managed by the StatefulSet.
func (r *ClawReconciler) applyRestartPod(ctx context.Context, claw *clawv1alpha1.Claw, intent *OpsIntent) error {
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

	// Find the oldest pod by creation timestamp.
	oldest := &podList.Items[0]
	for i := 1; i < len(podList.Items); i++ {
		if podList.Items[i].CreationTimestamp.Before(&oldest.CreationTimestamp) {
			oldest = &podList.Items[i]
		}
	}

	log.FromContext(ctx).Info("deleting pod for restart", "pod", oldest.Name)
	return r.Delete(ctx, oldest)
}

// applyRolloutRestart triggers a rolling restart by adding a restart annotation
// to the StatefulSet pod template (equivalent to kubectl rollout restart).
func (r *ClawReconciler) applyRolloutRestart(ctx context.Context, claw *clawv1alpha1.Claw, patch *IntentPatch) error {
	var sts appsv1.StatefulSet
	key := types.NamespacedName{Name: claw.Name, Namespace: claw.Namespace}
	if err := r.Get(ctx, key, &sts); err != nil {
		return fmt.Errorf("failed to get StatefulSet %s: %w", key, err)
	}

	if sts.Spec.Template.Annotations == nil {
		sts.Spec.Template.Annotations = make(map[string]string)
	}
	sts.Spec.Template.Annotations["claw.prismer.ai/restartedAt"] = metav1.Now().UTC().Format(time.RFC3339)

	return r.Update(ctx, &sts)
}

// applyScaleReplicas patches the StatefulSet replica count.
func (r *ClawReconciler) applyScaleReplicas(ctx context.Context, claw *clawv1alpha1.Claw, patch *IntentPatch) error {
	var sts appsv1.StatefulSet
	key := types.NamespacedName{Name: claw.Name, Namespace: claw.Namespace}
	if err := r.Get(ctx, key, &sts); err != nil {
		return fmt.Errorf("failed to get StatefulSet %s: %w", key, err)
	}

	sts.Spec.Replicas = &patch.Replicas
	return r.Update(ctx, &sts)
}
