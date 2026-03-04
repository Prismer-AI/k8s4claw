package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
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
	Scheme                *runtime.Scheme
	Registry              *clawruntime.Registry
	NativeSidecarsEnabled bool
}

// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods;services;persistentvolumeclaims;secrets;events;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete

func (r *ClawReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var claw clawv1alpha1.Claw
	if err := r.Get(ctx, req.NamespacedName, &claw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Resolve runtime adapter.
	adapter, ok := r.Registry.Get(claw.Spec.Runtime)
	if !ok {
		return ctrl.Result{}, fmt.Errorf("unsupported runtime type: %s", claw.Spec.Runtime)
	}

	// Handle deletion: if DeletionTimestamp is set, run cleanup and remove finalizer.
	if !claw.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &claw)
	}

	// Ensure finalizer is present for non-deleted resources.
	if err := r.ensureFinalizer(ctx, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure finalizer: %w", err)
	}

	// Ensure StatefulSet exists and is up to date.
	if err := r.ensureStatefulSet(ctx, &claw, adapter); err != nil {
		return ctrl.Result{}, err
	}

	// Re-fetch the claw to get latest version after StatefulSet changes.
	if err := r.Get(ctx, req.NamespacedName, &claw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.updateStatus(ctx, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}

	// TODO: implement remaining reconciliation phases:
	// 2. Ensure PVCs exist
	// 3. Resolve ClawChannel references -> build sidecar specs

	return ctrl.Result{}, nil
}

// handleDeletion runs cleanup logic and removes the finalizer so the object can be garbage-collected.
func (r *ClawReconciler) handleDeletion(ctx context.Context, claw *clawv1alpha1.Claw) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(claw, clawFinalizer) {
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx)

	reclaimPolicy := "Retain"
	if claw.Spec.Persistence != nil && claw.Spec.Persistence.ReclaimPolicy != "" {
		reclaimPolicy = string(claw.Spec.Persistence.ReclaimPolicy)
	}
	logger.Info("handling deletion for Claw", "name", claw.Name, "namespace", claw.Namespace, "reclaimPolicy", reclaimPolicy)

	// TODO: implement reclaim policy logic:
	// - Delete: remove PVCs
	// - Archive: snapshot PVCs, then remove
	// - Retain: leave PVCs in place

	// Remove the finalizer to allow Kubernetes to delete the resource.
	patch := client.MergeFrom(claw.DeepCopy())
	controllerutil.RemoveFinalizer(claw, clawFinalizer)
	if err := r.Patch(ctx, claw, patch); err != nil {
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

	patch := client.MergeFrom(claw.DeepCopy())
	controllerutil.AddFinalizer(claw, clawFinalizer)
	if err := r.Patch(ctx, claw, patch); err != nil {
		return fmt.Errorf("failed to add finalizer: %w", err)
	}

	return nil
}

// ensureStatefulSet creates or updates the StatefulSet for the given Claw.
func (r *ClawReconciler) ensureStatefulSet(ctx context.Context, claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) error {
	logger := log.FromContext(ctx)

	desired := r.buildStatefulSet(claw, adapter)
	if err := controllerutil.SetControllerReference(claw, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on StatefulSet: %w", err)
	}

	var existing appsv1.StatefulSet
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating StatefulSet", "name", desired.Name, "namespace", desired.Namespace)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create StatefulSet: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get StatefulSet: %w", err)
	}

	// Update the existing StatefulSet with desired spec.
	existing.Spec.Template = desired.Spec.Template
	existing.Spec.Replicas = desired.Spec.Replicas
	if err := r.Update(ctx, &existing); err != nil {
		return fmt.Errorf("failed to update StatefulSet: %w", err)
	}

	return nil
}

// updateStatus determines the phase from StatefulSet state and updates the Claw status subresource.
func (r *ClawReconciler) updateStatus(ctx context.Context, claw *clawv1alpha1.Claw) error {
	var sts appsv1.StatefulSet
	stsKey := client.ObjectKey{Name: claw.Name, Namespace: claw.Namespace}
	err := r.Get(ctx, stsKey, &sts)

	var phase clawv1alpha1.ClawPhase
	var runtimeReady bool

	switch {
	case apierrors.IsNotFound(err):
		phase = clawv1alpha1.ClawPhasePending
	case err != nil:
		return fmt.Errorf("failed to get StatefulSet for status: %w", err)
	case sts.Status.ReadyReplicas >= 1:
		phase = clawv1alpha1.ClawPhaseRunning
		runtimeReady = true
	default:
		phase = clawv1alpha1.ClawPhaseProvisioning
	}

	claw.Status.Phase = phase
	claw.Status.ObservedGeneration = claw.Generation

	// Set RuntimeReady condition.
	condition := metav1.Condition{
		Type:               "RuntimeReady",
		ObservedGeneration: claw.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if runtimeReady {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "StatefulSetReady"
		condition.Message = "StatefulSet has ready replicas"
	} else {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "StatefulSetNotReady"
		condition.Message = "Waiting for StatefulSet to become ready"
	}
	apimeta.SetStatusCondition(&claw.Status.Conditions, condition)

	return r.Status().Update(ctx, claw)
}

// buildStatefulSet constructs the desired StatefulSet for the given Claw and adapter.
func (r *ClawReconciler) buildStatefulSet(claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) *appsv1.StatefulSet {
	labels := map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": claw.Name,
		"claw.prismer.ai/runtime":    string(claw.Spec.Runtime),
	}

	replicas := int32(1)

	podTemplate := adapter.PodTemplate(claw)

	// Apply labels to the pod template.
	if podTemplate.Labels == nil {
		podTemplate.Labels = make(map[string]string)
	}
	for k, v := range labels {
		podTemplate.Labels[k] = v
	}

	// Apply pod security context.
	podTemplate.Spec.SecurityContext = &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true),
		FSGroup:      ptr.To(int64(1000)),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}

	// Set terminationGracePeriodSeconds.
	gracePeriod := int64(adapter.GracefulShutdownSeconds()) + 10
	podTemplate.Spec.TerminationGracePeriodSeconds = &gracePeriod

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claw.Name,
			Namespace: claw.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:             &replicas,
			ServiceName:          claw.Name,
			Selector:             &metav1.LabelSelector{MatchLabels: labels},
			Template:             *podTemplate,
			VolumeClaimTemplates: clawruntime.BuildVolumeClaimTemplates(claw),
		},
	}
}

func (r *ClawReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clawv1alpha1.Claw{}).
		Owns(&appsv1.StatefulSet{}).
		Complete(r)
}
