package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

// clawLabels returns the standard set of labels applied to all resources owned by a Claw.
func clawLabels(claw *clawv1alpha1.Claw) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": claw.Name,
		"claw.prismer.ai/runtime":    string(claw.Spec.Runtime),
		"claw.prismer.ai/instance":   claw.Name,
	}
}

// ensureService creates or updates the headless Service for the given Claw.
func (r *ClawReconciler) ensureService(ctx context.Context, claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) error {
	logger := log.FromContext(ctx)

	desired := r.buildService(claw, adapter)
	if err := controllerutil.SetControllerReference(claw, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on Service: %w", err)
	}

	var existing corev1.Service
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating Service", "name", desired.Name, "namespace", desired.Namespace)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create Service: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get Service: %w", err)
	}

	// Update the existing Service with desired ports and labels.
	existing.Spec.Ports = desired.Spec.Ports
	existing.Labels = desired.Labels
	if err := r.Update(ctx, &existing); err != nil {
		return fmt.Errorf("failed to update Service: %w", err)
	}

	return nil
}

// buildService constructs the desired headless Service for the given Claw and adapter.
func (r *ClawReconciler) buildService(claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) *corev1.Service {
	labels := clawLabels(claw)

	// Extract ports from the adapter's PodTemplate.
	podTemplate := adapter.PodTemplate(claw)
	var servicePorts []corev1.ServicePort
	for i := range podTemplate.Spec.Containers {
		for _, cp := range podTemplate.Spec.Containers[i].Ports {
			servicePorts = append(servicePorts, corev1.ServicePort{
				Name:       cp.Name,
				Port:       cp.ContainerPort,
				TargetPort: intstr.FromInt32(cp.ContainerPort),
				Protocol:   cp.Protocol,
			})
		}
	}

	selector := map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": claw.Name,
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claw.Name,
			Namespace: claw.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  selector,
			Ports:     servicePorts,
		},
	}
}
