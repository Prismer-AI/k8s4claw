package controller

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func createTestNamespace(t *testing.T) string {
	t.Helper()
	ns := fmt.Sprintf("test-clawops-%d", time.Now().UnixNano())
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	require.NoError(t, k8sClient.Create(ctx, namespace))
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, namespace)
	})
	return ns
}

func TestClawOpsController_OOMCreatesEscalation(t *testing.T) {
	ns := createTestNamespace(t)

	// Create a Claw.
	claw := &v1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test-oom-ops", Namespace: ns},
		Spec: v1alpha1.ClawSpec{
			Runtime:     v1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
		},
	}
	ensureTestSecret(t, ns)
	require.NoError(t, k8sClient.Create(ctx, claw))

	// Create a synthetic Pod with OOMKilled status.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-oom-ops-0",
			Namespace: ns,
			Labels:    map[string]string{"claw.prismer.ai/instance": "test-oom-ops"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "runtime", Image: "busybox"}},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, pod))

	// Patch pod status with OOMKilled.
	pod.Status = corev1.PodStatus{
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:         "runtime",
			RestartCount: 3,
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					Reason:   "OOMKilled",
					ExitCode: 137,
				},
			},
		}},
	}
	require.NoError(t, k8sClient.Status().Update(ctx, pod))

	// Wait for ClawOpsEscalation to be created.
	var escList v1alpha1.ClawOpsEscalationList
	waitForCondition(t, 15*time.Second, 200*time.Millisecond, func() (bool, error) {
		if err := k8sClient.List(ctx, &escList, client.InNamespace(ns)); err != nil {
			return false, err
		}
		for _, esc := range escList.Items {
			if esc.Spec.ClawRef.Name == "test-oom-ops" {
				return true, nil
			}
		}
		return false, nil
	})

	// Find the escalation for our claw.
	var found *v1alpha1.ClawOpsEscalation
	for i := range escList.Items {
		if escList.Items[i].Spec.ClawRef.Name == "test-oom-ops" {
			found = &escList.Items[i]
			break
		}
	}
	require.NotNil(t, found, "should find escalation for test-oom-ops")
	assert.Equal(t, v1alpha1.TriggerOOMKilled, found.Spec.Trigger.Type)
	assert.Equal(t, v1alpha1.SeverityHigh, found.Spec.Severity)
}
