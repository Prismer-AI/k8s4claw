package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestExtractPodSignals_OOMKilled(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
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
		},
	}
	signals := ExtractPodSignals(pod)
	assert.Len(t, signals, 1)
	assert.Equal(t, v1alpha1.TriggerOOMKilled, signals[0].Type)
	assert.Equal(t, v1alpha1.SeverityHigh, signals[0].Severity)
	assert.Equal(t, int32(3), signals[0].Count)
}

func TestExtractPodSignals_CrashLoopBackOff(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "runtime",
				RestartCount: 6,
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{
						Reason: "CrashLoopBackOff",
					},
				},
			}},
		},
	}
	signals := ExtractPodSignals(pod)
	assert.Len(t, signals, 1)
	assert.Equal(t, v1alpha1.TriggerCrashLoop, signals[0].Type)
	assert.Equal(t, int32(6), signals[0].Count)
}

func TestExtractPodSignals_Healthy(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "runtime",
				Ready: true,
				State: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{},
				},
			}},
		},
	}
	signals := ExtractPodSignals(pod)
	assert.Empty(t, signals)
}

func TestExtractPodSignals_Evicted(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Phase:  corev1.PodFailed,
			Reason: "Evicted",
		},
	}
	signals := ExtractPodSignals(pod)
	assert.Len(t, signals, 1)
	assert.Equal(t, v1alpha1.TriggerEvicted, signals[0].Type)
	assert.Equal(t, v1alpha1.SeverityHigh, signals[0].Severity)
}

func TestExtractPodSignals_MultipleContainers(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "runtime",
					RestartCount: 2,
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
					},
				},
				{
					Name:         "sidecar",
					RestartCount: 4,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				},
			},
		},
	}
	signals := ExtractPodSignals(pod)
	assert.Len(t, signals, 2)
}
