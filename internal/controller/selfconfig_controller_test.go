package controller

import (
	"testing"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestValidateActions_Allowed(t *testing.T) {
	r := &ClawSelfConfigReconciler{}
	sc := &clawv1alpha1.ClawSelfConfig{}
	sc.Spec.AddSkills = []string{"tool-use"}
	sc.Spec.ConfigPatch = map[string]string{"model": "claude-sonnet-4"}

	err := r.validateActions(sc, []string{"skills", "config"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateActions_Denied(t *testing.T) {
	r := &ClawSelfConfigReconciler{}
	sc := &clawv1alpha1.ClawSelfConfig{}
	sc.Spec.AddSkills = []string{"tool-use"}

	err := r.validateActions(sc, []string{"config"}) // skills not allowed
	if err == nil {
		t.Fatal("expected error for denied skills action")
	}
}

func TestValidateActions_QuantityLimit(t *testing.T) {
	r := &ClawSelfConfigReconciler{}
	sc := &clawv1alpha1.ClawSelfConfig{}

	skills := make([]string, 11) // exceeds max 10
	for i := range skills {
		skills[i] = "skill"
	}
	sc.Spec.AddSkills = skills

	err := r.validateActions(sc, []string{"skills"})
	if err == nil {
		t.Fatal("expected error for exceeding quantity limit")
	}
}

func TestValidateActions_Empty(t *testing.T) {
	r := &ClawSelfConfigReconciler{}
	sc := &clawv1alpha1.ClawSelfConfig{}

	// No actions requested — should pass with any allowlist.
	err := r.validateActions(sc, nil)
	if err != nil {
		t.Fatalf("expected no error for empty actions, got: %v", err)
	}
}

func TestValidateActions_AllCategories(t *testing.T) {
	r := &ClawSelfConfigReconciler{}
	sc := &clawv1alpha1.ClawSelfConfig{}
	sc.Spec.AddSkills = []string{"a"}
	sc.Spec.ConfigPatch = map[string]string{"k": "v"}
	sc.Spec.AddWorkspaceFiles = map[string]string{"f": "c"}
	sc.Spec.AddEnvVars = []clawv1alpha1.EnvVar{{Name: "X", Value: "1"}}

	err := r.validateActions(sc, []string{"skills", "config", "workspaceFiles", "envVars"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Missing envVars.
	err = r.validateActions(sc, []string{"skills", "config", "workspaceFiles"})
	if err == nil {
		t.Fatal("expected error for missing envVars")
	}
}
