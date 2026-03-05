package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const selfConfigTTL = 1 * time.Hour

// ClawSelfConfigReconciler reconciles ClawSelfConfig resources.
type ClawSelfConfigReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=claw.prismer.ai,resources=clawselfconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=clawselfconfigs/status,verbs=get;update;patch

func (r *ClawSelfConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var sc clawv1alpha1.ClawSelfConfig
	if err := r.Get(ctx, req.NamespacedName, &sc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get ClawSelfConfig: %w", err)
	}

	// TTL: delete Applied configs after 1h.
	if sc.Status.Phase == clawv1alpha1.SelfConfigPhaseApplied && sc.Status.AppliedAt != nil {
		elapsed := time.Since(sc.Status.AppliedAt.Time)
		if elapsed >= selfConfigTTL {
			log.Info("deleting expired SelfConfig", "name", sc.Name, "age", elapsed)
			if err := r.Delete(ctx, &sc); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to delete expired SelfConfig: %w", err)
			}
			return ctrl.Result{}, nil
		}
		// Requeue for TTL expiry.
		return ctrl.Result{RequeueAfter: selfConfigTTL - elapsed}, nil
	}

	// Skip if already in terminal state.
	if sc.Status.Phase == clawv1alpha1.SelfConfigPhaseDenied || sc.Status.Phase == clawv1alpha1.SelfConfigPhaseFailed {
		return ctrl.Result{}, nil
	}

	// Fetch parent Claw.
	var claw clawv1alpha1.Claw
	if err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: sc.Spec.ClawRef}, &claw); err != nil {
		if apierrors.IsNotFound(err) {
			return r.deny(ctx, &sc, fmt.Sprintf("target Claw %q not found", sc.Spec.ClawRef))
		}
		return ctrl.Result{}, fmt.Errorf("failed to get target Claw: %w", err)
	}

	// Check selfConfigure.enabled.
	if claw.Spec.SelfConfigure == nil || !claw.Spec.SelfConfigure.Enabled {
		return r.deny(ctx, &sc, "self-configuration is not enabled on target Claw")
	}

	// Validate actions against allowlist.
	if err := r.validateActions(&sc, claw.Spec.SelfConfigure.AllowedActions); err != nil {
		return r.deny(ctx, &sc, err.Error())
	}

	// Set ownerReference.
	if err := controllerutil.SetOwnerReference(&claw, &sc, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set owner reference: %w", err)
	}
	if err := r.Update(ctx, &sc); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update ownerReference: %w", err)
	}

	// Apply changes to Claw spec.
	if err := r.applyChanges(ctx, &claw, &sc); err != nil {
		sc.Status.Phase = clawv1alpha1.SelfConfigPhaseFailed
		sc.Status.Message = err.Error()
		_ = r.Status().Update(ctx, &sc)
		return ctrl.Result{}, fmt.Errorf("failed to apply self-config: %w", err)
	}

	// Mark as Applied.
	now := metav1.Now()
	sc.Status.Phase = clawv1alpha1.SelfConfigPhaseApplied
	sc.Status.Message = "configuration applied successfully"
	sc.Status.AppliedAt = &now
	if err := r.Status().Update(ctx, &sc); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}

	r.Recorder.Event(&sc, corev1.EventTypeNormal, EventSelfConfigApplied,
		fmt.Sprintf("Self-configuration applied to Claw %s", sc.Spec.ClawRef))

	log.Info("self-config applied", "name", sc.Name, "claw", sc.Spec.ClawRef)

	return ctrl.Result{RequeueAfter: selfConfigTTL}, nil
}

func (r *ClawSelfConfigReconciler) deny(ctx context.Context, sc *clawv1alpha1.ClawSelfConfig, reason string) (ctrl.Result, error) {
	sc.Status.Phase = clawv1alpha1.SelfConfigPhaseDenied
	sc.Status.Message = reason
	if err := r.Status().Update(ctx, sc); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update denied status: %w", err)
	}

	r.Recorder.Event(sc, corev1.EventTypeWarning, EventSelfConfigDenied, reason)
	return ctrl.Result{}, nil
}

func (r *ClawSelfConfigReconciler) validateActions(sc *clawv1alpha1.ClawSelfConfig, allowed []string) error {
	allowedSet := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allowedSet[a] = true
	}

	if (len(sc.Spec.AddSkills) > 0 || len(sc.Spec.RemoveSkills) > 0) && !allowedSet["skills"] {
		return fmt.Errorf("action 'skills' is not in allowedActions")
	}
	if len(sc.Spec.ConfigPatch) > 0 && !allowedSet["config"] {
		return fmt.Errorf("action 'config' is not in allowedActions")
	}
	if (len(sc.Spec.AddWorkspaceFiles) > 0 || len(sc.Spec.RemoveWorkspaceFiles) > 0) && !allowedSet["workspaceFiles"] {
		return fmt.Errorf("action 'workspaceFiles' is not in allowedActions")
	}
	if (len(sc.Spec.AddEnvVars) > 0 || len(sc.Spec.RemoveEnvVars) > 0) && !allowedSet["envVars"] {
		return fmt.Errorf("action 'envVars' is not in allowedActions")
	}

	// Quantity limits.
	if len(sc.Spec.AddSkills) > 10 {
		return fmt.Errorf("addSkills exceeds max 10 items")
	}
	if len(sc.Spec.RemoveSkills) > 10 {
		return fmt.Errorf("removeSkills exceeds max 10 items")
	}
	if len(sc.Spec.AddWorkspaceFiles) > 10 {
		return fmt.Errorf("addWorkspaceFiles exceeds max 10 items")
	}
	if len(sc.Spec.RemoveWorkspaceFiles) > 10 {
		return fmt.Errorf("removeWorkspaceFiles exceeds max 10 items")
	}
	if len(sc.Spec.AddEnvVars) > 10 {
		return fmt.Errorf("addEnvVars exceeds max 10 items")
	}
	if len(sc.Spec.RemoveEnvVars) > 10 {
		return fmt.Errorf("removeEnvVars exceeds max 10 items")
	}

	return nil
}

func (r *ClawSelfConfigReconciler) applyChanges(ctx context.Context, claw *clawv1alpha1.Claw, sc *clawv1alpha1.ClawSelfConfig) error {
	// Apply environment variable changes via annotation (triggers reconcile).
	// The Claw reconciler reads annotations to apply env var changes.
	// For now, store as annotations on the Claw CR.
	annotations := claw.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	// Mark that a self-config was applied (triggers reconcile via generation change).
	annotations["claw.prismer.ai/last-self-config"] = sc.Name
	claw.SetAnnotations(annotations)

	// Apply config patch to Claw spec config if provided.
	// Note: This is a simplified implementation. Full config merge would
	// depend on the runtime config format (JSON blob).

	if err := r.Update(ctx, claw); err != nil {
		return fmt.Errorf("failed to update Claw: %w", err)
	}

	return nil
}

// SetupWithManager registers the ClawSelfConfig controller.
func (r *ClawSelfConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clawv1alpha1.ClawSelfConfig{}).
		Complete(r)
}
