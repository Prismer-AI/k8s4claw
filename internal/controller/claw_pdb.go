package controller

import (
	"context"
	"fmt"

	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ensurePDB creates, updates, or deletes the PodDisruptionBudget for the given Claw.
// By default a PDB is created unless explicitly disabled via spec.availability.pdb.enabled=false.
func (r *ClawReconciler) ensurePDB(ctx context.Context, claw *clawv1alpha1.Claw) error {
	pdbName := claw.Name

	if !pdbEnabled(claw) {
		return r.deleteOwnedPDB(ctx, claw, pdbName)
	}

	logger := log.FromContext(ctx)

	desired := buildPDB(claw)
	if err := controllerutil.SetControllerReference(claw, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on PDB: %w", err)
	}

	var existing policyv1.PodDisruptionBudget
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating PDB", "name", desired.Name, "namespace", desired.Namespace)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create PDB: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get PDB: %w", err)
	}

	if !metav1.IsControlledBy(&existing, claw) {
		return fmt.Errorf("PDB %s/%s already exists and is not owned by this Claw", existing.Namespace, existing.Name)
	}

	existing.Labels = desired.Labels
	existing.Spec = desired.Spec
	if err := r.Update(ctx, &existing); err != nil {
		return fmt.Errorf("failed to update PDB: %w", err)
	}

	return nil
}

// pdbEnabled returns true when a PDB should be created.
// Default behavior: create PDB unless explicitly disabled.
func pdbEnabled(claw *clawv1alpha1.Claw) bool {
	if claw.Spec.Availability == nil || claw.Spec.Availability.PDB == nil {
		return true // default: create PDB
	}
	return claw.Spec.Availability.PDB.Enabled
}

// buildPDB constructs the desired PodDisruptionBudget for a Claw instance.
func buildPDB(claw *clawv1alpha1.Claw) *policyv1.PodDisruptionBudget {
	labels := clawLabels(claw)

	minAvailable := 1
	if claw.Spec.Availability != nil && claw.Spec.Availability.PDB != nil && claw.Spec.Availability.PDB.MinAvailable > 0 {
		minAvailable = claw.Spec.Availability.PDB.MinAvailable
	}

	minAvailableVal := intstr.FromInt32(int32(minAvailable))

	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claw.Name,
			Namespace: claw.Namespace,
			Labels:    labels,
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvailableVal,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"claw.prismer.ai/instance": claw.Name,
				},
			},
		},
	}
}

// deleteOwnedPDB deletes a PDB if it exists and is owned by this Claw.
func (r *ClawReconciler) deleteOwnedPDB(ctx context.Context, claw *clawv1alpha1.Claw, name string) error {
	var existing policyv1.PodDisruptionBudget
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: claw.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get PDB for cleanup: %w", err)
	}

	if !metav1.IsControlledBy(&existing, claw) {
		return nil
	}

	logger := log.FromContext(ctx)
	logger.Info("deleting stale PDB", "name", name, "namespace", claw.Namespace)
	if err := r.Delete(ctx, &existing); err != nil {
		return fmt.Errorf("failed to delete stale PDB: %w", err)
	}
	return nil
}
