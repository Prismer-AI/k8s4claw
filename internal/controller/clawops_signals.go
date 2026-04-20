package controller

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	"github.com/Prismer-AI/k8s4claw/internal/rules"
)

// ExtractPodSignals extracts anomaly signals from a Pod's status.
func ExtractPodSignals(pod *corev1.Pod) []rules.Signal {
	var signals []rules.Signal

	// Check pod-level eviction.
	if pod.Status.Phase == corev1.PodFailed && pod.Status.Reason == "Evicted" {
		signals = append(signals, rules.Signal{
			Type:     v1alpha1.TriggerEvicted,
			Severity: v1alpha1.SeverityHigh,
			Count:    1,
			Message:  fmt.Sprintf("Pod %s evicted: %s", pod.Name, pod.Status.Message),
			Source:   "pod-status",
		})
		return signals
	}

	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		// OOMKilled: check last termination state.
		if cs.LastTerminationState.Terminated != nil &&
			cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
			signals = append(signals, rules.Signal{
				Type:     v1alpha1.TriggerOOMKilled,
				Severity: v1alpha1.SeverityHigh,
				Count:    cs.RestartCount,
				Message:  fmt.Sprintf("Container %s OOMKilled (exit 137), restarts: %d", cs.Name, cs.RestartCount),
				Source:   "pod-status",
			})
			continue
		}

		// CrashLoopBackOff: check waiting state.
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
			signals = append(signals, rules.Signal{
				Type:     v1alpha1.TriggerCrashLoop,
				Severity: v1alpha1.SeverityHigh,
				Count:    cs.RestartCount,
				Message:  fmt.Sprintf("Container %s in CrashLoopBackOff, restarts: %d", cs.Name, cs.RestartCount),
				Source:   "pod-status",
			})
			continue
		}
	}

	return signals
}
