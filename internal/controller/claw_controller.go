package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

const clawFinalizer = "claw.prismer.ai/cleanup"

// ClawReconciler reconciles a Claw object.
type ClawReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	Registry              *clawruntime.Registry
	Recorder              record.EventRecorder
	NativeSidecarsEnabled bool
}

// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods;services;persistentvolumeclaims;secrets;events;configmaps;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies;ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
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

	// Ensure headless Service exists and is up to date.
	if err := r.ensureService(ctx, &claw, adapter); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure Service: %w", err)
	}

	// Ensure ConfigMap exists.
	if err := r.ensureConfigMap(ctx, &claw, adapter); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure ConfigMap: %w", err)
	}

	// Ensure per-instance ServiceAccount exists before StatefulSet references it.
	if err := r.ensureServiceAccount(ctx, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure ServiceAccount: %w", err)
	}

	// Ensure per-instance Role + RoleBinding (only when selfConfigure is enabled).
	if err := r.ensureRole(ctx, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure Role: %w", err)
	}
	if err := r.ensureRoleBinding(ctx, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure RoleBinding: %w", err)
	}

	// Ensure per-instance NetworkPolicy (default-deny + selective allow).
	gatewayPort := adapter.DefaultConfig().GatewayPort
	if err := r.ensureNetworkPolicy(ctx, &claw, gatewayPort); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure NetworkPolicy: %w", err)
	}

	// Ensure Ingress for external HTTP access.
	if err := r.ensureIngress(ctx, &claw, gatewayPort); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure Ingress: %w", err)
	}

	// Ensure PodDisruptionBudget (default: enabled with minAvailable=1).
	if err := r.ensurePDB(ctx, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure PDB: %w", err)
	}

	// Ensure StatefulSet exists and is up to date.
	if err := r.ensureStatefulSet(ctx, &claw, adapter); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure StatefulSet: %w", err)
	}

	// Ensure PVCs created by StatefulSet have ownerReferences to the Claw CR.
	if err := r.ensurePVCOwnerReferences(ctx, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure PVC ownerReferences: %w", err)
	}

	// Re-fetch the claw to get latest version after StatefulSet changes.
	if err := r.Get(ctx, req.NamespacedName, &claw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.updateStatus(ctx, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}

	// Reconcile VolumeSnapshots (cron-based scheduling).
	snapshotResult, err := r.reconcileSnapshots(ctx, &claw)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile snapshots: %w", err)
	}

	return snapshotResult, nil
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

	switch reclaimPolicy {
	case "Delete":
		if err := r.deleteClawPVCs(ctx, claw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to delete PVCs: %w", err)
		}
	case "Retain":
		logger.Info("retaining PVCs per reclaim policy, removing ownerReferences", "name", claw.Name)
		if err := r.removePVCOwnerReferences(ctx, claw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to remove PVC ownerReferences: %w", err)
		}
	case "Archive":
		logger.Info("Archive reclaim requested, retaining PVCs until archiver completes", "name", claw.Name)
		// Set ArchivePending condition.
		apimeta.SetStatusCondition(&claw.Status.Conditions, metav1.Condition{
			Type:               "ArchivePending",
			Status:             metav1.ConditionTrue,
			Reason:             "ArchiveRequested",
			Message:            "PVCs retained pending archival; archiver sidecar will handle upload",
			ObservedGeneration: claw.Generation,
			LastTransitionTime: metav1.Now(),
		})
		if err := r.Status().Update(ctx, claw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set ArchivePending condition: %w", err)
		}
		// Emit event.
		if r.Recorder != nil {
			r.Recorder.Event(claw, corev1.EventTypeNormal, "ArchiveRequested",
				"Archive reclaim policy active; PVCs retained pending archival")
		}
		// Remove ownerReferences so PVCs survive deletion (same as Retain).
		if err := r.removePVCOwnerReferences(ctx, claw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to remove PVC ownerReferences for archive: %w", err)
		}
	}

	// Remove the finalizer to allow Kubernetes to delete the resource.
	patch := client.MergeFrom(claw.DeepCopy())
	controllerutil.RemoveFinalizer(claw, clawFinalizer)
	if err := r.Patch(ctx, claw, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
	}

	logger.Info("finalizer removed, deletion proceeding", "name", claw.Name, "namespace", claw.Namespace)
	return ctrl.Result{}, nil
}

// deleteClawPVCs deletes all PVCs labeled with the Claw instance name.
func (r *ClawReconciler) deleteClawPVCs(ctx context.Context, claw *clawv1alpha1.Claw) error {
	logger := log.FromContext(ctx)

	var pvcList corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcList,
		client.InNamespace(claw.Namespace),
		client.MatchingLabels{"claw.prismer.ai/instance": claw.Name},
	); err != nil {
		return fmt.Errorf("failed to list PVCs: %w", err)
	}

	for i := range pvcList.Items {
		logger.Info("deleting PVC", "name", pvcList.Items[i].Name, "namespace", pvcList.Items[i].Namespace)
		if err := r.Delete(ctx, &pvcList.Items[i]); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("failed to delete PVC %s: %w", pvcList.Items[i].Name, err)
			}
		}
	}

	return nil
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

	desired, err := r.buildStatefulSet(ctx, claw, adapter)
	if err != nil {
		return fmt.Errorf("failed to build StatefulSet: %w", err)
	}
	if err := controllerutil.SetControllerReference(claw, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on StatefulSet: %w", err)
	}

	var existing appsv1.StatefulSet
	err = r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
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
func (r *ClawReconciler) buildStatefulSet(ctx context.Context, claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) (*appsv1.StatefulSet, error) {
	logger := log.FromContext(ctx)

	labels := clawLabels(claw)

	replicas := int32(1)

	podTemplate := adapter.PodTemplate(claw)

	// Apply labels to the pod template.
	if podTemplate.Labels == nil {
		podTemplate.Labels = make(map[string]string)
	}
	for k, v := range labels {
		podTemplate.Labels[k] = v
	}

	// Inject channel sidecars.
	if len(claw.Spec.Channels) > 0 {
		skipped, err := r.injectChannelSidecars(ctx, claw, podTemplate)
		if err != nil {
			return nil, fmt.Errorf("failed to inject channel sidecars: %w", err)
		}
		if len(skipped) > 0 {
			logger.Info("skipped channels during sidecar injection", "skipped", skipped)
		}
	}

	// Inject output archiver sidecar if configured.
	injectArchiverIfNeeded(claw, podTemplate)

	// Inject credentials into the pod template.
	if claw.Spec.Credentials != nil {
		if err := r.injectCredentials(ctx, claw, podTemplate); err != nil {
			return nil, fmt.Errorf("failed to inject credentials: %w", err)
		}
	}

	// Apply pod security context.
	podTemplate.Spec.SecurityContext = &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true),
		FSGroup:      ptr.To(int64(1000)),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}

	// Set the per-instance ServiceAccount on the pod template.
	// Only force automountServiceAccountToken=false for operator-managed SAs;
	// user-managed SAs may need the token mounted (e.g., for in-cluster API access).
	podTemplate.Spec.ServiceAccountName = serviceAccountName(claw)
	if !isUserManagedSA(claw) {
		podTemplate.Spec.AutomountServiceAccountToken = ptr.To(false)
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
	}, nil
}

func (r *ClawReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clawv1alpha1.Claw{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Owns(&networkingv1.Ingress{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Watches(&clawv1alpha1.ClawChannel{}, handler.EnqueueRequestsFromMapFunc(r.findClawsForChannel)).
		Complete(r)
}

// findClawsForChannel maps a ClawChannel change to all Claw resources that reference it.
// Uses the field indexer registered by SetupChannelNameIndex for efficient lookups.
func (r *ClawReconciler) findClawsForChannel(ctx context.Context, obj client.Object) []reconcile.Request {
	channel, ok := obj.(*clawv1alpha1.ClawChannel)
	if !ok {
		return nil
	}

	names, err := clawsReferencingChannel(ctx, r.Client, channel.Namespace, channel.Name)
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to find Claws for channel mapping", "channel", channel.Name)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(names))
	for _, name := range names {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      name,
				Namespace: channel.Namespace,
			},
		})
	}

	return requests
}
