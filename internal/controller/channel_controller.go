package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ClawChannelReconciler reconciles a ClawChannel object.
type ClawChannelReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=claw.prismer.ai,resources=clawchannels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=clawchannels/status,verbs=get;update;patch

func (r *ClawChannelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var channel clawv1alpha1.ClawChannel
	if err := r.Get(ctx, req.NamespacedName, &channel); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// TODO: implement reconciliation:
	// 1. Validate channel spec
	// 2. Find all Claws referencing this channel
	// 3. Trigger re-reconciliation of those Claws
	// 4. Update status conditions

	return ctrl.Result{}, nil
}

func (r *ClawChannelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clawv1alpha1.ClawChannel{}).
		Complete(r)
}
