package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ensurePVCOwnerReferences lists PVCs by label and ensures each has an
// ownerReference pointing to the Claw CR. StatefulSet creates PVCs but does
// NOT set ownerReferences, so we add them for GC when reclaimPolicy=Delete.
func (r *ClawReconciler) ensurePVCOwnerReferences(ctx context.Context, claw *clawv1alpha1.Claw) error {
	logger := log.FromContext(ctx)

	var pvcList corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcList,
		client.InNamespace(claw.Namespace),
		client.MatchingLabels{"claw.prismer.ai/instance": claw.Name},
	); err != nil {
		return fmt.Errorf("failed to list PVCs: %w", err)
	}

	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]
		if hasOwnerReference(pvc, claw) {
			continue
		}

		logger.Info("adding ownerReference to PVC", "pvc", pvc.Name, "claw", claw.Name)
		patch := client.MergeFrom(pvc.DeepCopy())
		pvc.OwnerReferences = append(pvc.OwnerReferences, metav1.OwnerReference{
			APIVersion: clawv1alpha1.GroupVersion.String(),
			Kind:       "Claw",
			Name:       claw.Name,
			UID:        claw.UID,
			Controller: boolPtr(false), // Not controller ref; StatefulSet is the controller.
		})
		if err := r.Patch(ctx, pvc, patch); err != nil {
			return fmt.Errorf("failed to patch PVC %s ownerReference: %w", pvc.Name, err)
		}
	}

	return nil
}

// removePVCOwnerReferences strips the Claw ownerReference from all PVCs
// so they become orphaned and survive Claw deletion (Retain policy).
func (r *ClawReconciler) removePVCOwnerReferences(ctx context.Context, claw *clawv1alpha1.Claw) error {
	logger := log.FromContext(ctx)

	var pvcList corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcList,
		client.InNamespace(claw.Namespace),
		client.MatchingLabels{"claw.prismer.ai/instance": claw.Name},
	); err != nil {
		return fmt.Errorf("failed to list PVCs: %w", err)
	}

	gvk := schema.GroupVersionKind{
		Group:   clawv1alpha1.GroupVersion.Group,
		Version: clawv1alpha1.GroupVersion.Version,
		Kind:    "Claw",
	}

	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]
		if !hasOwnerReference(pvc, claw) {
			continue
		}

		logger.Info("removing ownerReference from PVC for Retain policy", "pvc", pvc.Name)
		patch := client.MergeFrom(pvc.DeepCopy())
		pvc.OwnerReferences = removeOwnerRef(pvc.OwnerReferences, claw.UID, gvk)
		if err := r.Patch(ctx, pvc, patch); err != nil {
			return fmt.Errorf("failed to remove ownerReference from PVC %s: %w", pvc.Name, err)
		}
	}

	return nil
}

// hasOwnerReference checks if the object has an ownerReference pointing to the given Claw.
func hasOwnerReference(obj metav1.Object, claw *clawv1alpha1.Claw) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == claw.UID {
			return true
		}
	}
	return false
}

// removeOwnerRef removes the ownerReference matching the given UID and GVK.
func removeOwnerRef(refs []metav1.OwnerReference, uid types.UID, gvk schema.GroupVersionKind) []metav1.OwnerReference {
	result := make([]metav1.OwnerReference, 0, len(refs))
	for _, ref := range refs {
		if ref.UID == uid {
			continue
		}
		result = append(result, ref)
	}
	_ = gvk // reserved for future filtering
	return result
}

func boolPtr(b bool) *bool {
	return &b
}
