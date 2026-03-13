package controller

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
		"claw.prismer.ai/instance":   clawName,
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

	// Verify terminationGracePeriodSeconds = 30 + 15 = 45 for OpenClaw.
	if sts.Spec.Template.Spec.TerminationGracePeriodSeconds == nil || *sts.Spec.Template.Spec.TerminationGracePeriodSeconds != 45 {
		t.Errorf("expected terminationGracePeriodSeconds=45, got %v", sts.Spec.Template.Spec.TerminationGracePeriodSeconds)
	}

	// Verify init container named "claw-init" exists.
	if len(sts.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(sts.Spec.Template.Spec.InitContainers))
	}
	if sts.Spec.Template.Spec.InitContainers[0].Name != "claw-init" {
		t.Errorf("expected init container name=claw-init, got %q", sts.Spec.Template.Spec.InitContainers[0].Name)
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

	// Verify image is the real OpenClaw image (not busybox placeholder).
	if runtimeContainer.Image != "ghcr.io/prismer-ai/k8s4claw-openclaw:latest" {
		t.Errorf("expected image=ghcr.io/prismer-ai/k8s4claw-openclaw:latest, got %q", runtimeContainer.Image)
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

	// Verify base volumes are present.
	volNames := make(map[string]bool)
	for _, v := range sts.Spec.Template.Spec.Volumes {
		volNames[v.Name] = true
	}
	for _, expected := range []string{"ipc-socket", "wal-data", "config-vol", "tmp"} {
		if !volNames[expected] {
			t.Errorf("missing expected volume %q", expected)
		}
	}

	// Verify shared env vars on runtime container.
	envMap := make(map[string]string)
	for _, e := range runtimeContainer.Env {
		envMap[e.Name] = e.Value
	}
	if envMap["CLAW_NAME"] != clawName {
		t.Errorf("expected CLAW_NAME=%q, got %q", clawName, envMap["CLAW_NAME"])
	}
	if _, ok := envMap["IPC_SOCKET_PATH"]; !ok {
		t.Error("expected IPC_SOCKET_PATH env var")
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

func TestClawReconciler_ServiceCreated(t *testing.T) {
	ns := fmt.Sprintf("test-svc-create-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-svc"
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

	// Wait for the headless Service to appear.
	var svc corev1.Service
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      clawName,
			Namespace: ns,
		}, &svc)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Verify ClusterIP = "None" (headless).
	if svc.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Fatalf("expected ClusterIP=%q, got %q", corev1.ClusterIPNone, svc.Spec.ClusterIP)
	}

	// Verify selector labels.
	expectedSelector := map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": clawName,
	}
	for k, v := range expectedSelector {
		if got := svc.Spec.Selector[k]; got != v {
			t.Errorf("Service selector %q: expected %q, got %q", k, v, got)
		}
	}

	// Verify port named "gateway" with port 18900.
	var gatewayPort *corev1.ServicePort
	for i := range svc.Spec.Ports {
		if svc.Spec.Ports[i].Name == "gateway" {
			gatewayPort = &svc.Spec.Ports[i]
			break
		}
	}
	if gatewayPort == nil {
		t.Fatal("expected port named 'gateway', not found")
	}
	if gatewayPort.Port != 18900 {
		t.Errorf("expected gateway port=18900, got %d", gatewayPort.Port)
	}

	// Verify ownerReferences (kind=Claw).
	if len(svc.OwnerReferences) != 1 {
		t.Fatalf("expected 1 ownerReference, got %d", len(svc.OwnerReferences))
	}
	if svc.OwnerReferences[0].Kind != "Claw" {
		t.Errorf("expected ownerReference kind=Claw, got %q", svc.OwnerReferences[0].Kind)
	}

	// Verify standard labels on Service.
	expectedLabels := map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": clawName,
		"claw.prismer.ai/runtime":    "openclaw",
		"claw.prismer.ai/instance":   clawName,
	}
	for k, v := range expectedLabels {
		if got := svc.Labels[k]; got != v {
			t.Errorf("Service label %q: expected %q, got %q", k, v, got)
		}
	}
}

func TestClawReconciler_ConfigMapCreated(t *testing.T) {
	ns := fmt.Sprintf("test-cm-create-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-cm"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeNanoClaw,
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the ConfigMap to appear.
	cmName := fmt.Sprintf("%s-config", clawName)
	var cm corev1.ConfigMap
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      cmName,
			Namespace: ns,
		}, &cm)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Verify ownerReferences (kind=Claw).
	if len(cm.OwnerReferences) != 1 {
		t.Fatalf("expected 1 ownerReference, got %d", len(cm.OwnerReferences))
	}
	if cm.OwnerReferences[0].Kind != "Claw" {
		t.Errorf("expected ownerReference kind=Claw, got %q", cm.OwnerReferences[0].Kind)
	}

	// Verify labels.
	expectedLabels := map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": clawName,
	}
	for k, v := range expectedLabels {
		if got := cm.Labels[k]; got != v {
			t.Errorf("ConfigMap label %q: expected %q, got %q", k, v, got)
		}
	}

	// Verify config.json key exists in ConfigMap data.
	configJSON, ok := cm.Data["config.json"]
	if !ok {
		t.Fatal("expected config.json key in ConfigMap data, not found")
	}
	if configJSON == "" {
		t.Fatal("expected non-empty config.json value")
	}

	// Verify the config.json is valid JSON and contains expected NanoClaw defaults.
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(configJSON), &parsed); err != nil {
		t.Fatalf("config.json is not valid JSON: %v", err)
	}
	if port, ok := parsed["gatewayPort"]; !ok {
		t.Error("expected gatewayPort in config.json")
	} else if int(port.(float64)) != 19000 {
		t.Errorf("expected gatewayPort=19000, got %v", port)
	}
}

func TestClawReconciler_CredentialInjection(t *testing.T) {
	ns := fmt.Sprintf("test-cred-inject-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Pre-create the Secret that the Claw will reference.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claw-creds",
			Namespace: ns,
		},
		Data: map[string][]byte{
			"OPENAI_API_KEY": []byte("sk-test-key-12345"),
		},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("failed to create Secret: %v", err)
	}

	// Create a Claw with credentials.secretRef.
	clawName := "test-claw-cred"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Credentials: &clawv1alpha1.CredentialSpec{
				SecretRef: &corev1.LocalObjectReference{
					Name: "test-claw-creds",
				},
			},
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

	// Find the runtime container.
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

	// Verify envFrom contains the secret ref.
	found := false
	for _, ef := range runtimeContainer.EnvFrom {
		if ef.SecretRef != nil && ef.SecretRef.Name == "test-claw-creds" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected envFrom with secretRef 'test-claw-creds', not found")
	}

	// Verify pod template annotation for secret hash.
	hash, ok := sts.Spec.Template.Annotations["claw.prismer.ai/secret-hash"]
	if !ok {
		t.Fatal("expected annotation 'claw.prismer.ai/secret-hash', not found")
	}
	if hash == "" {
		t.Error("expected non-empty secret hash annotation")
	}
}

func TestClawReconciler_CredentialKeyMapping(t *testing.T) {
	ns := fmt.Sprintf("test-cred-keys-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Pre-create a Secret with multiple keys.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "multi-key-secret",
			Namespace: ns,
		},
		Data: map[string][]byte{
			"api-key":    []byte("key-value-123"),
			"api-secret": []byte("secret-value-456"),
		},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("failed to create Secret: %v", err)
	}

	// Create a Claw with credentials.keys (per-key mapping).
	clawName := "test-claw-keymap"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Credentials: &clawv1alpha1.CredentialSpec{
				Keys: []clawv1alpha1.KeyMapping{
					{
						Name: "OPENAI_KEY",
						SecretKeyRef: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "multi-key-secret",
							},
							Key: "api-key",
						},
					},
				},
			},
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

	// Find the runtime container.
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

	// Verify env var OPENAI_KEY with valueFrom.secretKeyRef.
	found := false
	for _, env := range runtimeContainer.Env {
		if env.Name == "OPENAI_KEY" &&
			env.ValueFrom != nil &&
			env.ValueFrom.SecretKeyRef != nil &&
			env.ValueFrom.SecretKeyRef.Name == "multi-key-secret" &&
			env.ValueFrom.SecretKeyRef.Key == "api-key" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected env var OPENAI_KEY with secretKeyRef referencing multi-key-secret/api-key, not found")
	}
}

func TestClawReconciler_UnknownRuntime(t *testing.T) {
	ns := fmt.Sprintf("test-sts-unknown-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Use "custom" which passes CRD enum validation but has no adapter registered.
	// The validating webhook should reject this at admission time.
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claw-unknown",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeCustom,
		},
	}

	err := k8sClient.Create(ctx, claw)
	if err == nil {
		t.Fatal("expected webhook to reject create with unsupported runtime, but it succeeded")
	}
	t.Logf("webhook correctly rejected unknown runtime: %v", err)
}

func TestClawReconciler_PVCReclaimDelete(t *testing.T) {
	ns := fmt.Sprintf("test-reclaim-del-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-reclaim-del"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				ReclaimPolicy: clawv1alpha1.ReclaimDelete,
				Session: &clawv1alpha1.VolumeSpec{
					Enabled:   true,
					Size:      "1Gi",
					MountPath: "/var/lib/claw/session",
				},
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the finalizer to be added by the reconciler.
	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	var latest clawv1alpha1.Claw
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		if err := k8sClient.Get(ctx, nn, &latest); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return controllerutil.ContainsFinalizer(&latest, clawFinalizer), nil
	})

	// Pre-create a PVC with the claw instance label, mimicking what a StatefulSet would create.
	pvcName := fmt.Sprintf("session-%s-0", clawName)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: ns,
			Labels: map[string]string{
				"claw.prismer.ai/instance": clawName,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, pvc); err != nil {
		t.Fatalf("failed to create PVC: %v", err)
	}

	// Wait for the PVC to be visible in the cache before deleting the Claw,
	// so the reconciler's List call can find it during deletion.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var pvcList corev1.PersistentVolumeClaimList
		if err := k8sClient.List(ctx, &pvcList,
			client.InNamespace(ns),
			client.MatchingLabels{"claw.prismer.ai/instance": clawName},
		); err != nil {
			return false, err
		}
		return len(pvcList.Items) > 0, nil
	})

	// Delete the Claw (re-fetch to get latest resourceVersion).
	if err := k8sClient.Get(ctx, nn, &latest); err != nil {
		t.Fatalf("failed to re-fetch Claw: %v", err)
	}
	if err := k8sClient.Delete(ctx, &latest); err != nil {
		t.Fatalf("failed to delete Claw: %v", err)
	}

	// Wait for the Claw to be fully deleted.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.Claw
		err := k8sClient.Get(ctx, nn, &fetched)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return true, nil
			}
			return false, err
		}
		return false, nil
	})

	// Verify the PVC was deleted (or is being deleted).
	// In envtest the kubernetes.io/pvc-protection finalizer controller does not run,
	// so PVCs may retain a DeletionTimestamp without being fully removed. We accept
	// either NotFound or DeletionTimestamp being set as proof of deletion.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetchedPVC corev1.PersistentVolumeClaim
		err := k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: ns}, &fetchedPVC)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return true, nil
			}
			return false, err
		}
		// PVC exists but has a deletion timestamp — the delete was issued.
		return !fetchedPVC.DeletionTimestamp.IsZero(), nil
	})
}

func TestClawReconciler_PVCReclaimRetain(t *testing.T) {
	ns := fmt.Sprintf("test-reclaim-ret-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-reclaim-ret"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				ReclaimPolicy: clawv1alpha1.ReclaimRetain,
				Session: &clawv1alpha1.VolumeSpec{
					Enabled:   true,
					Size:      "1Gi",
					MountPath: "/var/lib/claw/session",
				},
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the finalizer to be added by the reconciler.
	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	var latest clawv1alpha1.Claw
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		if err := k8sClient.Get(ctx, nn, &latest); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return controllerutil.ContainsFinalizer(&latest, clawFinalizer), nil
	})

	// Pre-create a PVC with the claw instance label.
	pvcName := fmt.Sprintf("workspace-%s-0", clawName)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: ns,
			Labels: map[string]string{
				"claw.prismer.ai/instance": clawName,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, pvc); err != nil {
		t.Fatalf("failed to create PVC: %v", err)
	}

	// Delete the Claw (re-fetch to get latest resourceVersion).
	if err := k8sClient.Get(ctx, nn, &latest); err != nil {
		t.Fatalf("failed to re-fetch Claw: %v", err)
	}
	if err := k8sClient.Delete(ctx, &latest); err != nil {
		t.Fatalf("failed to delete Claw: %v", err)
	}

	// Wait for the Claw to be fully deleted.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.Claw
		err := k8sClient.Get(ctx, nn, &fetched)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return true, nil
			}
			return false, err
		}
		return false, nil
	})

	// Verify the PVC still exists (Retain policy should not delete it).
	var fetchedPVC corev1.PersistentVolumeClaim
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: ns}, &fetchedPVC); err != nil {
		t.Fatalf("expected PVC %q to still exist with Retain policy, but got error: %v", pvcName, err)
	}
}

func TestClawReconciler_ServiceAccountCreated(t *testing.T) {
	ns := fmt.Sprintf("test-sa-create-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-sa"
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

	// Wait for the ServiceAccount to appear.
	var sa corev1.ServiceAccount
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      clawName,
			Namespace: ns,
		}, &sa)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Verify automountServiceAccountToken = false.
	if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken != false {
		t.Error("expected AutomountServiceAccountToken=false")
	}

	// Verify labels.
	expectedLabels := map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": clawName,
		"claw.prismer.ai/runtime":    "openclaw",
		"claw.prismer.ai/instance":   clawName,
	}
	for k, v := range expectedLabels {
		if got := sa.Labels[k]; got != v {
			t.Errorf("SA label %q: expected %q, got %q", k, v, got)
		}
	}

	// Verify ownerReferences (kind=Claw).
	if len(sa.OwnerReferences) != 1 {
		t.Fatalf("expected 1 ownerReference, got %d", len(sa.OwnerReferences))
	}
	if sa.OwnerReferences[0].Kind != "Claw" {
		t.Errorf("expected ownerReference kind=Claw, got %q", sa.OwnerReferences[0].Kind)
	}

	// Verify StatefulSet references the SA.
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

	if sts.Spec.Template.Spec.ServiceAccountName != clawName {
		t.Errorf("expected StatefulSet serviceAccountName=%q, got %q", clawName, sts.Spec.Template.Spec.ServiceAccountName)
	}
	if sts.Spec.Template.Spec.AutomountServiceAccountToken == nil || *sts.Spec.Template.Spec.AutomountServiceAccountToken != false {
		t.Error("expected StatefulSet automountServiceAccountToken=false")
	}
}

func TestClawReconciler_ServiceAccountUserManaged(t *testing.T) {
	ns := fmt.Sprintf("test-sa-user-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-sa-custom"
	customSAName := "my-custom-sa"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			ServiceAccount: &clawv1alpha1.ServiceAccountRef{
				Name: customSAName,
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the StatefulSet to appear (proves reconcile completed).
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

	// Verify the operator did NOT create a SA named after the claw.
	var sa corev1.ServiceAccount
	err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      clawName,
		Namespace: ns,
	}, &sa)
	if err == nil {
		t.Error("expected no operator-managed SA to be created when user-managed SA is specified")
	} else if client.IgnoreNotFound(err) != nil {
		t.Fatalf("unexpected error checking for SA: %v", err)
	}

	// Verify StatefulSet references the custom SA name.
	if sts.Spec.Template.Spec.ServiceAccountName != customSAName {
		t.Errorf("expected StatefulSet serviceAccountName=%q, got %q", customSAName, sts.Spec.Template.Spec.ServiceAccountName)
	}

	// Verify automountServiceAccountToken is NOT forced false for user-managed SA.
	if sts.Spec.Template.Spec.AutomountServiceAccountToken != nil {
		t.Errorf("expected AutomountServiceAccountToken to be nil (not overridden) for user-managed SA, got %v", *sts.Spec.Template.Spec.AutomountServiceAccountToken)
	}
}

// --- Webhook integration tests ---

func TestWebhook_RejectsInvalidCreate_CredentialExclusivity(t *testing.T) {
	ns := fmt.Sprintf("test-wh-cred-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bad-creds",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Credentials: &clawv1alpha1.CredentialSpec{
				SecretRef:      &corev1.LocalObjectReference{Name: "my-secret"},
				ExternalSecret: &clawv1alpha1.ExternalSecretRef{Provider: "vault", Store: "s", Path: "p"},
			},
		},
	}

	err := k8sClient.Create(ctx, claw)
	if err == nil {
		t.Fatal("expected webhook to reject create with both secretRef and externalSecret, but it succeeded")
	}
	t.Logf("webhook correctly rejected create: %v", err)
}

func TestWebhook_RejectsRuntimeChange(t *testing.T) {
	ns := fmt.Sprintf("test-wh-immut-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "immut-runtime",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for reconciler to process (ensures object is settled).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: claw.Name, Namespace: ns}, &fetched); err != nil {
			return false, nil
		}
		return controllerutil.ContainsFinalizer(&fetched, clawFinalizer), nil
	})

	// Re-fetch and attempt runtime change.
	var fetched clawv1alpha1.Claw
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: claw.Name, Namespace: ns}, &fetched); err != nil {
		t.Fatalf("failed to get Claw: %v", err)
	}
	fetched.Spec.Runtime = clawv1alpha1.RuntimeNanoClaw
	err := k8sClient.Update(ctx, &fetched)
	if err == nil {
		t.Fatal("expected webhook to reject runtime change, but update succeeded")
	}
	t.Logf("webhook correctly rejected runtime change: %v", err)
}

func TestWebhook_DefaultsReclaimPolicy(t *testing.T) {
	ns := fmt.Sprintf("test-wh-default-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default-reclaim",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				Session: &clawv1alpha1.VolumeSpec{
					Enabled:   true,
					Size:      "1Gi",
					MountPath: "/data/session",
				},
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the object to appear in the cache and verify the defaulter set reclaimPolicy.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: claw.Name, Namespace: ns}, &fetched); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		if fetched.Spec.Persistence == nil {
			return false, nil
		}
		if fetched.Spec.Persistence.ReclaimPolicy != clawv1alpha1.ReclaimRetain {
			t.Errorf("expected reclaimPolicy to be defaulted to %q, got %q", clawv1alpha1.ReclaimRetain, fetched.Spec.Persistence.ReclaimPolicy)
		}
		return true, nil
	})
}

func TestClawReconciler_NoRoleWithoutSelfConfigure(t *testing.T) {
	ns := fmt.Sprintf("test-no-rbac-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-no-rbac"
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

	// Wait for reconcile to complete (StatefulSet exists).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var sts appsv1.StatefulSet
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Verify no Role was created (needsRBAC returns false without SelfConfigure).
	var role rbacv1.Role
	err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &role)
	if err == nil {
		t.Error("expected no Role to be created without selfConfigure, but Role exists")
	} else if client.IgnoreNotFound(err) != nil {
		t.Fatalf("unexpected error checking for Role: %v", err)
	}

	// Verify no RoleBinding was created.
	var rb rbacv1.RoleBinding
	err = k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &rb)
	if err == nil {
		t.Error("expected no RoleBinding to be created without selfConfigure, but RoleBinding exists")
	} else if client.IgnoreNotFound(err) != nil {
		t.Fatalf("unexpected error checking for RoleBinding: %v", err)
	}
}
