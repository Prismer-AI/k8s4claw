package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

const clawFinalizer = "claw.prismer.ai/cleanup"

// ClawReconciler reconciles a Claw object.
type ClawReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Registry *clawruntime.Registry
}

// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods;services;persistentvolumeclaims;secrets;events;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete

func (r *ClawReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var claw clawv1alpha1.Claw
	if err := r.Get(ctx, req.NamespacedName, &claw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Resolve runtime adapter.
	adapter, ok := r.Registry.Get(claw.Spec.Runtime)
	if !ok {
		logger.Error(fmt.Errorf("unknown runtime: %s", claw.Spec.Runtime), "unsupported runtime type")
		return ctrl.Result{}, nil
	}

	// Handle deletion: if DeletionTimestamp is set, run cleanup and remove finalizer.
	if !claw.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &claw)
	}

	// Ensure finalizer is present for non-deleted resources.
	if err := r.ensureFinalizer(ctx, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure finalizer: %w", err)
	}

	// TODO: implement reconciliation phases:
	// 2. Ensure PVCs exist
	// 3. Resolve ClawChannel references -> build sidecar specs
	// 4. Build Pod from RuntimeAdapter.PodTemplate()
	// 5. Create/update Pod
	// 6. Update status conditions
	_ = adapter

	return ctrl.Result{}, nil
}

// handleDeletion runs cleanup logic and removes the finalizer so the object can be garbage-collected.
func (r *ClawReconciler) handleDeletion(ctx context.Context, claw *clawv1alpha1.Claw) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(claw, clawFinalizer) {
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx)
	logger.Info("handling deletion for Claw", "name", claw.Name, "namespace", claw.Namespace)

	// TODO: implement reclaim policy logic:
	// - Delete: remove PVCs
	// - Archive: snapshot PVCs, then remove
	// - Retain: leave PVCs in place

	// Remove the finalizer to allow Kubernetes to delete the resource.
	controllerutil.RemoveFinalizer(claw, clawFinalizer)
	if err := r.Update(ctx, claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
	}

	logger.Info("finalizer removed, deletion proceeding", "name", claw.Name, "namespace", claw.Namespace)
	return ctrl.Result{}, nil
}

// ensureFinalizer adds the cleanup finalizer if it is not already present.
func (r *ClawReconciler) ensureFinalizer(ctx context.Context, claw *clawv1alpha1.Claw) error {
	if controllerutil.ContainsFinalizer(claw, clawFinalizer) {
		return nil
	}

	logger := log.FromContext(ctx)
	logger.Info("adding finalizer", "name", claw.Name, "namespace", claw.Namespace)

	controllerutil.AddFinalizer(claw, clawFinalizer)
	if err := r.Update(ctx, claw); err != nil {
		return fmt.Errorf("failed to add finalizer: %w", err)
	}

	return nil
}

func (r *ClawReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clawv1alpha1.Claw{}).
		Owns(&appsv1.StatefulSet{}).
		Complete(r)
}
