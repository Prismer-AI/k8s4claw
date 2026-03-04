package controller

import (
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const (
	testTimeout  = 10 * time.Second
	testInterval = 250 * time.Millisecond
)

// waitForCondition polls until condFn returns true or the timeout is reached.
func waitForCondition(t *testing.T, timeout, interval time.Duration, condFn func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok, err := condFn()
		if err != nil {
			t.Fatalf("condition check returned error: %v", err)
		}
		if ok {
			return
		}
		time.Sleep(interval)
	}
	t.Fatal("timed out waiting for condition")
}

// createNamespace creates a namespace with the given name and waits for it to exist.
func createNamespace(t *testing.T, name string) {
	t.Helper()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("failed to create namespace %s: %v", name, err)
	}
}

func TestClawReconciler_FinalizerAdded(t *testing.T) {
	ns := fmt.Sprintf("test-finalizer-add-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claw",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the finalizer to be added by the reconciler.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      claw.Name,
			Namespace: claw.Namespace,
		}, &fetched); err != nil {
			// Cache may not have synced yet; treat NotFound as transient.
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return controllerutil.ContainsFinalizer(&fetched, clawFinalizer), nil
	})
}

func TestClawReconciler_FinalizerRunsOnDelete(t *testing.T) {
	ns := fmt.Sprintf("test-finalizer-del-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claw-delete",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the finalizer to be added first.
	// Use a fresh variable to track the latest version for the delete call.
	nn := types.NamespacedName{Name: claw.Name, Namespace: claw.Namespace}
	var latest clawv1alpha1.Claw
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		if err := k8sClient.Get(ctx, nn, &latest); err != nil {
			// Cache may not have synced yet; treat NotFound as transient.
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return controllerutil.ContainsFinalizer(&latest, clawFinalizer), nil
	})

	// Delete the Claw using the latest fetched version.
	if err := k8sClient.Delete(ctx, &latest); err != nil {
		t.Fatalf("failed to delete Claw: %v", err)
	}

	// Wait for the Claw to be fully deleted (finalizer removed by reconciler).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.Claw
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      claw.Name,
			Namespace: claw.Namespace,
		}, &fetched)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				// Object is gone — finalizer was removed and deletion completed.
				return true, nil
			}
			return false, err
		}
		return false, nil
	})
}

func TestClawReconciler_StatefulSetCreated(t *testing.T) {
	ns := fmt.Sprintf("test-sts-create-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-sts"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the StatefulSet to appear.
	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      clawName,
			Namespace: ns,
		}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Verify spec.replicas = 1.
	if sts.Spec.Replicas == nil || *sts.Spec.Replicas != 1 {
		t.Fatalf("expected replicas=1, got %v", sts.Spec.Replicas)
	}

	// Verify spec.serviceName = claw name.
	if sts.Spec.ServiceName != clawName {
		t.Fatalf("expected serviceName=%q, got %q", clawName, sts.Spec.ServiceName)
	}

	// Verify labels on the StatefulSet.
	expectedLabels := map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": clawName,
		"claw.prismer.ai/runtime":    "openclaw",
	}
	for k, v := range expectedLabels {
		if got := sts.Labels[k]; got != v {
			t.Errorf("StatefulSet label %q: expected %q, got %q", k, v, got)
		}
	}

	// Verify labels on the pod template.
	for k, v := range expectedLabels {
		if got := sts.Spec.Template.Labels[k]; got != v {
			t.Errorf("pod template label %q: expected %q, got %q", k, v, got)
		}
	}

	// Verify ownerReferences.
	if len(sts.OwnerReferences) != 1 {
		t.Fatalf("expected 1 ownerReference, got %d", len(sts.OwnerReferences))
	}
	if sts.OwnerReferences[0].Kind != "Claw" {
		t.Errorf("expected ownerReference kind=Claw, got %q", sts.OwnerReferences[0].Kind)
	}

	// Verify pod security context.
	podSec := sts.Spec.Template.Spec.SecurityContext
	if podSec == nil {
		t.Fatal("expected pod security context, got nil")
	}
	if podSec.RunAsNonRoot == nil || !*podSec.RunAsNonRoot {
		t.Error("expected runAsNonRoot=true")
	}
	if podSec.FSGroup == nil || *podSec.FSGroup != 1000 {
		t.Errorf("expected fsGroup=1000, got %v", podSec.FSGroup)
	}
	if podSec.SeccompProfile == nil || podSec.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("expected seccomp profile RuntimeDefault")
	}

	// Verify terminationGracePeriodSeconds = 30 + 10 = 40 for OpenClaw.
	if sts.Spec.Template.Spec.TerminationGracePeriodSeconds == nil || *sts.Spec.Template.Spec.TerminationGracePeriodSeconds != 40 {
		t.Errorf("expected terminationGracePeriodSeconds=40, got %v", sts.Spec.Template.Spec.TerminationGracePeriodSeconds)
	}

	// Verify container named "runtime" exists.
	var runtimeContainer *corev1.Container
	for i := range sts.Spec.Template.Spec.Containers {
		if sts.Spec.Template.Spec.Containers[i].Name == "runtime" {
			runtimeContainer = &sts.Spec.Template.Spec.Containers[i]
			break
		}
	}
	if runtimeContainer == nil {
		t.Fatal("expected container named 'runtime', not found")
	}

	// Verify image.
	if runtimeContainer.Image != "busybox:latest" {
		t.Errorf("expected image=busybox:latest, got %q", runtimeContainer.Image)
	}

	// Verify probes are present.
	if runtimeContainer.LivenessProbe == nil {
		t.Error("expected liveness probe, got nil")
	}
	if runtimeContainer.ReadinessProbe == nil {
		t.Error("expected readiness probe, got nil")
	}

	// Verify container security context.
	cSec := runtimeContainer.SecurityContext
	if cSec == nil {
		t.Fatal("expected container security context, got nil")
	}
	if cSec.ReadOnlyRootFilesystem == nil || !*cSec.ReadOnlyRootFilesystem {
		t.Error("expected readOnlyRootFilesystem=true")
	}
	if cSec.AllowPrivilegeEscalation == nil || *cSec.AllowPrivilegeEscalation {
		t.Error("expected allowPrivilegeEscalation=false")
	}
	if cSec.Capabilities == nil || len(cSec.Capabilities.Drop) == 0 {
		t.Error("expected capabilities.drop=[ALL]")
	} else {
		found := false
		for _, cap := range cSec.Capabilities.Drop {
			if cap == "ALL" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected capabilities.drop to contain ALL")
		}
	}
}

func TestClawReconciler_StatusProvisioning(t *testing.T) {
	ns := fmt.Sprintf("test-status-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-status"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the StatefulSet to be created (Task 5 already works).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var sts appsv1.StatefulSet
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      clawName,
			Namespace: ns,
		}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Wait for status.phase to become Provisioning.
	// In envtest, readyReplicas stays 0 so we expect Provisioning (not Running).
	var fetched clawv1alpha1.Claw
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      clawName,
			Namespace: ns,
		}, &fetched); err != nil {
			return false, err
		}
		return fetched.Status.Phase == clawv1alpha1.ClawPhaseProvisioning, nil
	})

	// Verify observedGeneration matches the Claw's generation.
	if fetched.Status.ObservedGeneration != fetched.Generation {
		t.Errorf("expected observedGeneration=%d, got %d", fetched.Generation, fetched.Status.ObservedGeneration)
	}

	// Verify RuntimeReady condition exists with status False.
	var runtimeReadyCond *metav1.Condition
	for i := range fetched.Status.Conditions {
		if fetched.Status.Conditions[i].Type == "RuntimeReady" {
			runtimeReadyCond = &fetched.Status.Conditions[i]
			break
		}
	}
	if runtimeReadyCond == nil {
		t.Fatal("expected RuntimeReady condition, not found")
	}
	if runtimeReadyCond.Status != metav1.ConditionFalse {
		t.Errorf("expected RuntimeReady status=%s, got %s", metav1.ConditionFalse, runtimeReadyCond.Status)
	}
	if runtimeReadyCond.Reason != "StatefulSetNotReady" {
		t.Errorf("expected RuntimeReady reason=StatefulSetNotReady, got %q", runtimeReadyCond.Reason)
	}
}

func TestClawReconciler_UnknownRuntime(t *testing.T) {
	ns := fmt.Sprintf("test-sts-unknown-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Use "custom" which passes CRD validation but has no adapter registered.
	clawName := "test-claw-unknown"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeCustom,
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait briefly to give the reconciler time to process.
	time.Sleep(2 * time.Second)

	// Verify NO StatefulSet is created.
	var sts appsv1.StatefulSet
	err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      clawName,
		Namespace: ns,
	}, &sts)
	if err == nil {
		t.Fatal("expected no StatefulSet to be created for unknown runtime, but one was found")
	}
	if client.IgnoreNotFound(err) != nil {
		t.Fatalf("unexpected error checking for StatefulSet: %v", err)
	}
}
