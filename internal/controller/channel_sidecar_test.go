package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestBuildChannelContainer_BuiltinNative(t *testing.T) {
	t.Parallel()

	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{Name: "slack-team"},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeSlack,
			Mode: clawv1alpha1.ChannelModeBidirectional,
		},
	}
	ref := clawv1alpha1.ChannelRef{
		Name: "slack-team",
		Mode: clawv1alpha1.ChannelModeBidirectional,
	}

	c := buildChannelContainer(channel, ref, true)

	// Verify container name.
	if c.Name != "channel-slack-team" {
		t.Errorf("expected name %q, got %q", "channel-slack-team", c.Name)
	}

	// Verify image.
	wantImage := "ghcr.io/prismer-ai/claw-channel-slack:latest"
	if c.Image != wantImage {
		t.Errorf("expected image %q, got %q", wantImage, c.Image)
	}

	// Verify RestartPolicy = Always (native sidecar).
	if c.RestartPolicy == nil {
		t.Fatal("expected RestartPolicy to be set for native sidecar")
	}
	if *c.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Errorf("expected RestartPolicy=Always, got %q", *c.RestartPolicy)
	}

	// Verify ipc-socket mount present.
	foundIPCMount := false
	for _, m := range c.VolumeMounts {
		if m.Name == "ipc-socket" && m.MountPath == "/var/run/claw" {
			foundIPCMount = true
			break
		}
	}
	if !foundIPCMount {
		t.Error("expected ipc-socket volume mount at /var/run/claw")
	}

	// Verify env vars.
	envMap := envToMap(c.Env)
	if envMap["CHANNEL_NAME"] != "slack-team" {
		t.Errorf("expected CHANNEL_NAME=%q, got %q", "slack-team", envMap["CHANNEL_NAME"])
	}
	if envMap["CHANNEL_TYPE"] != "slack" {
		t.Errorf("expected CHANNEL_TYPE=%q, got %q", "slack", envMap["CHANNEL_TYPE"])
	}
	if envMap["CHANNEL_MODE"] != "bidirectional" {
		t.Errorf("expected CHANNEL_MODE=%q, got %q", "bidirectional", envMap["CHANNEL_MODE"])
	}

	// Verify security context is set.
	if c.SecurityContext == nil {
		t.Fatal("expected security context to be set")
	}
	if c.SecurityContext.RunAsNonRoot == nil || !*c.SecurityContext.RunAsNonRoot {
		t.Error("expected runAsNonRoot=true")
	}
	if c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem {
		t.Error("expected readOnlyRootFilesystem=true")
	}
}

func TestBuildChannelContainer_BuiltinRegular(t *testing.T) {
	t.Parallel()

	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{Name: "webhook-ingest"},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeWebhook,
			Mode: clawv1alpha1.ChannelModeInbound,
		},
	}
	ref := clawv1alpha1.ChannelRef{
		Name: "webhook-ingest",
		Mode: clawv1alpha1.ChannelModeInbound,
	}

	c := buildChannelContainer(channel, ref, false)

	// Verify no RestartPolicy for regular sidecar.
	if c.RestartPolicy != nil {
		t.Errorf("expected no RestartPolicy for regular sidecar, got %v", *c.RestartPolicy)
	}

	// Verify default resources are applied.
	defaults := channelDefaultResources()
	if !c.Resources.Requests.Cpu().Equal(*defaults.Requests.Cpu()) {
		t.Errorf("expected CPU request %s, got %s", defaults.Requests.Cpu().String(), c.Resources.Requests.Cpu().String())
	}
	if !c.Resources.Requests.Memory().Equal(*defaults.Requests.Memory()) {
		t.Errorf("expected memory request %s, got %s", defaults.Requests.Memory().String(), c.Resources.Requests.Memory().String())
	}
	if !c.Resources.Limits.Cpu().Equal(*defaults.Limits.Cpu()) {
		t.Errorf("expected CPU limit %s, got %s", defaults.Limits.Cpu().String(), c.Resources.Limits.Cpu().String())
	}
	if !c.Resources.Limits.Memory().Equal(*defaults.Limits.Memory()) {
		t.Errorf("expected memory limit %s, got %s", defaults.Limits.Memory().String(), c.Resources.Limits.Memory().String())
	}
}

func TestBuildChannelContainer_Custom(t *testing.T) {
	t.Parallel()

	customPort := corev1.ContainerPort{
		Name:          "grpc",
		ContainerPort: 50051,
		Protocol:      corev1.ProtocolTCP,
	}
	customEnv := corev1.EnvVar{Name: "CUSTOM_VAR", Value: "custom-value"}
	livenessProbe := &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: portInt(50051)},
		},
	}

	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{Name: "my-custom-channel"},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeCustom,
			Mode: clawv1alpha1.ChannelModeBidirectional,
			Sidecar: &clawv1alpha1.SidecarSpec{
				Image:         "my-registry.io/custom-adapter:v1",
				Ports:         []corev1.ContainerPort{customPort},
				Env:           []corev1.EnvVar{customEnv},
				LivenessProbe: livenessProbe,
			},
		},
	}
	ref := clawv1alpha1.ChannelRef{
		Name: "my-custom-channel",
		Mode: clawv1alpha1.ChannelModeBidirectional,
	}

	c := buildChannelContainer(channel, ref, false)

	// Verify custom image.
	if c.Image != "my-registry.io/custom-adapter:v1" {
		t.Errorf("expected custom image, got %q", c.Image)
	}

	// Verify custom ports.
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 50051 {
		t.Errorf("expected custom port 50051, got %v", c.Ports)
	}

	// Verify custom env is appended after base envs.
	envMap := envToMap(c.Env)
	if envMap["CUSTOM_VAR"] != "custom-value" {
		t.Errorf("expected CUSTOM_VAR=%q, got %q", "custom-value", envMap["CUSTOM_VAR"])
	}
	// Base envs should still be present.
	if envMap["CHANNEL_NAME"] != "my-custom-channel" {
		t.Errorf("expected CHANNEL_NAME=%q, got %q", "my-custom-channel", envMap["CHANNEL_NAME"])
	}

	// Verify liveness probe.
	if c.LivenessProbe == nil {
		t.Error("expected liveness probe to be set")
	}
}

func TestBuildChannelContainer_WithCredentials(t *testing.T) {
	t.Parallel()

	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{Name: "slack-creds"},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeSlack,
			Mode: clawv1alpha1.ChannelModeBidirectional,
			Credentials: &clawv1alpha1.CredentialSpec{
				SecretRef: &corev1.LocalObjectReference{
					Name: "slack-api-token",
				},
			},
		},
	}
	ref := clawv1alpha1.ChannelRef{
		Name: "slack-creds",
		Mode: clawv1alpha1.ChannelModeBidirectional,
	}

	c := buildChannelContainer(channel, ref, false)

	// Verify envFrom contains the secret reference.
	if len(c.EnvFrom) != 1 {
		t.Fatalf("expected 1 envFrom entry, got %d", len(c.EnvFrom))
	}
	if c.EnvFrom[0].SecretRef == nil {
		t.Fatal("expected envFrom[0].SecretRef to be set")
	}
	if c.EnvFrom[0].SecretRef.Name != "slack-api-token" {
		t.Errorf("expected secret name %q, got %q", "slack-api-token", c.EnvFrom[0].SecretRef.Name)
	}
}

func TestBuildChannelContainer_WithConfig(t *testing.T) {
	t.Parallel()

	configJSON := `{"team_id":"T12345","channel":"#general"}`
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{Name: "slack-config"},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeSlack,
			Mode: clawv1alpha1.ChannelModeBidirectional,
			Config: &apiextensionsv1.JSON{
				Raw: []byte(configJSON),
			},
		},
	}
	ref := clawv1alpha1.ChannelRef{
		Name: "slack-config",
		Mode: clawv1alpha1.ChannelModeBidirectional,
	}

	c := buildChannelContainer(channel, ref, false)

	// Verify CHANNEL_CONFIG env var is present.
	envMap := envToMap(c.Env)
	val, ok := envMap["CHANNEL_CONFIG"]
	if !ok {
		t.Fatal("expected CHANNEL_CONFIG env var to be present")
	}
	if val == "" {
		t.Error("expected CHANNEL_CONFIG to have a non-empty value")
	}
}

func TestBuildChannelContainer_ResourceOverride(t *testing.T) {
	t.Parallel()

	customResources := &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
	}

	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{Name: "telegram-custom-res"},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type:      clawv1alpha1.ChannelTypeTelegram,
			Mode:      clawv1alpha1.ChannelModeBidirectional,
			Resources: customResources,
		},
	}
	ref := clawv1alpha1.ChannelRef{
		Name: "telegram-custom-res",
		Mode: clawv1alpha1.ChannelModeBidirectional,
	}

	c := buildChannelContainer(channel, ref, false)

	// Verify custom resources are used instead of defaults.
	wantCPU := resource.MustParse("100m")
	if !c.Resources.Requests.Cpu().Equal(wantCPU) {
		t.Errorf("expected CPU request %s, got %s", wantCPU.String(), c.Resources.Requests.Cpu().String())
	}
	wantMem := resource.MustParse("64Mi")
	if !c.Resources.Requests.Memory().Equal(wantMem) {
		t.Errorf("expected memory request %s, got %s", wantMem.String(), c.Resources.Requests.Memory().String())
	}
	wantCPULim := resource.MustParse("500m")
	if !c.Resources.Limits.Cpu().Equal(wantCPULim) {
		t.Errorf("expected CPU limit %s, got %s", wantCPULim.String(), c.Resources.Limits.Cpu().String())
	}
	wantMemLim := resource.MustParse("256Mi")
	if !c.Resources.Limits.Memory().Equal(wantMemLim) {
		t.Errorf("expected memory limit %s, got %s", wantMemLim.String(), c.Resources.Limits.Memory().String())
	}
}

func TestIsModeCompatible(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		requested  clawv1alpha1.ChannelMode
		capability clawv1alpha1.ChannelMode
		want       bool
	}{
		{
			name:       "bidirectional capability supports inbound",
			requested:  clawv1alpha1.ChannelModeInbound,
			capability: clawv1alpha1.ChannelModeBidirectional,
			want:       true,
		},
		{
			name:       "bidirectional capability supports outbound",
			requested:  clawv1alpha1.ChannelModeOutbound,
			capability: clawv1alpha1.ChannelModeBidirectional,
			want:       true,
		},
		{
			name:       "bidirectional capability supports bidirectional",
			requested:  clawv1alpha1.ChannelModeBidirectional,
			capability: clawv1alpha1.ChannelModeBidirectional,
			want:       true,
		},
		{
			name:       "inbound capability supports inbound",
			requested:  clawv1alpha1.ChannelModeInbound,
			capability: clawv1alpha1.ChannelModeInbound,
			want:       true,
		},
		{
			name:       "inbound capability rejects outbound",
			requested:  clawv1alpha1.ChannelModeOutbound,
			capability: clawv1alpha1.ChannelModeInbound,
			want:       false,
		},
		{
			name:       "inbound capability rejects bidirectional",
			requested:  clawv1alpha1.ChannelModeBidirectional,
			capability: clawv1alpha1.ChannelModeInbound,
			want:       false,
		},
		{
			name:       "outbound capability supports outbound",
			requested:  clawv1alpha1.ChannelModeOutbound,
			capability: clawv1alpha1.ChannelModeOutbound,
			want:       true,
		},
		{
			name:       "outbound capability rejects inbound",
			requested:  clawv1alpha1.ChannelModeInbound,
			capability: clawv1alpha1.ChannelModeOutbound,
			want:       false,
		},
		{
			name:       "outbound capability rejects bidirectional",
			requested:  clawv1alpha1.ChannelModeBidirectional,
			capability: clawv1alpha1.ChannelModeOutbound,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isModeCompatible(tt.requested, tt.capability)
			if got != tt.want {
				t.Errorf("isModeCompatible(%q, %q) = %v, want %v",
					tt.requested, tt.capability, got, tt.want)
			}
		})
	}
}

func TestChannelDefaultResources(t *testing.T) {
	t.Parallel()

	res := channelDefaultResources()

	wantCPUReq := resource.MustParse("50m")
	if !res.Requests.Cpu().Equal(wantCPUReq) {
		t.Errorf("expected CPU request %s, got %s", wantCPUReq.String(), res.Requests.Cpu().String())
	}

	wantMemReq := resource.MustParse("32Mi")
	if !res.Requests.Memory().Equal(wantMemReq) {
		t.Errorf("expected memory request %s, got %s", wantMemReq.String(), res.Requests.Memory().String())
	}

	wantCPULim := resource.MustParse("200m")
	if !res.Limits.Cpu().Equal(wantCPULim) {
		t.Errorf("expected CPU limit %s, got %s", wantCPULim.String(), res.Limits.Cpu().String())
	}

	wantMemLim := resource.MustParse("128Mi")
	if !res.Limits.Memory().Equal(wantMemLim) {
		t.Errorf("expected memory limit %s, got %s", wantMemLim.String(), res.Limits.Memory().String())
	}
}

func TestBuiltinChannelImage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		channelType clawv1alpha1.ChannelType
		wantImage   string
	}{
		{clawv1alpha1.ChannelTypeSlack, "ghcr.io/prismer-ai/claw-channel-slack:latest"},
		{clawv1alpha1.ChannelTypeTelegram, "ghcr.io/prismer-ai/claw-channel-telegram:latest"},
		{clawv1alpha1.ChannelTypeWhatsApp, "ghcr.io/prismer-ai/claw-channel-whatsapp:latest"},
		{clawv1alpha1.ChannelTypeDiscord, "ghcr.io/prismer-ai/claw-channel-discord:latest"},
		{clawv1alpha1.ChannelTypeMatrix, "ghcr.io/prismer-ai/claw-channel-matrix:latest"},
		{clawv1alpha1.ChannelTypeWebhook, "ghcr.io/prismer-ai/claw-channel-webhook:latest"},
		{clawv1alpha1.ChannelTypeCustom, "ghcr.io/prismer-ai/claw-channel-custom:latest"},
	}

	for _, tt := range tests {
		t.Run(string(tt.channelType), func(t *testing.T) {
			t.Parallel()
			got := builtinChannelImage(tt.channelType)
			if got != tt.wantImage {
				t.Errorf("builtinChannelImage(%q) = %q, want %q", tt.channelType, got, tt.wantImage)
			}
		})
	}
}

// envToMap converts a slice of EnvVar to a map for easier assertions.
func envToMap(envs []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(envs))
	for _, e := range envs {
		m[e.Name] = e.Value
	}
	return m
}

// portInt creates an intstr.IntOrString from an int for probe port specs.
func portInt(val int32) intstr.IntOrString {
	return intstr.FromInt32(val)
}
