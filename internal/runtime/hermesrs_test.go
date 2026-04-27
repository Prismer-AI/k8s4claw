package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestHermesRSAdapter_PodTemplate(t *testing.T) {
	t.Parallel()
	adapter := &HermesRSAdapter{}
	claw := &v1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "rs-test", Namespace: "ns"},
		Spec:       v1alpha1.ClawSpec{Runtime: v1alpha1.RuntimeHermesRS},
	}

	pt := adapter.PodTemplate(claw)
	require.NotNil(t, pt)

	// Runtime container.
	require.NotEmpty(t, pt.Spec.Containers)
	runtime := pt.Spec.Containers[0]
	assert.Equal(t, "runtime", runtime.Name)
	assert.Equal(t, "ghcr.io/prismer-ai/hermes-agent-rs:latest", runtime.Image)
	assert.Equal(t, int32(8080), runtime.Ports[0].ContainerPort)

	// Env vars.
	envMap := make(map[string]string)
	for _, e := range runtime.Env {
		envMap[e.Name] = e.Value
	}
	assert.Equal(t, "/data", envMap["HERMES_HOME"])
	assert.Equal(t, "0.0.0.0:8080", envMap["HERMES_GATEWAY_API_SERVER_BIND_ADDR"])
	assert.Equal(t, "info", envMap["RUST_LOG"])

	// Security context.
	require.NotNil(t, runtime.SecurityContext)
	assert.True(t, *runtime.SecurityContext.RunAsNonRoot)
	assert.False(t, *runtime.SecurityContext.AllowPrivilegeEscalation)
	assert.Equal(t, int64(10000), *runtime.SecurityContext.RunAsUser)
}

func TestHermesRSAdapter_DefaultConfig(t *testing.T) {
	t.Parallel()
	adapter := &HermesRSAdapter{}
	cfg := adapter.DefaultConfig()

	assert.Equal(t, 8080, cfg.GatewayPort)
	assert.Equal(t, "/data/skills", cfg.WorkspacePath)
	assert.Equal(t, "/data", cfg.Environment["HERMES_HOME"])
}

func TestHermesRSAdapter_GracefulShutdown(t *testing.T) {
	t.Parallel()
	adapter := &HermesRSAdapter{}
	assert.Equal(t, int32(30), adapter.GracefulShutdownSeconds())
}

func TestHermesRSAdapter_Probes(t *testing.T) {
	t.Parallel()
	adapter := &HermesRSAdapter{}

	lp := adapter.HealthProbe(nil)
	require.NotNil(t, lp)
	assert.Equal(t, "/health", lp.HTTPGet.Path)

	rp := adapter.ReadinessProbe(nil)
	require.NotNil(t, rp)
	assert.Equal(t, "/health", rp.HTTPGet.Path)
	assert.Equal(t, int32(3), rp.InitialDelaySeconds)
}

func TestHermesRSAdapter_Validate_RequiresCredentials(t *testing.T) {
	t.Parallel()
	adapter := &HermesRSAdapter{}
	errs := adapter.Validate(context.Background(), &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeHermesRS,
	})
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Detail, "credentials")
}

func TestHermesRSAdapter_Validate_AcceptsValidSpec(t *testing.T) {
	t.Parallel()
	adapter := &HermesRSAdapter{}
	errs := adapter.Validate(context.Background(), &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeHermesRS,
		Credentials: &v1alpha1.CredentialSpec{
			SecretRef: &corev1.LocalObjectReference{Name: "api-keys"},
		},
	})
	assert.Empty(t, errs)
}

func TestHermesRSAdapter_Validate_WrongMountPath(t *testing.T) {
	t.Parallel()
	adapter := &HermesRSAdapter{}
	errs := adapter.Validate(context.Background(), &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeHermesRS,
		Credentials: &v1alpha1.CredentialSpec{
			SecretRef: &corev1.LocalObjectReference{Name: "api-keys"},
		},
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{
				Enabled:   true,
				MountPath: "/wrong/path",
				Size:      "1Gi",
			},
		},
	})
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Detail, "/data")
}
