package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

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

func (r *ClawReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var claw clawv1alpha1.Claw
	if err := r.Get(ctx, req.NamespacedName, &claw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Resolve runtime adapter
	adapter, ok := r.Registry.Get(claw.Spec.Runtime)
	if !ok {
		logger.Error(fmt.Errorf("unknown runtime: %s", claw.Spec.Runtime), "unsupported runtime type")
		return ctrl.Result{}, nil
	}

	// TODO: implement reconciliation phases:
	// 1. Handle finalizers (for reclaim policy)
	// 2. Ensure PVCs exist
	// 3. Resolve ClawChannel references → build sidecar specs
	// 4. Build Pod from RuntimeAdapter.PodTemplate()
	// 5. Create/update Pod
	// 6. Update status conditions
	_ = adapter

	return ctrl.Result{}, nil
}

func (r *ClawReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clawv1alpha1.Claw{}).
		Complete(r)
}
