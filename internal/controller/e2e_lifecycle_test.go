package controller

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const e2eTimeout = 15 * time.Second

func TestE2E_OpenClawFullLifecycle(t *testing.T) {
	ns := fmt.Sprintf("e2e-lifecycle-%d", time.Now().UnixNano())
	createNamespace(t, ns)
	nn := types.NamespacedName{Name: "lifecycle-claw", Namespace: ns}

	// Pre-create credential Secret.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test-creds", Namespace: ns},
		Data:       map[string][]byte{"API_KEY": []byte("sk-test")},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("failed to create Secret: %v", err)
	}

	// Create Claw with credentials + persistence + observability.
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: nn.Name, Namespace: ns},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Credentials: &clawv1alpha1.CredentialSpec{
				SecretRef: &corev1.LocalObjectReference{Name: "test-creds"},
			},
			Persistence: &clawv1alpha1.PersistenceSpec{
				ReclaimPolicy: clawv1alpha1.ReclaimDelete,
				Session: &clawv1alpha1.VolumeSpec{
					Enabled:   true,
					Size:      "1Gi",
					MountPath: "/var/lib/claw/session",
				},
				Workspace: &clawv1alpha1.VolumeSpec{
					Enabled:   true,
					Size:      "5Gi",
					MountPath: "/workspace",
				},
			},
			Observability: &clawv1alpha1.ObservabilitySpec{
				Metrics: true,
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	t.Run("all sub-resources created", func(t *testing.T) {
		// StatefulSet
		var sts appsv1.StatefulSet
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			err := k8sClient.Get(ctx, nn, &sts)
			if client.IgnoreNotFound(err) == nil && err != nil {
				return false, nil
			}
			return err == nil, err
		})

		// Verify runtime container image + probes.
		var rt *corev1.Container
		for i := range sts.Spec.Template.Spec.Containers {
			if sts.Spec.Template.Spec.Containers[i].Name == "runtime" {
				rt = &sts.Spec.Template.Spec.Containers[i]
				break
			}
		}
		if rt == nil {
			t.Fatal("runtime container not found")
		}
		if rt.Image != "ghcr.io/prismer-ai/k8s4claw-openclaw:latest" {
			t.Errorf("image = %q, want openclaw image", rt.Image)
		}
		if rt.LivenessProbe == nil || rt.ReadinessProbe == nil {
			t.Error("expected both liveness and readiness probes")
		}

		// Verify credentials injected via envFrom.
		foundCred := false
		for _, ef := range rt.EnvFrom {
			if ef.SecretRef != nil && ef.SecretRef.Name == "test-creds" {
				foundCred = true
				break
			}
		}
		if !foundCred {
			t.Error("expected envFrom with secretRef test-creds")
		}

		// Verify persistence volumeClaimTemplates.
		if len(sts.Spec.VolumeClaimTemplates) < 2 {
			t.Errorf("expected >= 2 volumeClaimTemplates (session + workspace), got %d", len(sts.Spec.VolumeClaimTemplates))
		}

		// Service (headless).
		var svc corev1.Service
		if err := k8sClient.Get(ctx, nn, &svc); err != nil {
			t.Fatalf("Service not created: %v", err)
		}
		if svc.Spec.ClusterIP != corev1.ClusterIPNone {
			t.Errorf("expected headless service, got ClusterIP=%q", svc.Spec.ClusterIP)
		}

		// ConfigMap.
		cmNN := types.NamespacedName{Name: nn.Name + "-config", Namespace: ns}
		var cm corev1.ConfigMap
		if err := k8sClient.Get(ctx, cmNN, &cm); err != nil {
			t.Fatalf("ConfigMap not created: %v", err)
		}
		configJSON := cm.Data["config.json"]
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(configJSON), &parsed); err != nil {
			t.Fatalf("config.json invalid JSON: %v", err)
		}
		if int(parsed["gatewayPort"].(float64)) != 18900 {
			t.Errorf("expected gatewayPort=18900, got %v", parsed["gatewayPort"])
		}

		// ServiceAccount.
		var sa corev1.ServiceAccount
		if err := k8sClient.Get(ctx, nn, &sa); err != nil {
			t.Fatalf("ServiceAccount not created: %v", err)
		}

		// PDB.
		var pdb policyv1.PodDisruptionBudget
		if err := k8sClient.Get(ctx, nn, &pdb); err != nil {
			t.Fatalf("PDB not created: %v", err)
		}
	})

	t.Run("status reaches Provisioning", func(t *testing.T) {
		var fetched clawv1alpha1.Claw
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
				return false, err
			}
			return fetched.Status.Phase == clawv1alpha1.ClawPhaseProvisioning, nil
		})
		if fetched.Status.ObservedGeneration != fetched.Generation {
			t.Errorf("observedGeneration = %d, want %d", fetched.Status.ObservedGeneration, fetched.Generation)
		}
	})

	t.Run("config update propagates to ConfigMap", func(t *testing.T) {
		// Update Claw config.
		var latest clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, nn, &latest); err != nil {
			t.Fatalf("failed to get Claw: %v", err)
		}
		if latest.Spec.Config == nil {
			latest.Spec.Config = &apiextensionsv1.JSON{}
		}
		latest.Spec.Config.Raw = []byte(`{"model":"claude-sonnet-4","customKey":"test-value"}`)
		if err := k8sClient.Update(ctx, &latest); err != nil {
			t.Fatalf("failed to update Claw config: %v", err)
		}

		// Wait for ConfigMap to reflect the new config.
		cmNN := types.NamespacedName{Name: nn.Name + "-config", Namespace: ns}
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			var cm corev1.ConfigMap
			if err := k8sClient.Get(ctx, cmNN, &cm); err != nil {
				return false, err
			}
			var parsed map[string]any
			if err := json.Unmarshal([]byte(cm.Data["config.json"]), &parsed); err != nil {
				return false, nil
			}
			_, hasCustom := parsed["customKey"]
			return hasCustom, nil
		})
	})

	t.Run("delete removes Claw and finalizer", func(t *testing.T) {
		// Re-fetch and delete.
		var latest clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, nn, &latest); err != nil {
			t.Fatalf("failed to get Claw: %v", err)
		}
		if err := k8sClient.Delete(ctx, &latest); err != nil {
			t.Fatalf("failed to delete Claw: %v", err)
		}

		// Wait for Claw to be fully deleted (finalizer removed by reconciler).
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			err := k8sClient.Get(ctx, nn, &clawv1alpha1.Claw{})
			if client.IgnoreNotFound(err) == nil {
				return true, nil
			}
			return false, client.IgnoreNotFound(err)
		})
	})
}

func TestE2E_MultiRuntime(t *testing.T) {
	tests := []struct {
		runtime   clawv1alpha1.RuntimeType
		wantImage string
		wantPort  int32
	}{
		{clawv1alpha1.RuntimeOpenClaw, "ghcr.io/prismer-ai/k8s4claw-openclaw:latest", 18900},
		{clawv1alpha1.RuntimeNanoClaw, "ghcr.io/prismer-ai/k8s4claw-nanoclaw:latest", 19000},
		{clawv1alpha1.RuntimeZeroClaw, "ghcr.io/prismer-ai/k8s4claw-zeroclaw:latest", 3000},
		{clawv1alpha1.RuntimePicoClaw, "ghcr.io/prismer-ai/k8s4claw-picoclaw:latest", 8080},
		{clawv1alpha1.RuntimeIronClaw, "ghcr.io/prismer-ai/k8s4claw-ironclaw:latest", 3001},
	}

	for _, tt := range tests {
		t.Run(string(tt.runtime), func(t *testing.T) {
			ns := fmt.Sprintf("e2e-runtime-%s-%d", tt.runtime, time.Now().UnixNano())
			createNamespace(t, ns)
			nn := types.NamespacedName{Name: "rt-claw", Namespace: ns}

			claw := &clawv1alpha1.Claw{
				ObjectMeta: metav1.ObjectMeta{Name: nn.Name, Namespace: ns},
				Spec:       clawv1alpha1.ClawSpec{Runtime: tt.runtime},
			}
			if err := k8sClient.Create(ctx, claw); err != nil {
				t.Fatalf("failed to create Claw: %v", err)
			}

			// Wait for StatefulSet.
			var sts appsv1.StatefulSet
			waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
				err := k8sClient.Get(ctx, nn, &sts)
				if client.IgnoreNotFound(err) == nil && err != nil {
					return false, nil
				}
				return err == nil, err
			})

			// Verify image.
			var rt *corev1.Container
			for i := range sts.Spec.Template.Spec.Containers {
				if sts.Spec.Template.Spec.Containers[i].Name == "runtime" {
					rt = &sts.Spec.Template.Spec.Containers[i]
					break
				}
			}
			if rt == nil {
				t.Fatal("runtime container not found")
			}
			if rt.Image != tt.wantImage {
				t.Errorf("image = %q, want %q", rt.Image, tt.wantImage)
			}

			// Verify gateway port on Service.
			var svc corev1.Service
			if err := k8sClient.Get(ctx, nn, &svc); err != nil {
				t.Fatalf("Service not created: %v", err)
			}
			var gwPort *corev1.ServicePort
			for i := range svc.Spec.Ports {
				if svc.Spec.Ports[i].Name == "gateway" {
					gwPort = &svc.Spec.Ports[i]
					break
				}
			}
			if gwPort == nil {
				t.Fatal("gateway port not found on Service")
			}
			if gwPort.Port != tt.wantPort {
				t.Errorf("gateway port = %d, want %d", gwPort.Port, tt.wantPort)
			}

			// Verify runtime-specific label.
			if got := sts.Labels["claw.prismer.ai/runtime"]; got != string(tt.runtime) {
				t.Errorf("runtime label = %q, want %q", got, tt.runtime)
			}
		})
	}
}

func TestE2E_ChannelWithIPCBus(t *testing.T) {
	ns := fmt.Sprintf("e2e-channel-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create ClawChannel.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-webhook", Namespace: ns},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeWebhook,
			Mode: clawv1alpha1.ChannelModeBidirectional,
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Create Claw referencing the channel.
	clawNN := types.NamespacedName{Name: "channel-claw", Namespace: ns}
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: clawNN.Name, Namespace: ns},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "e2e-webhook", Mode: clawv1alpha1.ChannelModeBidirectional},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	t.Run("IPC Bus and channel sidecar injected", func(t *testing.T) {
		var sts appsv1.StatefulSet
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			err := k8sClient.Get(ctx, clawNN, &sts)
			if client.IgnoreNotFound(err) == nil && err != nil {
				return false, nil
			}
			return err == nil, err
		})

		// Verify IPC Bus init container exists with restartPolicy=Always.
		var ipcBus *corev1.Container
		for i := range sts.Spec.Template.Spec.InitContainers {
			if sts.Spec.Template.Spec.InitContainers[i].Name == "ipc-bus" {
				ipcBus = &sts.Spec.Template.Spec.InitContainers[i]
				break
			}
		}
		if ipcBus == nil {
			t.Fatal("ipc-bus init container not found")
		}
		if ipcBus.RestartPolicy == nil || *ipcBus.RestartPolicy != corev1.ContainerRestartPolicyAlways {
			t.Error("expected ipc-bus restartPolicy=Always")
		}
		if ipcBus.Image != "ghcr.io/prismer-ai/claw-ipcbus:latest" {
			t.Errorf("ipc-bus image = %q, want ipcbus image", ipcBus.Image)
		}

		// Verify channel sidecar container.
		var chContainer *corev1.Container
		for i := range sts.Spec.Template.Spec.InitContainers {
			if sts.Spec.Template.Spec.InitContainers[i].Name == "channel-e2e-webhook" {
				chContainer = &sts.Spec.Template.Spec.InitContainers[i]
				break
			}
		}
		if chContainer == nil {
			t.Fatal("channel-e2e-webhook sidecar container not found in init containers")
		}

		// Verify shared ipc-socket volume mount on both.
		for _, c := range []*corev1.Container{ipcBus, chContainer} {
			found := false
			for _, m := range c.VolumeMounts {
				if m.Name == "ipc-socket" && m.MountPath == "/var/run/claw" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("container %q missing ipc-socket mount at /var/run/claw", c.Name)
			}
		}

		// Verify channel env vars.
		envMap := make(map[string]string)
		for _, e := range chContainer.Env {
			envMap[e.Name] = e.Value
		}
		if envMap["CHANNEL_NAME"] != "e2e-webhook" {
			t.Errorf("CHANNEL_NAME = %q, want e2e-webhook", envMap["CHANNEL_NAME"])
		}
		if envMap["CHANNEL_TYPE"] != "webhook" {
			t.Errorf("CHANNEL_TYPE = %q, want webhook", envMap["CHANNEL_TYPE"])
		}
	})

	t.Run("channel refCount updated", func(t *testing.T) {
		chNN := types.NamespacedName{Name: "e2e-webhook", Namespace: ns}
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			var ch clawv1alpha1.ClawChannel
			if err := k8sClient.Get(ctx, chNN, &ch); err != nil {
				return false, err
			}
			return ch.Status.ReferenceCount > 0, nil
		})
	})

	t.Run("delete claw and verify channel survives", func(t *testing.T) {
		var latest clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, clawNN, &latest); err != nil {
			t.Fatalf("failed to get Claw: %v", err)
		}
		if err := k8sClient.Delete(ctx, &latest); err != nil {
			t.Fatalf("failed to delete Claw: %v", err)
		}

		// Wait for Claw deletion.
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			err := k8sClient.Get(ctx, clawNN, &clawv1alpha1.Claw{})
			if client.IgnoreNotFound(err) == nil {
				return true, nil
			}
			return false, client.IgnoreNotFound(err)
		})

		// Verify channel still exists after Claw deletion (no cascade).
		chNN := types.NamespacedName{Name: "e2e-webhook", Namespace: ns}
		var ch clawv1alpha1.ClawChannel
		if err := k8sClient.Get(ctx, chNN, &ch); err != nil {
			t.Fatalf("channel should still exist: %v", err)
		}
	})
}

func TestE2E_SecurityAndNetwork(t *testing.T) {
	ns := fmt.Sprintf("e2e-security-%d", time.Now().UnixNano())
	createNamespace(t, ns)
	nn := types.NamespacedName{Name: "secure-claw", Namespace: ns}

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: nn.Name, Namespace: ns},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Security: &clawv1alpha1.SecuritySpec{
				NetworkPolicy: &clawv1alpha1.NetworkPolicySpec{
					Enabled:            true,
					AllowedEgressCIDRs: []string{"10.0.0.0/8"},
				},
			},
			Ingress: &clawv1alpha1.IngressSpec{
				Enabled:   true,
				Host:      "test.example.com",
				ClassName: "nginx",
				TLS: &clawv1alpha1.IngressTLS{
					SecretName: "test-tls",
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	t.Run("NetworkPolicy created with correct rules", func(t *testing.T) {
		npNN := types.NamespacedName{Name: nn.Name + "-netpol", Namespace: ns}
		var np networkingv1.NetworkPolicy
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			err := k8sClient.Get(ctx, npNN, &np)
			if client.IgnoreNotFound(err) == nil && err != nil {
				return false, nil
			}
			return err == nil, err
		})

		// Verify pod selector.
		if got := np.Spec.PodSelector.MatchLabels["claw.prismer.ai/instance"]; got != nn.Name {
			t.Errorf("podSelector instance label = %q, want %q", got, nn.Name)
		}

		// Verify egress rules include DNS (port 53) and HTTPS (port 443).
		foundDNS, foundHTTPS := false, false
		for _, rule := range np.Spec.Egress {
			for _, port := range rule.Ports {
				if port.Port != nil && port.Port.IntValue() == 53 {
					foundDNS = true
				}
				if port.Port != nil && port.Port.IntValue() == 443 {
					foundHTTPS = true
				}
			}
		}
		if !foundDNS {
			t.Error("expected egress rule for DNS (port 53)")
		}
		if !foundHTTPS {
			t.Error("expected egress rule for HTTPS (port 443)")
		}
	})

	t.Run("Ingress created with TLS", func(t *testing.T) {
		var ing networkingv1.Ingress
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			err := k8sClient.Get(ctx, nn, &ing)
			if client.IgnoreNotFound(err) == nil && err != nil {
				return false, nil
			}
			return err == nil, err
		})

		// Verify host.
		if len(ing.Spec.Rules) == 0 {
			t.Fatal("expected at least 1 Ingress rule")
		}
		if ing.Spec.Rules[0].Host != "test.example.com" {
			t.Errorf("host = %q, want test.example.com", ing.Spec.Rules[0].Host)
		}

		// Verify TLS.
		if len(ing.Spec.TLS) == 0 {
			t.Fatal("expected TLS configuration")
		}
		if ing.Spec.TLS[0].SecretName != "test-tls" {
			t.Errorf("TLS secretName = %q, want test-tls", ing.Spec.TLS[0].SecretName)
		}

		// Verify ingressClassName.
		if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "nginx" {
			t.Errorf("ingressClassName = %v, want nginx", ing.Spec.IngressClassName)
		}
	})

	t.Run("disable NetworkPolicy removes it", func(t *testing.T) {
		var latest clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, nn, &latest); err != nil {
			t.Fatalf("failed to get Claw: %v", err)
		}
		latest.Spec.Security.NetworkPolicy.Enabled = false
		if err := k8sClient.Update(ctx, &latest); err != nil {
			t.Fatalf("failed to update Claw: %v", err)
		}

		npNN := types.NamespacedName{Name: nn.Name + "-netpol", Namespace: ns}
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			err := k8sClient.Get(ctx, npNN, &networkingv1.NetworkPolicy{})
			if client.IgnoreNotFound(err) == nil && err != nil {
				return true, nil // NotFound = deleted
			}
			return false, nil
		})

		// Ingress should still exist.
		if err := k8sClient.Get(ctx, nn, &networkingv1.Ingress{}); err != nil {
			t.Errorf("Ingress should still exist, got error: %v", err)
		}
	})
}

func TestE2E_PersistenceReclaimPolicies(t *testing.T) {
	t.Run("Delete policy removes PVCs", func(t *testing.T) {
		ns := fmt.Sprintf("e2e-reclaim-del-%d", time.Now().UnixNano())
		createNamespace(t, ns)
		nn := types.NamespacedName{Name: "reclaim-del-claw", Namespace: ns}

		claw := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: nn.Name, Namespace: ns},
			Spec: clawv1alpha1.ClawSpec{
				Runtime: clawv1alpha1.RuntimePicoClaw,
				Persistence: &clawv1alpha1.PersistenceSpec{
					ReclaimPolicy: clawv1alpha1.ReclaimDelete,
					Session: &clawv1alpha1.VolumeSpec{
						Enabled: true, Size: "1Gi", MountPath: "/data/session",
					},
				},
			},
		}
		if err := k8sClient.Create(ctx, claw); err != nil {
			t.Fatalf("failed to create Claw: %v", err)
		}

		// Wait for finalizer.
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			var fetched clawv1alpha1.Claw
			if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
				if client.IgnoreNotFound(err) == nil {
					return false, nil
				}
				return false, err
			}
			return controllerutil.ContainsFinalizer(&fetched, clawFinalizer), nil
		})

		// Pre-create PVC (simulating StatefulSet-created PVC).
		pvcName := fmt.Sprintf("session-%s-0", nn.Name)
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvcName, Namespace: ns,
				Labels: map[string]string{"claw.prismer.ai/instance": nn.Name},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}
		if err := k8sClient.Create(ctx, pvc); err != nil {
			t.Fatalf("failed to create PVC: %v", err)
		}

		// Wait for PVC to be visible in cache.
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			var list corev1.PersistentVolumeClaimList
			if err := k8sClient.List(ctx, &list, client.InNamespace(ns),
				client.MatchingLabels{"claw.prismer.ai/instance": nn.Name}); err != nil {
				return false, err
			}
			return len(list.Items) > 0, nil
		})

		// Delete Claw.
		var latest clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, nn, &latest); err != nil {
			t.Fatalf("failed to get Claw: %v", err)
		}
		if err := k8sClient.Delete(ctx, &latest); err != nil {
			t.Fatalf("failed to delete Claw: %v", err)
		}

		// Wait for Claw deletion + verify PVC deleted.
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			err := k8sClient.Get(ctx, nn, &clawv1alpha1.Claw{})
			if client.IgnoreNotFound(err) == nil {
				return true, nil
			}
			return false, client.IgnoreNotFound(err)
		})

		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			var p corev1.PersistentVolumeClaim
			err := k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: ns}, &p)
			if client.IgnoreNotFound(err) == nil && err != nil {
				return true, nil
			}
			if err == nil {
				return !p.DeletionTimestamp.IsZero(), nil
			}
			return false, err
		})
	})

	t.Run("Retain policy preserves PVCs", func(t *testing.T) {
		ns := fmt.Sprintf("e2e-reclaim-ret-%d", time.Now().UnixNano())
		createNamespace(t, ns)
		nn := types.NamespacedName{Name: "reclaim-ret-claw", Namespace: ns}

		claw := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: nn.Name, Namespace: ns},
			Spec: clawv1alpha1.ClawSpec{
				Runtime: clawv1alpha1.RuntimePicoClaw,
				Persistence: &clawv1alpha1.PersistenceSpec{
					ReclaimPolicy: clawv1alpha1.ReclaimRetain,
					Session: &clawv1alpha1.VolumeSpec{
						Enabled: true, Size: "1Gi", MountPath: "/data/session",
					},
				},
			},
		}
		if err := k8sClient.Create(ctx, claw); err != nil {
			t.Fatalf("failed to create Claw: %v", err)
		}

		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			var fetched clawv1alpha1.Claw
			if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
				if client.IgnoreNotFound(err) == nil {
					return false, nil
				}
				return false, err
			}
			return controllerutil.ContainsFinalizer(&fetched, clawFinalizer), nil
		})

		pvcName := fmt.Sprintf("session-%s-0", nn.Name)
		pvc := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvcName, Namespace: ns,
				Labels: map[string]string{"claw.prismer.ai/instance": nn.Name},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}
		if err := k8sClient.Create(ctx, pvc); err != nil {
			t.Fatalf("failed to create PVC: %v", err)
		}

		// Delete Claw.
		var latest clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, nn, &latest); err != nil {
			t.Fatalf("failed to get Claw: %v", err)
		}
		if err := k8sClient.Delete(ctx, &latest); err != nil {
			t.Fatalf("failed to delete Claw: %v", err)
		}

		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			err := k8sClient.Get(ctx, nn, &clawv1alpha1.Claw{})
			if client.IgnoreNotFound(err) == nil {
				return true, nil
			}
			return false, client.IgnoreNotFound(err)
		})

		// PVC should still exist.
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: ns}, &corev1.PersistentVolumeClaim{}); err != nil {
			t.Fatalf("PVC should still exist with Retain policy, got: %v", err)
		}
	})
}

func TestE2E_WebhookValidation(t *testing.T) {
	t.Run("rejects runtime change (immutable)", func(t *testing.T) {
		ns := fmt.Sprintf("e2e-wh-immut-%d", time.Now().UnixNano())
		createNamespace(t, ns)
		nn := types.NamespacedName{Name: "immut-claw", Namespace: ns}

		claw := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: nn.Name, Namespace: ns},
			Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
		}
		if err := k8sClient.Create(ctx, claw); err != nil {
			t.Fatalf("failed to create Claw: %v", err)
		}

		// Wait for reconcile to settle.
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			var fetched clawv1alpha1.Claw
			if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
				return false, nil
			}
			return controllerutil.ContainsFinalizer(&fetched, clawFinalizer), nil
		})

		// Attempt runtime change.
		var fetched clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
			t.Fatalf("failed to get Claw: %v", err)
		}
		fetched.Spec.Runtime = clawv1alpha1.RuntimeNanoClaw
		if err := k8sClient.Update(ctx, &fetched); err == nil {
			t.Fatal("expected webhook to reject runtime change")
		}
	})

	t.Run("rejects credential exclusivity violation", func(t *testing.T) {
		ns := fmt.Sprintf("e2e-wh-cred-%d", time.Now().UnixNano())
		createNamespace(t, ns)

		claw := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-creds", Namespace: ns},
			Spec: clawv1alpha1.ClawSpec{
				Runtime: clawv1alpha1.RuntimeOpenClaw,
				Credentials: &clawv1alpha1.CredentialSpec{
					SecretRef:      &corev1.LocalObjectReference{Name: "s1"},
					ExternalSecret: &clawv1alpha1.ExternalSecretRef{Provider: "vault", Store: "s", Path: "p"},
				},
			},
		}
		if err := k8sClient.Create(ctx, claw); err == nil {
			t.Fatal("expected webhook to reject both secretRef and externalSecret")
		}
	})

	t.Run("defaults reclaimPolicy to Retain", func(t *testing.T) {
		ns := fmt.Sprintf("e2e-wh-default-%d", time.Now().UnixNano())
		createNamespace(t, ns)
		nn := types.NamespacedName{Name: "default-claw", Namespace: ns}

		claw := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: nn.Name, Namespace: ns},
			Spec: clawv1alpha1.ClawSpec{
				Runtime: clawv1alpha1.RuntimeOpenClaw,
				Persistence: &clawv1alpha1.PersistenceSpec{
					Session: &clawv1alpha1.VolumeSpec{Enabled: true, Size: "1Gi", MountPath: "/data"},
				},
			},
		}
		if err := k8sClient.Create(ctx, claw); err != nil {
			t.Fatalf("failed to create Claw: %v", err)
		}

		var fetched clawv1alpha1.Claw
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
				if client.IgnoreNotFound(err) == nil {
					return false, nil
				}
				return false, err
			}
			return fetched.Spec.Persistence != nil, nil
		})

		if fetched.Spec.Persistence.ReclaimPolicy != clawv1alpha1.ReclaimRetain {
			t.Errorf("reclaimPolicy = %q, want Retain", fetched.Spec.Persistence.ReclaimPolicy)
		}
	})
}

func TestE2E_SelfConfig(t *testing.T) {
	ns := fmt.Sprintf("e2e-selfconfig-%d", time.Now().UnixNano())
	createNamespace(t, ns)
	nn := types.NamespacedName{Name: "selfconfig-claw", Namespace: ns}

	// Create Claw with selfConfigure enabled.
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: nn.Name, Namespace: ns},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			SelfConfigure: &clawv1alpha1.SelfConfigureSpec{
				Enabled:        true,
				AllowedActions: []string{"skills", "config", "envVars"},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	t.Run("Role and RoleBinding created", func(t *testing.T) {
		// Wait for Role.
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			err := k8sClient.Get(ctx, nn, &rbacv1.Role{})
			if client.IgnoreNotFound(err) == nil && err != nil {
				return false, nil
			}
			return err == nil, err
		})

		// Wait for RoleBinding.
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			err := k8sClient.Get(ctx, nn, &rbacv1.RoleBinding{})
			if client.IgnoreNotFound(err) == nil && err != nil {
				return false, nil
			}
			return err == nil, err
		})
	})

	t.Run("allowed SelfConfig applied", func(t *testing.T) {
		sc := &clawv1alpha1.ClawSelfConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "allowed-sc", Namespace: ns},
			Spec: clawv1alpha1.ClawSelfConfigSpec{
				ClawRef:     nn.Name,
				AddSkills:   []string{"tool-use"},
				ConfigPatch: map[string]string{"model": "claude-sonnet-4"},
			},
		}
		if err := k8sClient.Create(ctx, sc); err != nil {
			t.Fatalf("failed to create ClawSelfConfig: %v", err)
		}

		scNN := types.NamespacedName{Name: "allowed-sc", Namespace: ns}
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			var fetched clawv1alpha1.ClawSelfConfig
			if err := k8sClient.Get(ctx, scNN, &fetched); err != nil {
				if client.IgnoreNotFound(err) == nil {
					return false, nil
				}
				return false, err
			}
			return fetched.Status.Phase == clawv1alpha1.SelfConfigPhaseApplied, nil
		})
	})

	t.Run("denied SelfConfig rejected", func(t *testing.T) {
		sc := &clawv1alpha1.ClawSelfConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "denied-sc", Namespace: ns},
			Spec: clawv1alpha1.ClawSelfConfigSpec{
				ClawRef:           nn.Name,
				AddWorkspaceFiles: map[string]string{"hack.sh": "#!/bin/bash"},
			},
		}
		if err := k8sClient.Create(ctx, sc); err != nil {
			t.Fatalf("failed to create ClawSelfConfig: %v", err)
		}

		scNN := types.NamespacedName{Name: "denied-sc", Namespace: ns}
		waitForCondition(t, e2eTimeout, testInterval, func() (bool, error) {
			var fetched clawv1alpha1.ClawSelfConfig
			if err := k8sClient.Get(ctx, scNN, &fetched); err != nil {
				if client.IgnoreNotFound(err) == nil {
					return false, nil
				}
				return false, err
			}
			return fetched.Status.Phase == clawv1alpha1.SelfConfigPhaseDenied, nil
		})
	})
}
