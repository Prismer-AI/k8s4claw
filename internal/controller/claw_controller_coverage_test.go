package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimachineryruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

// ---------------------------------------------------------------------------
// Unit tests: mergeConfig / deepMerge
// ---------------------------------------------------------------------------

func TestMergeConfig(t *testing.T) {
	tests := []struct {
		name        string
		defaultJSON string
		userJSON    []byte
		wantKeys    map[string]interface{}
		wantErr     bool
	}{
		{
			name:        "nil user JSON returns defaults",
			defaultJSON: `{"gatewayPort":18900,"workspacePath":"/workspace"}`,
			userJSON:    nil,
			wantKeys:    map[string]interface{}{"gatewayPort": float64(18900), "workspacePath": "/workspace"},
		},
		{
			name:        "empty user JSON returns defaults",
			defaultJSON: `{"gatewayPort":18900}`,
			userJSON:    []byte{},
			wantKeys:    map[string]interface{}{"gatewayPort": float64(18900)},
		},
		{
			name:        "user overrides existing key",
			defaultJSON: `{"gatewayPort":18900,"model":"default"}`,
			userJSON:    []byte(`{"model":"claude-sonnet-4"}`),
			wantKeys:    map[string]interface{}{"gatewayPort": float64(18900), "model": "claude-sonnet-4"},
		},
		{
			name:        "user adds new key",
			defaultJSON: `{"gatewayPort":18900}`,
			userJSON:    []byte(`{"customField":"hello"}`),
			wantKeys:    map[string]interface{}{"gatewayPort": float64(18900), "customField": "hello"},
		},
		{
			name:        "invalid default JSON returns error",
			defaultJSON: `{invalid`,
			userJSON:    []byte(`{"key":"val"}`),
			wantErr:     true,
		},
		{
			name:        "invalid user JSON returns error",
			defaultJSON: `{"gatewayPort":18900}`,
			userJSON:    []byte(`{invalid`),
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := mergeConfig(tt.defaultJSON, tt.userJSON)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantKeys != nil {
				var parsed map[string]interface{}
				if err := json.Unmarshal([]byte(result), &parsed); err != nil {
					t.Fatalf("result is not valid JSON: %v", err)
				}
				for k, want := range tt.wantKeys {
					got, ok := parsed[k]
					if !ok {
						t.Errorf("missing key %q in merged result", k)
						continue
					}
					if got != want {
						t.Errorf("key %q: got %v, want %v", k, got, want)
					}
				}
			}
		})
	}
}

func TestDeepMerge(t *testing.T) {
	tests := []struct {
		name string
		dst  map[string]interface{}
		src  map[string]interface{}
		want map[string]interface{}
	}{
		{
			name: "simple override",
			dst:  map[string]interface{}{"a": "old"},
			src:  map[string]interface{}{"a": "new"},
			want: map[string]interface{}{"a": "new"},
		},
		{
			name: "add new key",
			dst:  map[string]interface{}{"a": "val"},
			src:  map[string]interface{}{"b": "new"},
			want: map[string]interface{}{"a": "val", "b": "new"},
		},
		{
			name: "nested map merge",
			dst: map[string]interface{}{
				"nested": map[string]interface{}{"x": float64(1), "y": float64(2)},
			},
			src: map[string]interface{}{
				"nested": map[string]interface{}{"y": float64(3), "z": float64(4)},
			},
			want: map[string]interface{}{
				"nested": map[string]interface{}{"x": float64(1), "y": float64(3), "z": float64(4)},
			},
		},
		{
			name: "src map replaces non-map dst value",
			dst:  map[string]interface{}{"key": "string-value"},
			src:  map[string]interface{}{"key": map[string]interface{}{"nested": true}},
			want: map[string]interface{}{"key": map[string]interface{}{"nested": true}},
		},
		{
			name: "src scalar replaces dst map",
			dst:  map[string]interface{}{"key": map[string]interface{}{"nested": true}},
			src:  map[string]interface{}{"key": "scalar"},
			want: map[string]interface{}{"key": "scalar"},
		},
		{
			name: "empty src returns dst",
			dst:  map[string]interface{}{"a": float64(1)},
			src:  map[string]interface{}{},
			want: map[string]interface{}{"a": float64(1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deepMerge(tt.dst, tt.src)

			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tt.want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("deepMerge:\n  got  %s\n  want %s", gotJSON, wantJSON)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Integration test: ConfigMap with user config merge
// ---------------------------------------------------------------------------

func TestClawReconciler_ConfigMapWithUserConfig(t *testing.T) {
	ns := fmt.Sprintf("test-cm-merge-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-cm-merge"
	userConfig := `{"model":"claude-sonnet-4","customSetting":true}`
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Config:  &apiextensionsv1.JSON{Raw: []byte(userConfig)},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the ConfigMap to appear.
	cmName := fmt.Sprintf("%s-config", clawName)
	var cm corev1.ConfigMap
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: ns}, &cm)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Parse the merged config.json.
	configJSON, ok := cm.Data["config.json"]
	if !ok {
		t.Fatal("expected config.json key in ConfigMap data")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(configJSON), &parsed); err != nil {
		t.Fatalf("config.json is not valid JSON: %v", err)
	}

	// Verify user config was merged (user's model should override default).
	if model, ok := parsed["model"]; !ok || model != "claude-sonnet-4" {
		t.Errorf("expected model=claude-sonnet-4, got %v", parsed["model"])
	}
	if cs, ok := parsed["customSetting"]; !ok || cs != true {
		t.Errorf("expected customSetting=true, got %v", parsed["customSetting"])
	}

	// Verify default keys are still present (gatewayPort from OpenClaw defaults).
	if gp, ok := parsed["gatewayPort"]; !ok {
		t.Error("expected gatewayPort from defaults to be present")
	} else if int(gp.(float64)) != 18900 {
		t.Errorf("expected gatewayPort=18900, got %v", gp)
	}
}

// ---------------------------------------------------------------------------
// Integration test: Channel sidecar injection into StatefulSet
// ---------------------------------------------------------------------------

func TestClawReconciler_ChannelSidecarInjected(t *testing.T) {
	ns := fmt.Sprintf("test-ch-inject-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a ClawChannel.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-slack-ch",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeSlack,
			Mode: clawv1alpha1.ChannelModeBidirectional,
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Create a Claw that references the channel.
	clawName := "test-claw-ch-inject"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeZeroClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{
					Name: "test-slack-ch",
					Mode: clawv1alpha1.ChannelModeInbound,
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
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// With NativeSidecarsEnabled=true, the channel sidecar should be in initContainers.
	found := false
	for _, c := range sts.Spec.Template.Spec.InitContainers {
		if c.Name == "channel-test-slack-ch" {
			found = true
			if c.Image != "ghcr.io/prismer-ai/claw-channel-slack:latest" {
				t.Errorf("expected slack sidecar image, got %q", c.Image)
			}
			// Verify ipc-socket volume mount.
			hasIPC := false
			for _, m := range c.VolumeMounts {
				if m.Name == "ipc-socket" {
					hasIPC = true
					break
				}
			}
			if !hasIPC {
				t.Error("expected ipc-socket volume mount on channel sidecar")
			}
			// Verify restartPolicy=Always (native sidecar).
			if c.RestartPolicy == nil || *c.RestartPolicy != corev1.ContainerRestartPolicyAlways {
				t.Error("expected restartPolicy=Always for native sidecar")
			}
			// Verify env vars.
			envMap := make(map[string]string)
			for _, e := range c.Env {
				envMap[e.Name] = e.Value
			}
			if envMap["CHANNEL_NAME"] != "test-slack-ch" {
				t.Errorf("expected CHANNEL_NAME=test-slack-ch, got %q", envMap["CHANNEL_NAME"])
			}
			if envMap["CHANNEL_TYPE"] != "slack" {
				t.Errorf("expected CHANNEL_TYPE=slack, got %q", envMap["CHANNEL_TYPE"])
			}
			if envMap["CHANNEL_MODE"] != "inbound" {
				t.Errorf("expected CHANNEL_MODE=inbound, got %q", envMap["CHANNEL_MODE"])
			}
			break
		}
	}
	if !found {
		t.Error("expected channel sidecar 'channel-test-slack-ch' in initContainers, not found")
	}
}

// ---------------------------------------------------------------------------
// Integration test: Channel sidecar skipped when ClawChannel not found
// ---------------------------------------------------------------------------

func TestClawReconciler_ChannelSidecarSkippedMissing(t *testing.T) {
	ns := fmt.Sprintf("test-ch-skip-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-ch-skip"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{
					Name: "nonexistent-channel",
					Mode: clawv1alpha1.ChannelModeBidirectional,
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the StatefulSet to appear (reconciler should still succeed).
	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Verify no channel sidecar was injected.
	for _, c := range sts.Spec.Template.Spec.InitContainers {
		if c.Name == "channel-nonexistent-channel" {
			t.Error("expected missing channel to be skipped, but sidecar was found")
		}
	}
}

// ---------------------------------------------------------------------------
// Integration test: Channel sidecar skipped on mode incompatibility
// ---------------------------------------------------------------------------

func TestClawReconciler_ChannelSidecarSkippedIncompatible(t *testing.T) {
	ns := fmt.Sprintf("test-ch-incompat-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create an inbound-only channel.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inbound-only-ch",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeWebhook,
			Mode: clawv1alpha1.ChannelModeInbound,
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Create a Claw requesting bidirectional on that inbound-only channel.
	clawName := "test-claw-ch-incompat"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{
					Name: "inbound-only-ch",
					Mode: clawv1alpha1.ChannelModeBidirectional,
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
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Verify the incompatible channel sidecar was skipped.
	for _, c := range sts.Spec.Template.Spec.InitContainers {
		if c.Name == "channel-inbound-only-ch" {
			t.Error("expected incompatible channel to be skipped, but sidecar was found")
		}
	}
}

// ---------------------------------------------------------------------------
// Integration test: StatefulSet update path (spec change triggers update)
// ---------------------------------------------------------------------------

func TestClawReconciler_StatefulSetUpdated(t *testing.T) {
	ns := fmt.Sprintf("test-sts-update-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-sts-upd"
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
	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, nn, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Record initial resourceVersion.
	initialRV := sts.ResourceVersion

	// Update the Claw spec: add credentials to trigger StatefulSet update.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "upd-creds",
			Namespace: ns,
		},
		Data: map[string][]byte{"KEY": []byte("val")},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("failed to create Secret: %v", err)
	}

	var latest clawv1alpha1.Claw
	if err := k8sClient.Get(ctx, nn, &latest); err != nil {
		t.Fatalf("failed to re-fetch Claw: %v", err)
	}
	latest.Spec.Credentials = &clawv1alpha1.CredentialSpec{
		SecretRef: &corev1.LocalObjectReference{Name: "upd-creds"},
	}
	if err := k8sClient.Update(ctx, &latest); err != nil {
		t.Fatalf("failed to update Claw: %v", err)
	}

	// Wait for the StatefulSet to be updated (resourceVersion changes).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		if err := k8sClient.Get(ctx, nn, &sts); err != nil {
			return false, err
		}
		return sts.ResourceVersion != initialRV, nil
	})

	// Verify the runtime container now has envFrom with the secret ref.
	var runtimeContainer *corev1.Container
	for i := range sts.Spec.Template.Spec.Containers {
		if sts.Spec.Template.Spec.Containers[i].Name == "runtime" {
			runtimeContainer = &sts.Spec.Template.Spec.Containers[i]
			break
		}
	}
	if runtimeContainer == nil {
		t.Fatal("expected runtime container, not found")
	}

	found := false
	for _, ef := range runtimeContainer.EnvFrom {
		if ef.SecretRef != nil && ef.SecretRef.Name == "upd-creds" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected envFrom with secretRef 'upd-creds' after update, not found")
	}
}

// ---------------------------------------------------------------------------
// Integration test: Different runtimes produce correct StatefulSets
// ---------------------------------------------------------------------------

func TestClawReconciler_AllRuntimes(t *testing.T) {
	runtimes := []struct {
		name        clawv1alpha1.RuntimeType
		gatewayPort int32
		image       string
		gracePeriod int64
	}{
		{clawv1alpha1.RuntimeNanoClaw, 19000, "ghcr.io/prismer-ai/k8s4claw-nanoclaw:latest", 25},
		{clawv1alpha1.RuntimeZeroClaw, 3000, "ghcr.io/prismer-ai/k8s4claw-zeroclaw:latest", 15},
		{clawv1alpha1.RuntimePicoClaw, 8080, "ghcr.io/prismer-ai/k8s4claw-picoclaw:latest", 12},
	}

	for _, rt := range runtimes {
		t.Run(string(rt.name), func(t *testing.T) {
			ns := fmt.Sprintf("test-rt-%s-%d", rt.name, time.Now().UnixNano())
			createNamespace(t, ns)

			clawName := fmt.Sprintf("test-%s", rt.name)
			claw := &clawv1alpha1.Claw{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clawName,
					Namespace: ns,
				},
				Spec: clawv1alpha1.ClawSpec{
					Runtime: rt.name,
				},
			}
			if err := k8sClient.Create(ctx, claw); err != nil {
				t.Fatalf("failed to create Claw: %v", err)
			}

			// Wait for StatefulSet.
			nn := types.NamespacedName{Name: clawName, Namespace: ns}
			var sts appsv1.StatefulSet
			waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
				err := k8sClient.Get(ctx, nn, &sts)
				if err != nil {
					if client.IgnoreNotFound(err) == nil {
						return false, nil
					}
					return false, err
				}
				return true, nil
			})

			// Verify runtime image.
			var runtimeContainer *corev1.Container
			for i := range sts.Spec.Template.Spec.Containers {
				if sts.Spec.Template.Spec.Containers[i].Name == "runtime" {
					runtimeContainer = &sts.Spec.Template.Spec.Containers[i]
					break
				}
			}
			if runtimeContainer == nil {
				t.Fatal("runtime container not found")
			}
			if runtimeContainer.Image != rt.image {
				t.Errorf("expected image=%q, got %q", rt.image, runtimeContainer.Image)
			}

			// Verify terminationGracePeriodSeconds.
			if sts.Spec.Template.Spec.TerminationGracePeriodSeconds == nil ||
				*sts.Spec.Template.Spec.TerminationGracePeriodSeconds != rt.gracePeriod {
				t.Errorf("expected terminationGracePeriodSeconds=%d, got %v",
					rt.gracePeriod, sts.Spec.Template.Spec.TerminationGracePeriodSeconds)
			}

			// Verify runtime label.
			if sts.Labels["claw.prismer.ai/runtime"] != string(rt.name) {
				t.Errorf("expected runtime label=%q, got %q", rt.name, sts.Labels["claw.prismer.ai/runtime"])
			}

			// Wait for ConfigMap and verify gatewayPort.
			cmName := fmt.Sprintf("%s-config", clawName)
			var cm corev1.ConfigMap
			waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: ns}, &cm)
				if err != nil {
					if client.IgnoreNotFound(err) == nil {
						return false, nil
					}
					return false, err
				}
				return true, nil
			})

			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(cm.Data["config.json"]), &parsed); err != nil {
				t.Fatalf("config.json is not valid JSON: %v", err)
			}
			if gp, ok := parsed["gatewayPort"]; !ok || int(gp.(float64)) != int(rt.gatewayPort) {
				t.Errorf("expected gatewayPort=%d, got %v", rt.gatewayPort, parsed["gatewayPort"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Integration test: PVC reclaim Archive (stub behavior — should retain)
// ---------------------------------------------------------------------------

func TestClawReconciler_PVCReclaimArchive(t *testing.T) {
	ns := fmt.Sprintf("test-reclaim-arc-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-reclaim-arc"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				ReclaimPolicy: clawv1alpha1.ReclaimArchive,
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

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

	// Pre-create a labeled PVC.
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("session-%s-0", clawName),
			Namespace: ns,
			Labels:    map[string]string{"claw.prismer.ai/instance": clawName},
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

	// Delete the Claw.
	if err := k8sClient.Get(ctx, nn, &latest); err != nil {
		t.Fatalf("failed to re-fetch Claw: %v", err)
	}
	if err := k8sClient.Delete(ctx, &latest); err != nil {
		t.Fatalf("failed to delete Claw: %v", err)
	}

	// Wait for the Claw to be fully deleted.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, nn, &clawv1alpha1.Claw{})
		if client.IgnoreNotFound(err) == nil {
			return true, nil
		}
		return false, err
	})

	// Verify PVC still exists (Archive stub retains PVCs).
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      pvc.Name,
		Namespace: ns,
	}, &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("expected PVC to be retained with Archive policy stub, but got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Integration test: ConfigMap update path (spec change triggers update)
// ---------------------------------------------------------------------------

func TestClawReconciler_ConfigMapUpdated(t *testing.T) {
	ns := fmt.Sprintf("test-cm-upd-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-cm-upd"
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

	// Wait for ConfigMap to be created.
	cmName := fmt.Sprintf("%s-config", clawName)
	nn := types.NamespacedName{Name: cmName, Namespace: ns}
	var cm corev1.ConfigMap
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, nn, &cm)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	initialRV := cm.ResourceVersion

	// Update the Claw's config to trigger ConfigMap update path.
	var latest clawv1alpha1.Claw
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &latest); err != nil {
		t.Fatalf("failed to re-fetch Claw: %v", err)
	}
	latest.Spec.Config = &apiextensionsv1.JSON{Raw: []byte(`{"model":"claude-sonnet-4"}`)}
	if err := k8sClient.Update(ctx, &latest); err != nil {
		t.Fatalf("failed to update Claw: %v", err)
	}

	// Wait for ConfigMap to be updated (resourceVersion changes).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		if err := k8sClient.Get(ctx, nn, &cm); err != nil {
			return false, err
		}
		return cm.ResourceVersion != initialRV, nil
	})

	// Verify the ConfigMap now has the user config merged in.
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(cm.Data["config.json"]), &parsed); err != nil {
		t.Fatalf("config.json is not valid JSON: %v", err)
	}
	if parsed["model"] != "claude-sonnet-4" {
		t.Errorf("expected model=claude-sonnet-4, got %v", parsed["model"])
	}
	// Default gatewayPort should still be present.
	if gp, ok := parsed["gatewayPort"]; !ok || int(gp.(float64)) != 18900 {
		t.Errorf("expected gatewayPort=18900, got %v", parsed["gatewayPort"])
	}
}

// ---------------------------------------------------------------------------
// Integration test: Custom channel with full sidecar spec
// ---------------------------------------------------------------------------

func TestClawReconciler_CustomChannelSidecar(t *testing.T) {
	ns := fmt.Sprintf("test-ch-custom-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a custom ClawChannel with sidecar spec.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "custom-adapter",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeCustom,
			Mode: clawv1alpha1.ChannelModeBidirectional,
			Sidecar: &clawv1alpha1.SidecarSpec{
				Image: "my-registry/custom-adapter:v1",
				Env: []corev1.EnvVar{
					{Name: "CUSTOM_VAR", Value: "custom_value"},
				},
				Ports: []corev1.ContainerPort{
					{Name: "http", ContainerPort: 9090, Protocol: corev1.ProtocolTCP},
				},
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Create a Claw referencing the custom channel.
	clawName := "test-claw-custom-ch"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "custom-adapter", Mode: clawv1alpha1.ChannelModeBidirectional},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for StatefulSet.
	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Find the custom channel sidecar in initContainers.
	var sidecar *corev1.Container
	for i := range sts.Spec.Template.Spec.InitContainers {
		if sts.Spec.Template.Spec.InitContainers[i].Name == "channel-custom-adapter" {
			sidecar = &sts.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if sidecar == nil {
		t.Fatal("expected channel sidecar 'channel-custom-adapter' in initContainers, not found")
	}

	// Verify custom image.
	if sidecar.Image != "my-registry/custom-adapter:v1" {
		t.Errorf("expected custom image, got %q", sidecar.Image)
	}

	// Verify custom env var is present.
	envMap := make(map[string]string)
	for _, e := range sidecar.Env {
		envMap[e.Name] = e.Value
	}
	if envMap["CUSTOM_VAR"] != "custom_value" {
		t.Errorf("expected CUSTOM_VAR=custom_value, got %q", envMap["CUSTOM_VAR"])
	}
	if envMap["CHANNEL_TYPE"] != "custom" {
		t.Errorf("expected CHANNEL_TYPE=custom, got %q", envMap["CHANNEL_TYPE"])
	}

	// Verify custom port.
	if len(sidecar.Ports) != 1 || sidecar.Ports[0].ContainerPort != 9090 {
		t.Errorf("expected port 9090, got %v", sidecar.Ports)
	}

	// Verify custom resources override default.
	if sidecar.Resources.Requests.Cpu().String() != "100m" {
		t.Errorf("expected CPU request 100m, got %s", sidecar.Resources.Requests.Cpu().String())
	}
}

// ---------------------------------------------------------------------------
// Integration test: Channel with credentials (envFrom in sidecar)
// ---------------------------------------------------------------------------

func TestClawReconciler_ChannelWithCredentials(t *testing.T) {
	ns := fmt.Sprintf("test-ch-cred-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a secret for the channel.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "slack-creds",
			Namespace: ns,
		},
		Data: map[string][]byte{"SLACK_TOKEN": []byte("xoxb-test")},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("failed to create Secret: %v", err)
	}

	// Create a ClawChannel with credentials.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cred-slack",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeSlack,
			Mode: clawv1alpha1.ChannelModeBidirectional,
			Credentials: &clawv1alpha1.CredentialSpec{
				SecretRef: &corev1.LocalObjectReference{Name: "slack-creds"},
			},
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Create a Claw referencing the channel.
	clawName := "test-claw-ch-cred"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "cred-slack", Mode: clawv1alpha1.ChannelModeBidirectional},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for StatefulSet.
	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Find the channel sidecar.
	var sidecar *corev1.Container
	for i := range sts.Spec.Template.Spec.InitContainers {
		if sts.Spec.Template.Spec.InitContainers[i].Name == "channel-cred-slack" {
			sidecar = &sts.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if sidecar == nil {
		t.Fatal("expected channel sidecar 'channel-cred-slack', not found")
	}

	// Verify envFrom with the secret ref.
	found := false
	for _, ef := range sidecar.EnvFrom {
		if ef.SecretRef != nil && ef.SecretRef.Name == "slack-creds" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected envFrom with secretRef 'slack-creds' on channel sidecar, not found")
	}
}

// ---------------------------------------------------------------------------
// Integration test: Channel with config (CHANNEL_CONFIG env var)
// ---------------------------------------------------------------------------

func TestClawReconciler_ChannelWithConfig(t *testing.T) {
	ns := fmt.Sprintf("test-ch-cfg-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	configJSON := `{"webhook_url":"https://example.com/hook","timeout":30}`
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cfg-webhook",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type:   clawv1alpha1.ChannelTypeWebhook,
			Mode:   clawv1alpha1.ChannelModeBidirectional,
			Config: &apiextensionsv1.JSON{Raw: []byte(configJSON)},
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	clawName := "test-claw-ch-cfg"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "cfg-webhook", Mode: clawv1alpha1.ChannelModeBidirectional},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Find the channel sidecar.
	var sidecar *corev1.Container
	for i := range sts.Spec.Template.Spec.InitContainers {
		if sts.Spec.Template.Spec.InitContainers[i].Name == "channel-cfg-webhook" {
			sidecar = &sts.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if sidecar == nil {
		t.Fatal("expected channel sidecar 'channel-cfg-webhook', not found")
	}

	// Verify CHANNEL_CONFIG env var is present and contains the config.
	envMap := make(map[string]string)
	for _, e := range sidecar.Env {
		envMap[e.Name] = e.Value
	}
	channelCfg, ok := envMap["CHANNEL_CONFIG"]
	if !ok {
		t.Fatal("expected CHANNEL_CONFIG env var, not found")
	}
	if channelCfg == "" {
		t.Error("expected non-empty CHANNEL_CONFIG")
	}
}

// ---------------------------------------------------------------------------
// Integration test: Credential injection with both secretRef AND keys
// ---------------------------------------------------------------------------

func TestClawReconciler_CredentialBothSecretRefAndKeys(t *testing.T) {
	ns := fmt.Sprintf("test-cred-both-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a Secret.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "both-creds",
			Namespace: ns,
		},
		Data: map[string][]byte{
			"API_KEY":    []byte("key-123"),
			"API_SECRET": []byte("secret-456"),
		},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("failed to create Secret: %v", err)
	}

	clawName := "test-claw-cred-both"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Credentials: &clawv1alpha1.CredentialSpec{
				SecretRef: &corev1.LocalObjectReference{Name: "both-creds"},
				Keys: []clawv1alpha1.KeyMapping{
					{
						Name: "MAPPED_KEY",
						SecretKeyRef: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "both-creds"},
							Key:                  "API_SECRET",
						},
					},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Find runtime container.
	var runtimeContainer *corev1.Container
	for i := range sts.Spec.Template.Spec.Containers {
		if sts.Spec.Template.Spec.Containers[i].Name == "runtime" {
			runtimeContainer = &sts.Spec.Template.Spec.Containers[i]
			break
		}
	}
	if runtimeContainer == nil {
		t.Fatal("expected runtime container, not found")
	}

	// Verify envFrom (from secretRef).
	foundEnvFrom := false
	for _, ef := range runtimeContainer.EnvFrom {
		if ef.SecretRef != nil && ef.SecretRef.Name == "both-creds" {
			foundEnvFrom = true
			break
		}
	}
	if !foundEnvFrom {
		t.Error("expected envFrom with secretRef 'both-creds', not found")
	}

	// Verify per-key env var (from keys mapping).
	foundKeyMapping := false
	for _, env := range runtimeContainer.Env {
		if env.Name == "MAPPED_KEY" &&
			env.ValueFrom != nil &&
			env.ValueFrom.SecretKeyRef != nil &&
			env.ValueFrom.SecretKeyRef.Name == "both-creds" &&
			env.ValueFrom.SecretKeyRef.Key == "API_SECRET" {
			foundKeyMapping = true
			break
		}
	}
	if !foundKeyMapping {
		t.Error("expected env var MAPPED_KEY with secretKeyRef, not found")
	}

	// Verify secret hash annotation is present (from secretRef path).
	hash, ok := sts.Spec.Template.Annotations["claw.prismer.ai/secret-hash"]
	if !ok || hash == "" {
		t.Error("expected non-empty secret-hash annotation")
	}
}

// ---------------------------------------------------------------------------
// Integration test: findClawsForChannel maps channel changes to Claws
// ---------------------------------------------------------------------------

func TestClawReconciler_FindClawsForChannel(t *testing.T) {
	ns := fmt.Sprintf("test-find-ch-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a ClawChannel.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "watched-channel",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeWebhook,
			Mode: clawv1alpha1.ChannelModeBidirectional,
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Create a Claw referencing the channel.
	clawName := "test-claw-watch"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "watched-channel", Mode: clawv1alpha1.ChannelModeBidirectional},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for StatefulSet to confirm Claw is reconciled.
	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	initialRV := sts.ResourceVersion

	// Update the ClawChannel spec to trigger the Watches handler.
	var latestChannel clawv1alpha1.ClawChannel
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "watched-channel", Namespace: ns}, &latestChannel); err != nil {
		t.Fatalf("failed to get channel: %v", err)
	}
	latestChannel.Spec.Mode = clawv1alpha1.ChannelModeOutbound
	if err := k8sClient.Update(ctx, &latestChannel); err != nil {
		t.Fatalf("failed to update channel: %v", err)
	}

	// The Watches handler should map the channel change to the Claw, triggering re-reconcile.
	// Wait for the StatefulSet to be re-reconciled (resourceVersion changes).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts); err != nil {
			return false, err
		}
		return sts.ResourceVersion != initialRV, nil
	})
}

// ---------------------------------------------------------------------------
// Integration test: Channel with custom resources override
// ---------------------------------------------------------------------------

func TestClawReconciler_ChannelWithCustomResources(t *testing.T) {
	ns := fmt.Sprintf("test-ch-res-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "res-channel",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeWebhook,
			Mode: clawv1alpha1.ChannelModeBidirectional,
			Resources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	clawName := "test-claw-ch-res"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "res-channel", Mode: clawv1alpha1.ChannelModeBidirectional},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Find the channel sidecar.
	var sidecar *corev1.Container
	for i := range sts.Spec.Template.Spec.InitContainers {
		if sts.Spec.Template.Spec.InitContainers[i].Name == "channel-res-channel" {
			sidecar = &sts.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if sidecar == nil {
		t.Fatal("expected channel sidecar 'channel-res-channel', not found")
	}

	// Verify custom resources were applied.
	if sidecar.Resources.Requests.Cpu().String() != "250m" {
		t.Errorf("expected CPU request 250m, got %s", sidecar.Resources.Requests.Cpu().String())
	}
	if sidecar.Resources.Limits.Memory().String() != "512Mi" {
		t.Errorf("expected memory limit 512Mi, got %s", sidecar.Resources.Limits.Memory().String())
	}
}

// ---------------------------------------------------------------------------
// Integration test: ClawChannel deletion proceeds when no references
// ---------------------------------------------------------------------------

func TestClawChannelReconciler_DeletionNoReferences(t *testing.T) {
	ns := fmt.Sprintf("test-ch-delok-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a ClawChannel with NO Claws referencing it.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-channel-delok",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeWebhook,
			Mode: clawv1alpha1.ChannelModeBidirectional,
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Wait for the finalizer to be added.
	nn := types.NamespacedName{Name: channel.Name, Namespace: channel.Namespace}
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.ClawChannel
		if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return controllerutil.ContainsFinalizer(&fetched, clawChannelFinalizer), nil
	})

	// Delete the channel.
	var latest clawv1alpha1.ClawChannel
	if err := k8sClient.Get(ctx, nn, &latest); err != nil {
		t.Fatalf("failed to get channel: %v", err)
	}
	if err := k8sClient.Delete(ctx, &latest); err != nil {
		t.Fatalf("failed to delete channel: %v", err)
	}

	// Wait for the channel to be fully deleted (finalizer removed, no references).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, nn, &clawv1alpha1.ClawChannel{})
		if client.IgnoreNotFound(err) == nil {
			return true, nil
		}
		return false, err
	})
}

// ===========================================================================
// Unit tests: error paths using fake client + interceptor
// ===========================================================================

// newFakeReconciler builds a ClawReconciler with the given client for unit testing.
func newFakeReconciler(c client.Client) *ClawReconciler {
	registry := clawruntime.NewRegistry()
	registry.Register(clawv1alpha1.RuntimeOpenClaw, &clawruntime.OpenClawAdapter{})
	registry.Register(clawv1alpha1.RuntimeNanoClaw, &clawruntime.NanoClawAdapter{})
	registry.Register(clawv1alpha1.RuntimeZeroClaw, &clawruntime.ZeroClawAdapter{})
	registry.Register(clawv1alpha1.RuntimePicoClaw, &clawruntime.PicoClawAdapter{})
	return &ClawReconciler{
		Client:                c,
		Scheme:                scheme.Scheme,
		Registry:              registry,
		NativeSidecarsEnabled: true,
	}
}

func TestReconcile_ClawNotFound(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := newFakeReconciler(fakeClient)

	// Reconcile a non-existent Claw — should return nil (IgnoreNotFound).
	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("expected nil error for not-found Claw, got: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue for not-found Claw")
	}
}

func TestEnsureConfigMap_GetError(t *testing.T) {
	errSimulated := errors.New("simulated get error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureConfigMap(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureConfigMap, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

func TestEnsureConfigMap_CreateError(t *testing.T) {
	errSimulated := errors.New("simulated create error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok {
				return errSimulated
			}
			return c.Create(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureConfigMap(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureConfigMap Create, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated create error, got: %v", err)
	}
}

func TestEnsureConfigMap_UpdateError(t *testing.T) {
	errSimulated := errors.New("simulated update error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	// Pre-create a ConfigMap so the update path is triggered.
	existingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config", Namespace: "default"},
		Data:       map[string]string{"config.json": "{}"},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(existingCM).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok {
				return errSimulated
			}
			return c.Update(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	err := r.ensureConfigMap(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureConfigMap Update, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated update error, got: %v", err)
	}
}

func TestEnsureService_GetError(t *testing.T) {
	errSimulated := errors.New("simulated service get error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Service); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureService(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureService, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

func TestEnsureService_UpdateError(t *testing.T) {
	errSimulated := errors.New("simulated service update error")

	// Pre-create a Service so the update path is triggered.
	existingSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(existingSvc).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*corev1.Service); ok {
				return errSimulated
			}
			return c.Update(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureService(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureService Update, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated update error, got: %v", err)
	}
}

func TestEnsureStatefulSet_GetError(t *testing.T) {
	errSimulated := errors.New("simulated sts get error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*appsv1.StatefulSet); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureStatefulSet(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureStatefulSet, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

func TestEnsureStatefulSet_UpdateError(t *testing.T) {
	errSimulated := errors.New("simulated sts update error")

	existingSts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(existingSts).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*appsv1.StatefulSet); ok {
				return errSimulated
			}
			return c.Update(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureStatefulSet(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureStatefulSet Update, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated update error, got: %v", err)
	}
}

func TestUpdateStatus_GetError(t *testing.T) {
	errSimulated := errors.New("simulated status get error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*appsv1.StatefulSet); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.updateStatus(ctx, claw)
	if err == nil {
		t.Fatal("expected error from updateStatus, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

func TestDeleteClawPVCs_ListError(t *testing.T) {
	errSimulated := errors.New("simulated list error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			return errSimulated
		},
	})

	r := newFakeReconciler(wrappedClient)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
	}

	err := r.deleteClawPVCs(ctx, claw)
	if err == nil {
		t.Fatal("expected error from deleteClawPVCs, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

func TestDeleteClawPVCs_DeleteError(t *testing.T) {
	errSimulated := errors.New("simulated delete error")

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc-0",
			Namespace: "default",
			Labels:    map[string]string{"claw.prismer.ai/instance": "test"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(pvc).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			return errSimulated
		},
	})

	r := newFakeReconciler(wrappedClient)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
	}

	err := r.deleteClawPVCs(ctx, claw)
	if err == nil {
		t.Fatal("expected error from deleteClawPVCs Delete, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated delete error, got: %v", err)
	}
}

func TestHandleDeletion_NoFinalizer(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := newFakeReconciler(fakeClient)

	// Create a Claw with DeletionTimestamp but no finalizer.
	now := metav1.Now()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{}, // No finalizer.
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	result, err := r.handleDeletion(ctx, claw)
	if err != nil {
		t.Fatalf("expected nil error for handleDeletion without finalizer, got: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue")
	}
}

func TestEnsureStatefulSet_CreateError(t *testing.T) {
	errSimulated := errors.New("simulated sts create error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*appsv1.StatefulSet); ok {
				return errSimulated
			}
			return c.Create(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureStatefulSet(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureStatefulSet Create, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated create error, got: %v", err)
	}
}

func TestReconcile_EnsureServiceError(t *testing.T) {
	errSimulated := errors.New("simulated reconcile service error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-reconcile-svc", Namespace: "default",
			UID: "test-uid", Generation: 1,
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		WithStatusSubresource(claw).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Service); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-reconcile-svc", Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected error from Reconcile, got nil")
	}
}

func TestReconcile_EnsureConfigMapError(t *testing.T) {
	errSimulated := errors.New("simulated reconcile cm error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-reconcile-cm", Namespace: "default",
			UID: "test-uid-2", Generation: 1,
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		WithStatusSubresource(claw).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-reconcile-cm", Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected error from Reconcile, got nil")
	}
}

func TestReconcile_EnsureStatefulSetError(t *testing.T) {
	errSimulated := errors.New("simulated reconcile sts error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-reconcile-sts", Namespace: "default",
			UID: "test-uid-3", Generation: 1,
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		WithStatusSubresource(claw).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*appsv1.StatefulSet); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-reconcile-sts", Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected error from Reconcile, got nil")
	}
}

func TestReconcile_UpdateStatusError(t *testing.T) {
	errSimulated := errors.New("simulated status update error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-reconcile-status", Namespace: "default",
			UID: "test-uid-4", Generation: 1,
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		WithStatusSubresource(claw).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			return errSimulated
		},
	})

	r := newFakeReconciler(wrappedClient)

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-reconcile-status", Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected error from Reconcile status update, got nil")
	}
}

func TestEnsureService_CreateError(t *testing.T) {
	errSimulated := errors.New("simulated service create error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*corev1.Service); ok {
				return errSimulated
			}
			return c.Create(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureService(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureService Create, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated create error, got: %v", err)
	}
}

// ===========================================================================
// Phase 3: Additional coverage tests targeting remaining uncovered blocks
// ===========================================================================

// newFakeChannelReconciler builds a ClawChannelReconciler with the given client for unit testing.
func newFakeChannelReconciler(c client.Client) *ClawChannelReconciler {
	return &ClawChannelReconciler{
		Client: c,
		Scheme: scheme.Scheme,
	}
}

// ---------------------------------------------------------------------------
// channel_controller.go error paths
// ---------------------------------------------------------------------------

func TestChannelReconcile_FindReferencingClawsError(t *testing.T) {
	errSimulated := errors.New("simulated list error")

	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{Name: "ch-err", Namespace: "default", UID: "ch-uid-1"},
		Spec:       clawv1alpha1.ClawChannelSpec{Type: clawv1alpha1.ChannelTypeWebhook},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(channel).
		WithStatusSubresource(channel).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*clawv1alpha1.ClawList); ok {
				return errSimulated
			}
			return c.List(ctx, list, opts...)
		},
	})

	r := newFakeChannelReconciler(wrappedClient)
	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ch-err", Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected error from channel Reconcile, got nil")
	}
}

func TestChannelReconcile_AddFinalizerPatchError(t *testing.T) {
	errSimulated := errors.New("simulated patch error")

	// Channel without finalizer → Reconcile will try to add it.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{Name: "ch-patch", Namespace: "default", UID: "ch-uid-2"},
		Spec:       clawv1alpha1.ClawChannelSpec{Type: clawv1alpha1.ChannelTypeWebhook},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(channel).
		WithStatusSubresource(channel).
		WithIndex(&clawv1alpha1.Claw{}, ChannelNameIndexField, func(obj client.Object) []string {
			claw, ok := obj.(*clawv1alpha1.Claw)
			if !ok {
				return nil
			}
			names := make([]string, 0, len(claw.Spec.Channels))
			for _, ch := range claw.Spec.Channels {
				names = append(names, ch.Name)
			}
			return names
		}).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			return errSimulated
		},
	})

	r := newFakeChannelReconciler(wrappedClient)
	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ch-patch", Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected error from channel Reconcile Patch, got nil")
	}
}

func TestHandleChannelDeletion_NoFinalizer(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := newFakeChannelReconciler(fakeClient)

	now := metav1.Now()
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "ch-nofin",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{}, // No finalizer.
		},
	}

	result, err := r.handleChannelDeletion(ctx, channel, nil)
	if err != nil {
		t.Fatalf("expected nil error for channel deletion without finalizer, got: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue")
	}
}

func TestHandleChannelDeletion_StatusUpdateError(t *testing.T) {
	errSimulated := errors.New("simulated status update error")

	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "ch-status-err",
			Namespace:  "default",
			UID:        "ch-uid-3",
			Finalizers: []string{clawChannelFinalizer},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(channel).
		WithStatusSubresource(channel).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			return errSimulated
		},
	})

	r := newFakeChannelReconciler(wrappedClient)

	now := metav1.Now()
	channel.DeletionTimestamp = &now

	// Still referenced by a Claw → tries to update status.
	_, err := r.handleChannelDeletion(ctx, channel, []string{"my-claw"})
	if err == nil {
		t.Fatal("expected error from handleChannelDeletion status update, got nil")
	}
}

func TestHandleChannelDeletion_RemoveFinalizerPatchError(t *testing.T) {
	errSimulated := errors.New("simulated patch error")

	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "ch-rm-fin",
			Namespace:  "default",
			UID:        "ch-uid-4",
			Finalizers: []string{clawChannelFinalizer},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(channel).
		WithStatusSubresource(channel).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			return errSimulated
		},
	})

	r := newFakeChannelReconciler(wrappedClient)

	now := metav1.Now()
	channel.DeletionTimestamp = &now

	// No references → tries to remove finalizer.
	_, err := r.handleChannelDeletion(ctx, channel, nil)
	if err == nil {
		t.Fatal("expected error from handleChannelDeletion Patch, got nil")
	}
}

// ---------------------------------------------------------------------------
// channel_index.go: clawsReferencingChannel List error
// ---------------------------------------------------------------------------

func TestClawsReferencingChannel_ListError(t *testing.T) {
	errSimulated := errors.New("simulated list error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			return errSimulated
		},
	})

	_, err := clawsReferencingChannel(ctx, wrappedClient, "default", "some-channel")
	if err == nil {
		t.Fatal("expected error from clawsReferencingChannel, got nil")
	}
}

// ---------------------------------------------------------------------------
// updateStatus: Running phase (ReadyReplicas >= 1)
// ---------------------------------------------------------------------------

func TestUpdateStatus_RunningPhase(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-running", Namespace: "default", UID: "uid-run", Generation: 2},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	// Pre-create a StatefulSet with ReadyReplicas=1 to trigger Running phase.
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-running", Namespace: "default"},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: 1,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw, sts).
		WithStatusSubresource(claw).
		Build()

	r := newFakeReconciler(fakeClient)

	if err := r.updateStatus(ctx, claw); err != nil {
		t.Fatalf("unexpected error from updateStatus: %v", err)
	}

	if claw.Status.Phase != clawv1alpha1.ClawPhaseRunning {
		t.Errorf("expected phase Running, got %s", claw.Status.Phase)
	}

	// Verify RuntimeReady condition is True.
	found := false
	for _, c := range claw.Status.Conditions {
		if c.Type == "RuntimeReady" {
			found = true
			if c.Status != metav1.ConditionTrue {
				t.Errorf("expected RuntimeReady=True, got %s", c.Status)
			}
			if c.Reason != "StatefulSetReady" {
				t.Errorf("expected reason StatefulSetReady, got %s", c.Reason)
			}
		}
	}
	if !found {
		t.Error("RuntimeReady condition not found")
	}
}

// ---------------------------------------------------------------------------
// findClawsForChannel error paths
// ---------------------------------------------------------------------------

func TestFindClawsForChannel_TypeAssertionFail(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := newFakeReconciler(fakeClient)

	// Pass a non-ClawChannel object → type assertion fails → return nil.
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "not-a-channel", Namespace: "default"}}
	result := r.findClawsForChannel(ctx, svc)
	if result != nil {
		t.Errorf("expected nil for non-ClawChannel object, got %v", result)
	}
}

func TestFindClawsForChannel_ListError(t *testing.T) {
	errSimulated := errors.New("simulated list error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*clawv1alpha1.ClawList); ok {
				return errSimulated
			}
			return c.List(ctx, list, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)

	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ch", Namespace: "default"},
	}

	result := r.findClawsForChannel(ctx, channel)
	if result != nil {
		t.Errorf("expected nil on list error, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// channel_sidecar.go: NativeSidecarsEnabled=false path
// ---------------------------------------------------------------------------

func TestInjectChannelSidecars_NonNativeSidecar(t *testing.T) {
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{Name: "ch-nonnative", Namespace: "default"},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeSlack,
			Mode: clawv1alpha1.ChannelModeBidirectional,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(channel).
		Build()

	r := newFakeReconciler(fakeClient)
	r.NativeSidecarsEnabled = false // Force non-native path.

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-nonnative", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "ch-nonnative", Mode: clawv1alpha1.ChannelModeInbound},
			},
		},
	}

	podTemplate := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "runtime", Image: "test:latest"},
			},
		},
	}

	skipped, err := r.injectChannelSidecars(ctx, claw, podTemplate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("expected 0 skipped, got %d", len(skipped))
	}

	// Sidecar should be in Containers (not InitContainers) when NativeSidecarsEnabled=false.
	if len(podTemplate.Spec.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(podTemplate.Spec.Containers))
	}
	if podTemplate.Spec.Containers[1].Name != "channel-ch-nonnative" {
		t.Errorf("expected sidecar name channel-ch-nonnative, got %s", podTemplate.Spec.Containers[1].Name)
	}
	// InitContainers should be empty.
	if len(podTemplate.Spec.InitContainers) != 0 {
		t.Errorf("expected 0 init containers for non-native path, got %d", len(podTemplate.Spec.InitContainers))
	}
}

func TestInjectChannelSidecars_GetNonNotFoundError(t *testing.T) {
	errSimulated := errors.New("simulated channel get error")

	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*clawv1alpha1.ClawChannel); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ch-err", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "bad-channel", Mode: clawv1alpha1.ChannelModeInbound},
			},
		},
	}

	podTemplate := &corev1.PodTemplateSpec{}

	_, err := r.injectChannelSidecars(ctx, claw, podTemplate)
	if err == nil {
		t.Fatal("expected error from injectChannelSidecars non-NotFound Get, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// claw_credentials.go edge cases
// ---------------------------------------------------------------------------

func TestInjectCredentials_NilCredentials(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := newFakeReconciler(fakeClient)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-nil-creds", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: nil, // Explicitly nil.
		},
	}

	podTemplate := &corev1.PodTemplateSpec{}

	err := r.injectCredentials(ctx, claw, podTemplate)
	if err != nil {
		t.Fatalf("expected nil error for nil credentials, got: %v", err)
	}
}

func TestInjectCredentials_NoRuntimeContainer(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := newFakeReconciler(fakeClient)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-no-runtime", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Credentials: &clawv1alpha1.CredentialSpec{
				SecretRef: &corev1.LocalObjectReference{Name: "my-secret"},
			},
		},
	}

	// Pod template with a container named "other" instead of "runtime".
	podTemplate := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "other", Image: "test:latest"},
			},
		},
	}

	err := r.injectCredentials(ctx, claw, podTemplate)
	if err == nil {
		t.Fatal("expected error for missing runtime container, got nil")
	}
	if err.Error() != "runtime container not found in pod template" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestComputeSecretHash_GetError(t *testing.T) {
	errSimulated := errors.New("simulated secret get error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)

	_, err := r.computeSecretHash(ctx, "default", "nonexistent-secret")
	if err == nil {
		t.Fatal("expected error from computeSecretHash, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

func TestInjectCredentials_SecretHashError(t *testing.T) {
	errSimulated := errors.New("simulated secret get error")

	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-hash-err", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Credentials: &clawv1alpha1.CredentialSpec{
				SecretRef: &corev1.LocalObjectReference{Name: "my-secret"},
			},
		},
	}

	podTemplate := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "runtime", Image: "test:latest"},
			},
		},
	}

	err := r.injectCredentials(ctx, claw, podTemplate)
	if err == nil {
		t.Fatal("expected error from injectCredentials secret hash, got nil")
	}
}

// ---------------------------------------------------------------------------
// SetControllerReference errors (using scheme without clawv1alpha1)
// ---------------------------------------------------------------------------

func incompleteSchemeReconciler(c client.Client) *ClawReconciler {
	incompleteScheme := apimachineryruntime.NewScheme()
	_ = corev1.AddToScheme(incompleteScheme)
	_ = appsv1.AddToScheme(incompleteScheme)
	// Intentionally do NOT register clawv1alpha1 → SetControllerReference will fail.

	registry := clawruntime.NewRegistry()
	registry.Register(clawv1alpha1.RuntimeOpenClaw, &clawruntime.OpenClawAdapter{})
	return &ClawReconciler{
		Client:                c,
		Scheme:                incompleteScheme,
		Registry:              registry,
		NativeSidecarsEnabled: true,
	}
}

func TestEnsureConfigMap_SetControllerReferenceError(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := incompleteSchemeReconciler(fakeClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ref", Namespace: "default", UID: "uid-ref"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureConfigMap(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from SetControllerReference on ConfigMap, got nil")
	}
	if !containsSubstring(err.Error(), "controller reference") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEnsureService_SetControllerReferenceError(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := incompleteSchemeReconciler(fakeClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ref-svc", Namespace: "default", UID: "uid-ref"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureService(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from SetControllerReference on Service, got nil")
	}
	if !containsSubstring(err.Error(), "controller reference") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEnsureStatefulSet_SetControllerReferenceError(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := incompleteSchemeReconciler(fakeClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ref-sts", Namespace: "default", UID: "uid-ref"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureStatefulSet(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from SetControllerReference on StatefulSet, got nil")
	}
	if !containsSubstring(err.Error(), "controller reference") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// buildConfigMap / mergeConfig error paths
// ---------------------------------------------------------------------------

func TestBuildConfigMap_MergeConfigError(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := newFakeReconciler(fakeClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	// Invalid JSON in user config → mergeConfig will fail.
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bad-json", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Config:  &apiextensionsv1.JSON{Raw: []byte("{{invalid json")},
		},
	}

	_, err := r.buildConfigMap(claw, adapter)
	if err == nil {
		t.Fatal("expected error from buildConfigMap with invalid JSON, got nil")
	}
	if !containsSubstring(err.Error(), "merge config") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEnsureConfigMap_BuildConfigMapError(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := newFakeReconciler(fakeClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	// Invalid JSON in user config → buildConfigMap fails → ensureConfigMap returns wrapped error.
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-build-cm-err", Namespace: "default", UID: "uid-build"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Config:  &apiextensionsv1.JSON{Raw: []byte("{{bad")},
		},
	}

	err := r.ensureConfigMap(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureConfigMap build path, got nil")
	}
	if !containsSubstring(err.Error(), "build ConfigMap") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ensureFinalizer Patch error
// ---------------------------------------------------------------------------

func TestEnsureFinalizer_PatchError(t *testing.T) {
	errSimulated := errors.New("simulated patch error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-fin-patch", Namespace: "default",
			UID: "uid-fin", Finalizers: []string{}, // No finalizer yet.
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			return errSimulated
		},
	})

	r := newFakeReconciler(wrappedClient)
	err := r.ensureFinalizer(ctx, claw)
	if err == nil {
		t.Fatal("expected error from ensureFinalizer Patch, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleDeletion error paths
// ---------------------------------------------------------------------------

func TestHandleDeletion_DeletePVCsError(t *testing.T) {
	errSimulated := errors.New("simulated list error for deletion")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-del-pvc-err", Namespace: "default",
			UID: "uid-del", Finalizers: []string{clawFinalizer},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				ReclaimPolicy: "Delete",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.PersistentVolumeClaimList); ok {
				return errSimulated
			}
			return c.List(ctx, list, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	_, err := r.handleDeletion(ctx, claw)
	if err == nil {
		t.Fatal("expected error from handleDeletion deleteClawPVCs, got nil")
	}
}

func TestHandleDeletion_RemoveFinalizerPatchError(t *testing.T) {
	errSimulated := errors.New("simulated patch error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-del-patch-err", Namespace: "default",
			UID: "uid-del-2", Finalizers: []string{clawFinalizer},
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
		// No persistence or Retain policy → skips PVC deletion → goes to remove finalizer.
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			return errSimulated
		},
	})

	r := newFakeReconciler(wrappedClient)
	_, err := r.handleDeletion(ctx, claw)
	if err == nil {
		t.Fatal("expected error from handleDeletion finalizer removal, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Reconcile: ensureFinalizer error path
// ---------------------------------------------------------------------------

func TestReconcile_EnsureFinalizerError(t *testing.T) {
	errSimulated := errors.New("simulated reconcile finalizer error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-reconcile-fin", Namespace: "default",
			UID: "uid-fin-2", Generation: 1,
			// No finalizer → Reconcile will try to add it.
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		WithStatusSubresource(claw).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			return errSimulated
		},
	})

	r := newFakeReconciler(wrappedClient)
	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-reconcile-fin", Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected error from Reconcile ensureFinalizer, got nil")
	}
}

// ---------------------------------------------------------------------------
// Reconcile: re-fetch error after StatefulSet (claw_controller.go:81-83)
// ---------------------------------------------------------------------------

func TestReconcile_RefetchError(t *testing.T) {
	errSimulated := errors.New("simulated refetch error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-refetch", Namespace: "default",
			UID: "uid-refetch", Generation: 1,
			Finalizers: []string{clawFinalizer}, // Already has finalizer.
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		WithStatusSubresource(claw).
		Build()

	// Track Get calls to Claw — fail on the second one (the re-fetch).
	clawGetCount := 0
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*clawv1alpha1.Claw); ok {
				clawGetCount++
				if clawGetCount >= 2 {
					return errSimulated
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-refetch", Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected error from Reconcile re-fetch, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated refetch error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// buildStatefulSet: injectChannelSidecars error + injectCredentials error
// ---------------------------------------------------------------------------

func TestBuildStatefulSet_InjectSidecarsError(t *testing.T) {
	errSimulated := errors.New("simulated sidecar inject error")

	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*clawv1alpha1.ClawChannel); ok {
				return errSimulated // Non-NotFound error.
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-sidecar-err", Namespace: "default", UID: "uid-se"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "bad-ch", Mode: clawv1alpha1.ChannelModeInbound},
			},
		},
	}

	_, err := r.buildStatefulSet(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from buildStatefulSet inject sidecars, got nil")
	}
}

func TestBuildStatefulSet_InjectCredentialsError(t *testing.T) {
	errSimulated := errors.New("simulated secret error")

	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cred-err", Namespace: "default", UID: "uid-ce"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Credentials: &clawv1alpha1.CredentialSpec{
				SecretRef: &corev1.LocalObjectReference{Name: "missing-secret"},
			},
		},
	}

	_, err := r.buildStatefulSet(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from buildStatefulSet inject credentials, got nil")
	}
}

// ---------------------------------------------------------------------------
// ensureStatefulSet: buildStatefulSet error (line 175-177)
// ---------------------------------------------------------------------------

func TestEnsureStatefulSet_BuildError(t *testing.T) {
	errSimulated := errors.New("simulated secret error for build")

	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-build-err", Namespace: "default", UID: "uid-be"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Credentials: &clawv1alpha1.CredentialSpec{
				SecretRef: &corev1.LocalObjectReference{Name: "missing-secret"},
			},
		},
	}

	err := r.ensureStatefulSet(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureStatefulSet build, got nil")
	}
	if !containsSubstring(err.Error(), "build StatefulSet") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// updateStatus: Pending phase (StatefulSet not found)
// ---------------------------------------------------------------------------

func TestUpdateStatus_PendingPhase(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pending", Namespace: "default", UID: "uid-pend", Generation: 1},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	// No StatefulSet exists → should be Pending.
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		WithStatusSubresource(claw).
		Build()

	r := newFakeReconciler(fakeClient)

	if err := r.updateStatus(ctx, claw); err != nil {
		t.Fatalf("unexpected error from updateStatus: %v", err)
	}

	if claw.Status.Phase != clawv1alpha1.ClawPhasePending {
		t.Errorf("expected phase Pending, got %s", claw.Status.Phase)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ===========================================================================
// ServiceAccount tests
// ===========================================================================

func TestBuildServiceAccount_Defaults(t *testing.T) {
	tests := []struct {
		name           string
		claw           *clawv1alpha1.Claw
		wantName       string
		wantAnnotation map[string]string
	}{
		{
			name: "default SA named after claw",
			claw: &clawv1alpha1.Claw{
				ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
				Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
			},
			wantName:       "my-agent",
			wantAnnotation: nil,
		},
		{
			name: "SA with annotations from spec",
			claw: &clawv1alpha1.Claw{
				ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
				Spec: clawv1alpha1.ClawSpec{
					Runtime: clawv1alpha1.RuntimeOpenClaw,
					ServiceAccount: &clawv1alpha1.ServiceAccountRef{
						Annotations: map[string]string{
							"eks.amazonaws.com/role-arn": "arn:aws:iam::role/test",
						},
					},
				},
			},
			wantName:       "my-agent",
			wantAnnotation: map[string]string{"eks.amazonaws.com/role-arn": "arn:aws:iam::role/test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sa := buildServiceAccount(tt.claw)
			if sa.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", sa.Name, tt.wantName)
			}
			if sa.Namespace != tt.claw.Namespace {
				t.Errorf("Namespace = %q, want %q", sa.Namespace, tt.claw.Namespace)
			}
			if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken != false {
				t.Error("AutomountServiceAccountToken should be false")
			}
			// Check labels.
			labels := clawLabels(tt.claw)
			for k, v := range labels {
				if sa.Labels[k] != v {
					t.Errorf("label %s = %q, want %q", k, sa.Labels[k], v)
				}
			}
			// Check annotations.
			if tt.wantAnnotation == nil {
				if sa.Annotations != nil {
					t.Errorf("expected nil annotations, got %v", sa.Annotations)
				}
			} else {
				for k, v := range tt.wantAnnotation {
					if sa.Annotations[k] != v {
						t.Errorf("annotation %s = %q, want %q", k, sa.Annotations[k], v)
					}
				}
			}
		})
	}
}

func TestServiceAccountName_UserManaged(t *testing.T) {
	tests := []struct {
		name     string
		claw     *clawv1alpha1.Claw
		wantName string
	}{
		{
			name: "nil ServiceAccount returns claw name",
			claw: &clawv1alpha1.Claw{
				ObjectMeta: metav1.ObjectMeta{Name: "agent-1"},
				Spec:       clawv1alpha1.ClawSpec{},
			},
			wantName: "agent-1",
		},
		{
			name: "empty name returns claw name",
			claw: &clawv1alpha1.Claw{
				ObjectMeta: metav1.ObjectMeta{Name: "agent-2"},
				Spec: clawv1alpha1.ClawSpec{
					ServiceAccount: &clawv1alpha1.ServiceAccountRef{Name: ""},
				},
			},
			wantName: "agent-2",
		},
		{
			name: "custom name returned as-is",
			claw: &clawv1alpha1.Claw{
				ObjectMeta: metav1.ObjectMeta{Name: "agent-3"},
				Spec: clawv1alpha1.ClawSpec{
					ServiceAccount: &clawv1alpha1.ServiceAccountRef{Name: "my-custom-sa"},
				},
			},
			wantName: "my-custom-sa",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := serviceAccountName(tt.claw)
			if got != tt.wantName {
				t.Errorf("serviceAccountName() = %q, want %q", got, tt.wantName)
			}
		})
	}
}

func TestEnsureServiceAccount_UserManaged_IsNoOp(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := newFakeReconciler(fakeClient)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:        clawv1alpha1.RuntimeOpenClaw,
			ServiceAccount: &clawv1alpha1.ServiceAccountRef{Name: "user-managed-sa"},
		},
	}

	if err := r.ensureServiceAccount(ctx, claw); err != nil {
		t.Fatalf("expected no error for user-managed SA, got: %v", err)
	}

	// Verify no SA was created.
	var sa corev1.ServiceAccount
	err := fakeClient.Get(ctx, types.NamespacedName{Name: "user-managed-sa", Namespace: "default"}, &sa)
	if err == nil {
		t.Error("expected SA not to be created for user-managed case")
	}
}

func TestEnsureServiceAccount_CreateError(t *testing.T) {
	errSimulated := errors.New("simulated SA create error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*corev1.ServiceAccount); ok {
				return errSimulated
			}
			return c.Create(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-sa", Namespace: "default", UID: "uid-2"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureServiceAccount(ctx, claw)
	if err == nil {
		t.Fatal("expected error from ensureServiceAccount Create, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated create error, got: %v", err)
	}
}

func TestEnsureServiceAccount_GetError(t *testing.T) {
	errSimulated := errors.New("simulated SA get error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ServiceAccount); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-sa-get", Namespace: "default", UID: "uid-3"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureServiceAccount(ctx, claw)
	if err == nil {
		t.Fatal("expected error from ensureServiceAccount Get, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated get error, got: %v", err)
	}
}

func TestEnsureServiceAccount_UpdateError(t *testing.T) {
	errSimulated := errors.New("simulated SA update error")

	clawUID := types.UID("uid-4")
	// Pre-create the SA with a proper ownerReference so it passes the ownership check.
	existingSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sa-upd",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "claw.prismer.ai/v1alpha1",
					Kind:       "Claw",
					Name:       "test-sa-upd",
					UID:        clawUID,
					Controller: ptr.To(true),
				},
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(existingSA).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*corev1.ServiceAccount); ok {
				return errSimulated
			}
			return c.Update(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-sa-upd", Namespace: "default", UID: clawUID},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureServiceAccount(ctx, claw)
	if err == nil {
		t.Fatal("expected error from ensureServiceAccount Update, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated update error, got: %v", err)
	}
}

func TestEnsureServiceAccount_UnownedSARejected(t *testing.T) {
	// Pre-create an SA with no ownerReferences (simulates namespace default SA).
	existingSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "conflict-sa", Namespace: "default"},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(existingSA).Build()
	r := newFakeReconciler(fakeClient)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "conflict-sa", Namespace: "default", UID: "uid-conflict"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureServiceAccount(ctx, claw)
	if err == nil {
		t.Fatal("expected error for unowned SA, got nil")
	}
	if !containsSubstring(err.Error(), "not owned by this Claw") {
		t.Errorf("expected ownership error, got: %v", err)
	}
}

func TestEnsureServiceAccount_SetControllerReferenceError(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := incompleteSchemeReconciler(fakeClient)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-sa-ref", Namespace: "default", UID: "uid-5"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureServiceAccount(ctx, claw)
	if err == nil {
		t.Fatal("expected error from SetControllerReference, got nil")
	}
	if !containsSubstring(err.Error(), "controller reference") {
		t.Errorf("expected controller reference error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: buildRole / buildRoleBinding
// ---------------------------------------------------------------------------

func TestBuildRole(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-agent",
			Namespace: "prod",
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
		},
	}

	role := buildRole(claw)

	if role.Name != "my-agent" {
		t.Errorf("expected role name=my-agent, got %q", role.Name)
	}
	if role.Namespace != "prod" {
		t.Errorf("expected namespace=prod, got %q", role.Namespace)
	}

	// Verify labels.
	if role.Labels["claw.prismer.ai/instance"] != "my-agent" {
		t.Errorf("expected instance label=my-agent, got %q", role.Labels["claw.prismer.ai/instance"])
	}

	// Verify rules.
	if len(role.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(role.Rules))
	}

	// Rule 0: get own Claw instance.
	r0 := role.Rules[0]
	if r0.APIGroups[0] != "claw.prismer.ai" {
		t.Errorf("rule 0: expected apiGroup=claw.prismer.ai, got %q", r0.APIGroups[0])
	}
	if r0.Resources[0] != "claws" {
		t.Errorf("rule 0: expected resource=claws, got %q", r0.Resources[0])
	}
	if len(r0.ResourceNames) != 1 || r0.ResourceNames[0] != "my-agent" {
		t.Errorf("rule 0: expected resourceNames=[my-agent], got %v", r0.ResourceNames)
	}
	if r0.Verbs[0] != "get" {
		t.Errorf("rule 0: expected verb=get, got %v", r0.Verbs)
	}

	// Rule 1: create/get/list ClawSelfConfigs.
	r1 := role.Rules[1]
	if r1.Resources[0] != "clawselfconfigs" {
		t.Errorf("rule 1: expected resource=clawselfconfigs, got %q", r1.Resources[0])
	}
	expectedVerbs := map[string]bool{"create": true, "get": true, "list": true}
	for _, v := range r1.Verbs {
		if !expectedVerbs[v] {
			t.Errorf("rule 1: unexpected verb %q", v)
		}
	}
}

func TestBuildRoleBinding(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-agent",
			Namespace: "prod",
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
		},
	}

	rb := buildRoleBinding(claw)

	if rb.Name != "my-agent" {
		t.Errorf("expected binding name=my-agent, got %q", rb.Name)
	}
	if rb.RoleRef.Kind != "Role" || rb.RoleRef.Name != "my-agent" {
		t.Errorf("expected roleRef Kind=Role Name=my-agent, got Kind=%q Name=%q", rb.RoleRef.Kind, rb.RoleRef.Name)
	}
	if rb.RoleRef.APIGroup != "rbac.authorization.k8s.io" {
		t.Errorf("expected roleRef apiGroup=rbac.authorization.k8s.io, got %q", rb.RoleRef.APIGroup)
	}
	if len(rb.Subjects) != 1 {
		t.Fatalf("expected 1 subject, got %d", len(rb.Subjects))
	}
	subj := rb.Subjects[0]
	if subj.Kind != "ServiceAccount" || subj.Name != "my-agent" || subj.Namespace != "prod" {
		t.Errorf("expected subject SA my-agent in prod, got Kind=%q Name=%q NS=%q", subj.Kind, subj.Name, subj.Namespace)
	}
}

func TestBuildRoleBinding_UserManagedSA(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-agent",
			Namespace: "prod",
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			ServiceAccount: &clawv1alpha1.ServiceAccountRef{
				Name: "custom-sa",
			},
		},
	}

	rb := buildRoleBinding(claw)

	if rb.Subjects[0].Name != "custom-sa" {
		t.Errorf("expected subject to reference custom-sa, got %q", rb.Subjects[0].Name)
	}
}

func TestNeedsRBAC_CurrentlyAlwaysFalse(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}
	if needsRBAC(claw) {
		t.Error("expected needsRBAC to return false until SelfConfigure field is added to CRD")
	}
}

// ---------------------------------------------------------------------------
// Unit tests: buildNetworkPolicy
// ---------------------------------------------------------------------------

func TestBuildNetworkPolicy(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "prod"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Security: &clawv1alpha1.SecuritySpec{
				NetworkPolicy: &clawv1alpha1.NetworkPolicySpec{
					Enabled: true,
				},
			},
		},
	}
	np := buildNetworkPolicy(claw, 18900)

	if np.Name != "my-agent-netpol" {
		t.Errorf("expected name my-agent-netpol, got %s", np.Name)
	}
	if np.Namespace != "prod" {
		t.Errorf("expected namespace prod, got %s", np.Namespace)
	}

	// Verify podSelector uses claw.prismer.ai/instance label.
	if np.Spec.PodSelector.MatchLabels["claw.prismer.ai/instance"] != "my-agent" {
		t.Error("expected podSelector to use claw.prismer.ai/instance label")
	}

	// Verify policyTypes.
	if len(np.Spec.PolicyTypes) != 2 {
		t.Fatalf("expected 2 policy types, got %d", len(np.Spec.PolicyTypes))
	}

	// Verify DNS egress (port 53 UDP+TCP).
	if len(np.Spec.Egress) < 2 {
		t.Fatalf("expected at least 2 egress rules, got %d", len(np.Spec.Egress))
	}
	dnsRule := np.Spec.Egress[0]
	if len(dnsRule.Ports) != 2 {
		t.Errorf("expected 2 DNS ports (UDP+TCP), got %d", len(dnsRule.Ports))
	}

	// Verify HTTPS egress (port 443 TCP).
	httpsRule := np.Spec.Egress[1]
	if len(httpsRule.Ports) != 1 || httpsRule.Ports[0].Port.IntValue() != 443 {
		t.Error("expected HTTPS egress on port 443")
	}

	// Verify same-namespace ingress.
	if len(np.Spec.Ingress) < 1 {
		t.Fatal("expected at least 1 ingress rule")
	}
	ingressRule := np.Spec.Ingress[0]
	if len(ingressRule.From) != 1 {
		t.Fatalf("expected 1 ingress peer, got %d", len(ingressRule.From))
	}
	if ingressRule.From[0].PodSelector == nil {
		t.Error("expected podSelector peer for same-namespace ingress")
	}
	if len(ingressRule.Ports) != 1 || ingressRule.Ports[0].Port.IntValue() != 18900 {
		t.Error("expected ingress on gateway port 18900")
	}
}

func TestBuildNetworkPolicy_WithCustomEgress(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "prod"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Security: &clawv1alpha1.SecuritySpec{
				NetworkPolicy: &clawv1alpha1.NetworkPolicySpec{
					Enabled:            true,
					AllowedEgressCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12"},
				},
			},
		},
	}
	np := buildNetworkPolicy(claw, 18900)

	// DNS + HTTPS + 2 custom CIDRs = 4 egress rules.
	if len(np.Spec.Egress) != 4 {
		t.Errorf("expected 4 egress rules, got %d", len(np.Spec.Egress))
	}
	// Check first custom CIDR.
	if np.Spec.Egress[2].To[0].IPBlock.CIDR != "10.0.0.0/8" {
		t.Errorf("expected CIDR 10.0.0.0/8, got %s", np.Spec.Egress[2].To[0].IPBlock.CIDR)
	}
}

func TestBuildNetworkPolicy_WithCrossNamespaceIngress(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "prod"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Security: &clawv1alpha1.SecuritySpec{
				NetworkPolicy: &clawv1alpha1.NetworkPolicySpec{
					Enabled:                  true,
					AllowedIngressNamespaces: []string{"monitoring"},
				},
			},
		},
	}
	np := buildNetworkPolicy(claw, 18900)

	// Same-namespace + monitoring = 2 ingress rules.
	if len(np.Spec.Ingress) != 2 {
		t.Errorf("expected 2 ingress rules, got %d", len(np.Spec.Ingress))
	}
	nsSelector := np.Spec.Ingress[1].From[0].NamespaceSelector
	if nsSelector == nil {
		t.Fatal("expected namespace selector for cross-namespace rule")
	}
	if nsSelector.MatchLabels["kubernetes.io/metadata.name"] != "monitoring" {
		t.Error("expected namespace selector for monitoring")
	}
}

func TestBuildNetworkPolicy_WithIngressEnabled(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "prod"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Security: &clawv1alpha1.SecuritySpec{
				NetworkPolicy: &clawv1alpha1.NetworkPolicySpec{
					Enabled: true,
				},
			},
			Ingress: &clawv1alpha1.IngressSpec{
				Enabled: true,
				Host:    "agent.example.com",
			},
		},
	}
	np := buildNetworkPolicy(claw, 18900)

	// Same-namespace + ingress-controller = 2 ingress rules.
	if len(np.Spec.Ingress) != 2 {
		t.Errorf("expected 2 ingress rules, got %d", len(np.Spec.Ingress))
	}
	ingressCtrl := np.Spec.Ingress[1]
	if ingressCtrl.From[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "ingress-nginx" {
		t.Error("expected ingress-nginx namespace selector")
	}
}

func TestNetworkPolicyEnabled(t *testing.T) {
	tests := []struct {
		name string
		claw *clawv1alpha1.Claw
		want bool
	}{
		{
			name: "nil security",
			claw: &clawv1alpha1.Claw{},
			want: false,
		},
		{
			name: "nil networkPolicy",
			claw: &clawv1alpha1.Claw{
				Spec: clawv1alpha1.ClawSpec{Security: &clawv1alpha1.SecuritySpec{}},
			},
			want: false,
		},
		{
			name: "disabled",
			claw: &clawv1alpha1.Claw{
				Spec: clawv1alpha1.ClawSpec{
					Security: &clawv1alpha1.SecuritySpec{
						NetworkPolicy: &clawv1alpha1.NetworkPolicySpec{Enabled: false},
					},
				},
			},
			want: false,
		},
		{
			name: "enabled",
			claw: &clawv1alpha1.Claw{
				Spec: clawv1alpha1.ClawSpec{
					Security: &clawv1alpha1.SecuritySpec{
						NetworkPolicy: &clawv1alpha1.NetworkPolicySpec{Enabled: true},
					},
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := networkPolicyEnabled(tt.claw); got != tt.want {
				t.Errorf("networkPolicyEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Unit tests: buildIngress
// ---------------------------------------------------------------------------

func TestBuildIngress_Basic(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "prod"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Ingress: &clawv1alpha1.IngressSpec{
				Enabled: true,
				Host:    "agent.example.com",
			},
		},
	}
	ing := buildIngress(claw, 18900)

	if ing.Name != "my-agent" {
		t.Errorf("expected name my-agent, got %s", ing.Name)
	}
	if len(ing.Spec.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(ing.Spec.Rules))
	}
	if ing.Spec.Rules[0].Host != "agent.example.com" {
		t.Errorf("expected host agent.example.com, got %s", ing.Spec.Rules[0].Host)
	}
	paths := ing.Spec.Rules[0].HTTP.Paths
	if len(paths) != 1 || paths[0].Backend.Service.Port.Number != 18900 {
		t.Error("expected backend service port 18900")
	}
	if ing.Spec.IngressClassName != nil {
		t.Error("expected nil IngressClassName when not specified")
	}
	if ing.Spec.TLS != nil {
		t.Error("expected nil TLS when not specified")
	}
}

func TestBuildIngress_WithTLS(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "prod"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Ingress: &clawv1alpha1.IngressSpec{
				Enabled:   true,
				Host:      "agent.example.com",
				ClassName: "nginx",
				TLS: &clawv1alpha1.IngressTLS{
					SecretName: "agent-tls",
				},
			},
		},
	}
	ing := buildIngress(claw, 18900)

	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "nginx" {
		t.Error("expected IngressClassName nginx")
	}
	if len(ing.Spec.TLS) != 1 {
		t.Fatalf("expected 1 TLS entry, got %d", len(ing.Spec.TLS))
	}
	if ing.Spec.TLS[0].SecretName != "agent-tls" {
		t.Errorf("expected TLS secret agent-tls, got %s", ing.Spec.TLS[0].SecretName)
	}
	if len(ing.Spec.TLS[0].Hosts) != 1 || ing.Spec.TLS[0].Hosts[0] != "agent.example.com" {
		t.Error("expected TLS host agent.example.com")
	}
}

func TestBuildIngress_WithBasicAuth(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "prod"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Ingress: &clawv1alpha1.IngressSpec{
				Enabled: true,
				Host:    "agent.example.com",
				BasicAuth: &clawv1alpha1.BasicAuthSpec{
					Enabled:    true,
					SecretName: "htpasswd-secret",
				},
			},
		},
	}
	ing := buildIngress(claw, 18900)

	if ing.Annotations["nginx.ingress.kubernetes.io/auth-type"] != "basic" {
		t.Error("expected auth-type basic annotation")
	}
	if ing.Annotations["nginx.ingress.kubernetes.io/auth-secret"] != "htpasswd-secret" {
		t.Error("expected auth-secret annotation")
	}
}

func TestBuildIngress_WithCustomAnnotations(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "prod"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Ingress: &clawv1alpha1.IngressSpec{
				Enabled: true,
				Host:    "agent.example.com",
				BasicAuth: &clawv1alpha1.BasicAuthSpec{
					Enabled:    true,
					SecretName: "htpasswd-secret",
				},
				Annotations: map[string]string{
					"custom/annotation":                        "value",
					"nginx.ingress.kubernetes.io/auth-type":     "custom-override",
				},
			},
		},
	}
	ing := buildIngress(claw, 18900)

	// User annotations override basic auth annotations.
	if ing.Annotations["nginx.ingress.kubernetes.io/auth-type"] != "custom-override" {
		t.Error("expected user annotation to override basic auth annotation")
	}
	if ing.Annotations["custom/annotation"] != "value" {
		t.Error("expected custom annotation to be present")
	}
}

func TestEnsureIngress_NoOp(t *testing.T) {
	// When ingress is nil, ensureIngress should be a no-op.
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}
	if claw.Spec.Ingress != nil {
		t.Error("expected nil ingress spec")
	}

	// Also test with enabled=false.
	claw.Spec.Ingress = &clawv1alpha1.IngressSpec{Enabled: false}
	if claw.Spec.Ingress.Enabled {
		t.Error("expected disabled ingress")
	}
}

// ---------------------------------------------------------------------------
// Unit tests: buildPDB
// ---------------------------------------------------------------------------

func TestBuildPDB_Default(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "prod"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}
	pdb := buildPDB(claw)

	if pdb.Name != "my-agent" {
		t.Errorf("expected name my-agent, got %s", pdb.Name)
	}
	if pdb.Namespace != "prod" {
		t.Errorf("expected namespace prod, got %s", pdb.Namespace)
	}
	if pdb.Spec.MinAvailable.IntValue() != 1 {
		t.Errorf("expected minAvailable=1, got %d", pdb.Spec.MinAvailable.IntValue())
	}
	if pdb.Spec.Selector.MatchLabels["claw.prismer.ai/instance"] != "my-agent" {
		t.Error("expected selector with claw.prismer.ai/instance label")
	}
}

func TestBuildPDB_CustomMinAvailable(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "prod"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Availability: &clawv1alpha1.AvailabilitySpec{
				PDB: &clawv1alpha1.PDBSpec{
					Enabled:      true,
					MinAvailable: 3,
				},
			},
		},
	}
	pdb := buildPDB(claw)

	if pdb.Spec.MinAvailable.IntValue() != 3 {
		t.Errorf("expected minAvailable=3, got %d", pdb.Spec.MinAvailable.IntValue())
	}
}

func TestPDBEnabled(t *testing.T) {
	tests := []struct {
		name string
		claw *clawv1alpha1.Claw
		want bool
	}{
		{
			name: "nil availability defaults to enabled",
			claw: &clawv1alpha1.Claw{},
			want: true,
		},
		{
			name: "nil pdb defaults to enabled",
			claw: &clawv1alpha1.Claw{
				Spec: clawv1alpha1.ClawSpec{
					Availability: &clawv1alpha1.AvailabilitySpec{},
				},
			},
			want: true,
		},
		{
			name: "explicitly disabled",
			claw: &clawv1alpha1.Claw{
				Spec: clawv1alpha1.ClawSpec{
					Availability: &clawv1alpha1.AvailabilitySpec{
						PDB: &clawv1alpha1.PDBSpec{Enabled: false},
					},
				},
			},
			want: false,
		},
		{
			name: "explicitly enabled",
			claw: &clawv1alpha1.Claw{
				Spec: clawv1alpha1.ClawSpec{
					Availability: &clawv1alpha1.AvailabilitySpec{
						PDB: &clawv1alpha1.PDBSpec{Enabled: true},
					},
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pdbEnabled(tt.claw); got != tt.want {
				t.Errorf("pdbEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Unit tests: PVC ownerReferences
// ---------------------------------------------------------------------------

func TestHasOwnerReference(t *testing.T) {
	clawUID := types.UID("claw-uid-123")
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: clawUID},
	}

	pvcWithRef := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{
				{UID: clawUID, Name: "test"},
			},
		},
	}
	if !hasOwnerReference(pvcWithRef, claw) {
		t.Error("expected hasOwnerReference to return true")
	}

	pvcWithoutRef := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{},
	}
	if hasOwnerReference(pvcWithoutRef, claw) {
		t.Error("expected hasOwnerReference to return false")
	}
}

func TestRemoveOwnerRef(t *testing.T) {
	uid := types.UID("claw-uid-123")
	otherUID := types.UID("other-uid-456")
	gvk := schema.GroupVersionKind{Group: "claw.prismer.ai", Version: "v1alpha1", Kind: "Claw"}

	refs := []metav1.OwnerReference{
		{UID: uid, Name: "test-claw"},
		{UID: otherUID, Name: "other-owner"},
	}

	result := removeOwnerRef(refs, uid, gvk)
	if len(result) != 1 {
		t.Fatalf("expected 1 remaining ref, got %d", len(result))
	}
	if result[0].UID != otherUID {
		t.Errorf("expected remaining ref to be other-uid-456, got %s", result[0].UID)
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
