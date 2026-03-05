package controller

import (
	"context"
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// isUserManagedSA returns true when the user has specified their own ServiceAccount name.
func isUserManagedSA(claw *clawv1alpha1.Claw) bool {
	return claw.Spec.ServiceAccount != nil && claw.Spec.ServiceAccount.Name != ""
}

// serviceAccountName returns the ServiceAccount name for the given Claw.
// If spec.serviceAccount.name is set, the user-provided name is returned;
// otherwise the SA is operator-managed and named after the Claw instance.
func serviceAccountName(claw *clawv1alpha1.Claw) string {
	if isUserManagedSA(claw) {
		return claw.Spec.ServiceAccount.Name
	}
	return claw.Name
}

// ensureServiceAccount creates or updates the operator-managed ServiceAccount
// for the given Claw. If spec.serviceAccount.name is set the function is a
// no-op: the user is responsible for that SA.
func (r *ClawReconciler) ensureServiceAccount(ctx context.Context, claw *clawv1alpha1.Claw) error {
	if isUserManagedSA(claw) {
		return nil
	}

	logger := log.FromContext(ctx)

	desired := buildServiceAccount(claw)
	if err := controllerutil.SetControllerReference(claw, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on ServiceAccount: %w", err)
	}

	var existing corev1.ServiceAccount
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating ServiceAccount", "name", desired.Name, "namespace", desired.Namespace)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create ServiceAccount: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get ServiceAccount: %w", err)
	}

	// Only update ServiceAccounts that we own to avoid clobbering unrelated SAs
	// (e.g., the namespace's built-in "default" SA if a Claw is named "default").
	if !metav1.IsControlledBy(&existing, claw) {
		return fmt.Errorf("ServiceAccount %s/%s already exists and is not owned by this Claw", existing.Namespace, existing.Name)
	}

	existing.Labels = desired.Labels
	existing.Annotations = desired.Annotations
	existing.AutomountServiceAccountToken = desired.AutomountServiceAccountToken
	if err := r.Update(ctx, &existing); err != nil {
		return fmt.Errorf("failed to update ServiceAccount: %w", err)
	}

	return nil
}

// buildServiceAccount constructs the desired operator-managed ServiceAccount.
func buildServiceAccount(claw *clawv1alpha1.Claw) *corev1.ServiceAccount {
	labels := clawLabels(claw)

	var annotations map[string]string
	if claw.Spec.ServiceAccount != nil && len(claw.Spec.ServiceAccount.Annotations) > 0 {
		annotations = make(map[string]string, len(claw.Spec.ServiceAccount.Annotations))
		maps.Copy(annotations, claw.Spec.ServiceAccount.Annotations)
	}

	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        claw.Name,
			Namespace:   claw.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		AutomountServiceAccountToken: ptr.To(false),
	}
}
