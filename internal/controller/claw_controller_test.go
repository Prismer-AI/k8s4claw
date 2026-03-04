package controller

import (
	"fmt"
	"testing"
	"time"

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
