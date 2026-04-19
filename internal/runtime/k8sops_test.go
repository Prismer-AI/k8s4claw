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

func TestK8sOpsAdapter_DefaultConfig(t *testing.T) {
	adapter := &K8sOpsAdapter{}
	config := adapter.DefaultConfig()
	assert.Equal(t, 18910, config.GatewayPort)
	assert.Equal(t, "/workspace", config.WorkspacePath)
	assert.Contains(t, config.Environment, "CLAW4K8S_ROLE")
	assert.Equal(t, "companion", config.Environment["CLAW4K8S_ROLE"])
}

func TestK8sOpsAdapter_GracefulShutdownSeconds(t *testing.T) {
	adapter := &K8sOpsAdapter{}
	assert.Equal(t, int32(30), adapter.GracefulShutdownSeconds())
}

func TestK8sOpsAdapter_PodTemplate(t *testing.T) {
	adapter := &K8sOpsAdapter{}
	claw := &v1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ops", Namespace: "default"},
		Spec:       v1alpha1.ClawSpec{Runtime: v1alpha1.RuntimeK8sOps},
	}
	tmpl := adapter.PodTemplate(claw)
	require.NotNil(t, tmpl)
	require.NotEmpty(t, tmpl.Spec.Containers)

	container := tmpl.Spec.Containers[0]
	assert.Equal(t, int32(18910), container.Ports[0].ContainerPort)
}

func TestK8sOpsAdapter_SecurityContext(t *testing.T) {
	adapter := &K8sOpsAdapter{}
	claw := &v1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ops", Namespace: "default"},
		Spec:       v1alpha1.ClawSpec{Runtime: v1alpha1.RuntimeK8sOps},
	}
	tmpl := adapter.PodTemplate(claw)
	require.NotNil(t, tmpl)

	container := tmpl.Spec.Containers[0]
	sc := container.SecurityContext
	require.NotNil(t, sc)
	assert.True(t, *sc.RunAsNonRoot)
	assert.True(t, *sc.ReadOnlyRootFilesystem)
	assert.False(t, *sc.AllowPrivilegeEscalation)
	require.NotNil(t, sc.Capabilities)
	assert.Contains(t, sc.Capabilities.Drop, corev1.Capability("ALL"))
}

func TestK8sOpsAdapter_UniquePort(t *testing.T) {
	adapter := &K8sOpsAdapter{}
	config := adapter.DefaultConfig()
	existingPorts := []int{18900, 19000, 3000, 8080, 3001, 18800}
	for _, p := range existingPorts {
		assert.NotEqual(t, p, config.GatewayPort, "port conflict with existing adapter")
	}
}

func TestK8sOpsAdapter_Validate_RequiresNetworkPolicy(t *testing.T) {
	adapter := &K8sOpsAdapter{}

	// No security spec → error.
	errs := adapter.Validate(context.Background(), &v1alpha1.ClawSpec{Runtime: v1alpha1.RuntimeK8sOps})
	assert.NotEmpty(t, errs)

	// NetworkPolicy disabled → error.
	errs = adapter.Validate(context.Background(), &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeK8sOps,
		Security: &v1alpha1.SecuritySpec{
			NetworkPolicy: &v1alpha1.NetworkPolicySpec{Enabled: false},
		},
	})
	assert.NotEmpty(t, errs)

	// NetworkPolicy enabled → OK.
	errs = adapter.Validate(context.Background(), &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeK8sOps,
		Security: &v1alpha1.SecuritySpec{
			NetworkPolicy: &v1alpha1.NetworkPolicySpec{Enabled: true},
		},
	})
	assert.Empty(t, errs)
}
