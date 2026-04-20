package v1alpha1

import (
	"testing"
)

func TestEscalationPhases_AllExistAndUnique(t *testing.T) {
	phases := []EscalationPhase{
		EscalationPhasePending,
		EscalationPhaseAutoExecuted,
		EscalationPhaseAnalyzing,
		EscalationPhaseProposed,
		EscalationPhaseAwaitingApproval,
		EscalationPhaseApproved,
		EscalationPhaseExecuted,
		EscalationPhaseRejected,
		EscalationPhaseFailed,
	}

	if len(phases) != 9 {
		t.Fatalf("expected 9 escalation phases, got %d", len(phases))
	}

	seen := make(map[EscalationPhase]bool, len(phases))
	for _, p := range phases {
		if p == "" {
			t.Error("phase must not be empty string")
		}
		if seen[p] {
			t.Errorf("duplicate phase: %s", p)
		}
		seen[p] = true
	}
}

func TestIsTerminalPhase(t *testing.T) {
	terminal := []EscalationPhase{
		EscalationPhaseAutoExecuted,
		EscalationPhaseExecuted,
		EscalationPhaseRejected,
		EscalationPhaseFailed,
	}
	nonTerminal := []EscalationPhase{
		EscalationPhasePending,
		EscalationPhaseAnalyzing,
		EscalationPhaseProposed,
		EscalationPhaseAwaitingApproval,
		EscalationPhaseApproved,
	}

	for _, p := range terminal {
		if !IsTerminalPhase(p) {
			t.Errorf("expected %s to be terminal", p)
		}
	}
	for _, p := range nonTerminal {
		if IsTerminalPhase(p) {
			t.Errorf("expected %s to be non-terminal", p)
		}
	}
}

func TestSeverityRank_Ordering(t *testing.T) {
	if SeverityRank(SeverityCritical) <= SeverityRank(SeverityHigh) {
		t.Error("Critical must rank higher than High")
	}
	if SeverityRank(SeverityHigh) <= SeverityRank(SeverityMedium) {
		t.Error("High must rank higher than Medium")
	}
	if SeverityRank(SeverityMedium) <= SeverityRank(SeverityLow) {
		t.Error("Medium must rank higher than Low")
	}
	if SeverityRank(SeverityLow) <= 0 {
		t.Error("Low must rank above zero")
	}
}

func TestSeverityRank_UnknownReturnsZero(t *testing.T) {
	if rank := SeverityRank("bogus"); rank != 0 {
		t.Errorf("expected unknown severity rank 0, got %d", rank)
	}
}

func TestTriggerTypes_AllExist(t *testing.T) {
	triggers := []TriggerType{
		TriggerOOMKilled,
		TriggerCrashLoop,
		TriggerHighCPU,
		TriggerHighMemory,
		TriggerPodPending,
		TriggerProbeFailure,
		TriggerChannelDisconnect,
		TriggerEvicted,
		TriggerUnknown,
	}

	if len(triggers) != 9 {
		t.Fatalf("expected 9 trigger types, got %d", len(triggers))
	}

	seen := make(map[TriggerType]bool, len(triggers))
	for _, tr := range triggers {
		if tr == "" {
			t.Error("trigger type must not be empty string")
		}
		if seen[tr] {
			t.Errorf("duplicate trigger type: %s", tr)
		}
		seen[tr] = true
	}
}

func TestRuntimeK8sOps_Exists(t *testing.T) {
	rt := RuntimeK8sOps
	if rt != "k8sops" {
		t.Errorf("expected RuntimeK8sOps to be %q, got %q", "k8sops", rt)
	}
}
