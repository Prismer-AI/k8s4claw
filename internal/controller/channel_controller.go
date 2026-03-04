package controller

import (
	"context"
	"fmt"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const clawChannelFinalizer = "claw.prismer.ai/channel-cleanup"

// ClawChannelReconciler reconciles a ClawChannel object.
type ClawChannelReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=claw.prismer.ai,resources=clawchannels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=clawchannels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=clawchannels/finalizers,verbs=update
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws,verbs=get;list;watch

// Reconcile handles a single reconciliation loop for a ClawChannel resource.
func (r *ClawChannelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var channel clawv1alpha1.ClawChannel
	if err := r.Get(ctx, req.NamespacedName, &channel); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Find all Claws referencing this channel.
	referencingClaws, err := r.findReferencingClaws(ctx, &channel)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to find referencing Claws: %w", err)
	}

	logger.Info("reconciling ClawChannel",
		"name", channel.Name,
		"namespace", channel.Namespace,
		"referencingClaws", len(referencingClaws),
	)

	// Handle deletion if DeletionTimestamp is set.
	if !channel.DeletionTimestamp.IsZero() {
		return r.handleChannelDeletion(ctx, &channel, referencingClaws)
	}

	// Ensure finalizer is present for non-deleted resources.
	if !controllerutil.ContainsFinalizer(&channel, clawChannelFinalizer) {
		logger.Info("adding finalizer", "name", channel.Name, "namespace", channel.Namespace)
		patch := client.MergeFrom(channel.DeepCopy())
		controllerutil.AddFinalizer(&channel, clawChannelFinalizer)
		if err := r.Patch(ctx, &channel, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
	}

	// Update status with reference information.
	if err := r.updateChannelStatus(ctx, &channel, referencingClaws); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update channel status: %w", err)
	}

	return ctrl.Result{}, nil
}

// findReferencingClaws returns the sorted names of Claws referencing the given channel.
// Uses the field indexer registered by SetupChannelNameIndex for efficient lookups.
func (r *ClawChannelReconciler) findReferencingClaws(ctx context.Context, channel *clawv1alpha1.ClawChannel) ([]string, error) {
	return clawsReferencingChannel(ctx, r.Client, channel.Namespace, channel.Name)
}

// handleChannelDeletion manages the deletion of a ClawChannel. If the channel is still
// referenced by Claws, it sets an InUse condition and requeues. If no references remain,
// it removes the finalizer to allow deletion.
func (r *ClawChannelReconciler) handleChannelDeletion(ctx context.Context, channel *clawv1alpha1.ClawChannel, referencingClaws []string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(channel, clawChannelFinalizer) {
		return ctrl.Result{}, nil
	}

	if len(referencingClaws) > 0 {
		logger.Info("channel still in use, blocking deletion",
			"name", channel.Name,
			"namespace", channel.Namespace,
			"referencingClaws", referencingClaws,
		)

		// Set InUse condition to True.
		apimeta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "InUse",
			Status:             metav1.ConditionTrue,
			ObservedGeneration: channel.Generation,
			Reason:             "ReferencesExist",
			Message:            fmt.Sprintf("channel is referenced by %d Claw(s): %v", len(referencingClaws), referencingClaws),
		})
		channel.Status.ReferenceCount = len(referencingClaws)
		channel.Status.ReferencingClaws = referencingClaws

		if err := r.Status().Update(ctx, channel); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update channel status during deletion: %w", err)
		}

		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// No references remain — remove the finalizer.
	logger.Info("no references remain, removing finalizer",
		"name", channel.Name,
		"namespace", channel.Namespace,
	)

	patch := client.MergeFrom(channel.DeepCopy())
	controllerutil.RemoveFinalizer(channel, clawChannelFinalizer)
	if err := r.Patch(ctx, channel, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
	}

	logger.Info("finalizer removed, deletion proceeding",
		"name", channel.Name,
		"namespace", channel.Namespace,
	)
	return ctrl.Result{}, nil
}

// updateChannelStatus sets the ReferenceCount, ReferencingClaws, ObservedGeneration,
// and InUse condition on the channel's status subresource.
func (r *ClawChannelReconciler) updateChannelStatus(ctx context.Context, channel *clawv1alpha1.ClawChannel, referencingClaws []string) error {
	channel.Status.ReferenceCount = len(referencingClaws)
	channel.Status.ReferencingClaws = referencingClaws
	channel.Status.ObservedGeneration = channel.Generation

	if len(referencingClaws) > 0 {
		apimeta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "InUse",
			Status:             metav1.ConditionTrue,
			ObservedGeneration: channel.Generation,
			Reason:             "ReferencesExist",
			Message:            fmt.Sprintf("channel is referenced by %d Claw(s): %v", len(referencingClaws), referencingClaws),
		})
	} else {
		apimeta.SetStatusCondition(&channel.Status.Conditions, metav1.Condition{
			Type:               "InUse",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: channel.Generation,
			Reason:             "NoReferences",
			Message:            "channel is not referenced by any Claw",
		})
	}

	return r.Status().Update(ctx, channel)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClawChannelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clawv1alpha1.ClawChannel{}).
		Complete(r)
}
