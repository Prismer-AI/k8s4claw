package webhook

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

func newRegistry() *clawruntime.Registry {
	reg := clawruntime.NewRegistry()
	reg.Register(clawv1alpha1.RuntimeOpenClaw, &clawruntime.OpenClawAdapter{})
	reg.Register(clawv1alpha1.RuntimeNanoClaw, &clawruntime.NanoClawAdapter{})
	reg.Register(clawv1alpha1.RuntimeZeroClaw, &clawruntime.ZeroClawAdapter{})
	reg.Register(clawv1alpha1.RuntimePicoClaw, &clawruntime.PicoClawAdapter{})
	return reg
}

func baseClaw() *clawv1alpha1.Claw {
	return &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
		},
	}
}

func TestValidateCreate_Valid(t *testing.T) {
	v := &ClawValidator{Registry: newRegistry()}
	warnings, err := v.ValidateCreate(context.Background(), baseClaw())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
}

func TestValidateCreate_CredentialExclusivity(t *testing.T) {
	v := &ClawValidator{Registry: newRegistry()}
	claw := baseClaw()
	claw.Spec.Credentials = &clawv1alpha1.CredentialSpec{
		SecretRef:      &corev1.LocalObjectReference{Name: "my-secret"},
		ExternalSecret: &clawv1alpha1.ExternalSecretRef{Provider: "vault", Store: "s", Path: "p"},
	}

	_, err := v.ValidateCreate(context.Background(), claw)
	if err == nil {
		t.Fatal("expected error for mutually exclusive credentials, got nil")
	}
	if !containsFieldError(err.Error(), "credentials") {
		t.Fatalf("error should mention credentials: %v", err)
	}
}

func TestValidateCreate_PVCSizeFormat(t *testing.T) {
	tests := []struct {
		name      string
		size      string
		maxSize   string
		wantError bool
	}{
		{"valid size", "2Gi", "", false},
		{"valid size and maxSize", "2Gi", "20Gi", false},
		{"invalid size", "not-a-size", "", true},
		{"valid size invalid maxSize", "2Gi", "bad", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := &ClawValidator{Registry: newRegistry()}
			claw := baseClaw()
			claw.Spec.Persistence = &clawv1alpha1.PersistenceSpec{
				Session: &clawv1alpha1.VolumeSpec{
					Enabled:   true,
					Size:      tc.size,
					MaxSize:   tc.maxSize,
					MountPath: "/data/session",
				},
			}

			_, err := v.ValidateCreate(context.Background(), claw)
			if tc.wantError && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantError && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

func TestValidateCreate_PVCSizeFormat_AllVolumes(t *testing.T) {
	v := &ClawValidator{Registry: newRegistry()}
	claw := baseClaw()
	claw.Spec.Persistence = &clawv1alpha1.PersistenceSpec{
		Session: &clawv1alpha1.VolumeSpec{
			Enabled: true, Size: "invalid", MountPath: "/s",
		},
		Output: &clawv1alpha1.OutputVolumeSpec{
			VolumeSpec: clawv1alpha1.VolumeSpec{
				Enabled: true, Size: "also-invalid", MountPath: "/o",
			},
		},
		Workspace: &clawv1alpha1.VolumeSpec{
			Enabled: true, Size: "nope", MountPath: "/w",
		},
		Cache: &clawv1alpha1.CacheSpec{
			Enabled: true, Size: "bad", MountPath: "/c",
		},
	}

	_, err := v.ValidateCreate(context.Background(), claw)
	if err == nil {
		t.Fatal("expected compound errors, got nil")
	}
	// Should have errors for all 4 volumes.
	errStr := err.Error()
	for _, vol := range []string{"session", "output", "workspace", "cache"} {
		if !containsFieldError(errStr, vol) {
			t.Errorf("expected error mentioning %q, got: %s", vol, errStr)
		}
	}
}

func TestValidateCreate_UnsupportedRuntime(t *testing.T) {
	v := &ClawValidator{Registry: newRegistry()}
	claw := baseClaw()
	claw.Spec.Runtime = "unknown-runtime"

	_, err := v.ValidateCreate(context.Background(), claw)
	if err == nil {
		t.Fatal("expected error for unsupported runtime, got nil")
	}
}

func TestValidateCreate_RuntimeAdapterValidation(t *testing.T) {
	// Adapters currently return empty ErrorList (stubs), so validation passes.
	v := &ClawValidator{Registry: newRegistry()}
	claw := baseClaw()
	claw.Spec.Runtime = clawv1alpha1.RuntimeNanoClaw

	warnings, err := v.ValidateCreate(context.Background(), claw)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
}

func TestValidateUpdate_RuntimeImmutability(t *testing.T) {
	v := &ClawValidator{Registry: newRegistry()}
	oldObj := baseClaw()
	oldObj.Spec.Runtime = clawv1alpha1.RuntimeOpenClaw

	newObj := baseClaw()
	newObj.Spec.Runtime = clawv1alpha1.RuntimeNanoClaw

	_, err := v.ValidateUpdate(context.Background(), oldObj, newObj)
	if err == nil {
		t.Fatal("expected error for runtime change, got nil")
	}
	if !containsFieldError(err.Error(), "runtime") {
		t.Fatalf("error should mention runtime: %v", err)
	}
}

func TestValidateUpdate_SameRuntime(t *testing.T) {
	v := &ClawValidator{Registry: newRegistry()}
	oldObj := baseClaw()
	newObj := baseClaw()

	_, err := v.ValidateUpdate(context.Background(), oldObj, newObj)
	if err != nil {
		t.Fatalf("expected no error for same runtime, got %v", err)
	}
}

func TestValidateUpdate_CompoundErrors(t *testing.T) {
	v := &ClawValidator{Registry: newRegistry()}
	oldObj := baseClaw()
	oldObj.Spec.Runtime = clawv1alpha1.RuntimeOpenClaw

	newObj := baseClaw()
	newObj.Spec.Runtime = clawv1alpha1.RuntimeNanoClaw
	newObj.Spec.Credentials = &clawv1alpha1.CredentialSpec{
		SecretRef:      &corev1.LocalObjectReference{Name: "s"},
		ExternalSecret: &clawv1alpha1.ExternalSecretRef{Provider: "v", Store: "s", Path: "p"},
	}

	_, err := v.ValidateUpdate(context.Background(), oldObj, newObj)
	if err == nil {
		t.Fatal("expected compound errors, got nil")
	}
	errStr := err.Error()
	if !containsFieldError(errStr, "runtime") {
		t.Errorf("expected runtime error: %s", errStr)
	}
	if !containsFieldError(errStr, "credentials") {
		t.Errorf("expected credentials error: %s", errStr)
	}
}

func TestValidateDelete_NoOp(t *testing.T) {
	v := &ClawValidator{Registry: newRegistry()}
	warnings, err := v.ValidateDelete(context.Background(), baseClaw())
	if err != nil {
		t.Fatalf("expected no error on delete, got %v", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
}

func TestValidateVolumeSize(t *testing.T) {
	tests := []struct {
		name      string
		vol       *clawv1alpha1.VolumeSpec
		wantCount int
	}{
		{
			name:      "valid",
			vol:       &clawv1alpha1.VolumeSpec{Size: "1Gi", MountPath: "/data"},
			wantCount: 0,
		},
		{
			name:      "invalid size",
			vol:       &clawv1alpha1.VolumeSpec{Size: "xyz", MountPath: "/data"},
			wantCount: 1,
		},
		{
			name:      "invalid maxSize",
			vol:       &clawv1alpha1.VolumeSpec{Size: "1Gi", MaxSize: "abc", MountPath: "/data"},
			wantCount: 1,
		},
		{
			name:      "both invalid",
			vol:       &clawv1alpha1.VolumeSpec{Size: "bad", MaxSize: "worse", MountPath: "/data"},
			wantCount: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateVolumeSize(field.NewPath("test"), tc.vol)
			if len(errs) != tc.wantCount {
				t.Errorf("expected %d errors, got %d: %v", tc.wantCount, len(errs), errs)
			}
		})
	}
}

func TestValidateCredentialExclusivity_NilCredentials(t *testing.T) {
	errs := validateCredentialExclusivity(baseClaw())
	if len(errs) > 0 {
		t.Fatalf("expected no errors for nil credentials, got %v", errs)
	}
}

func TestValidateCredentialExclusivity_OnlySecretRef(t *testing.T) {
	claw := baseClaw()
	claw.Spec.Credentials = &clawv1alpha1.CredentialSpec{
		SecretRef: &corev1.LocalObjectReference{Name: "s"},
	}
	errs := validateCredentialExclusivity(claw)
	if len(errs) > 0 {
		t.Fatalf("expected no errors for only secretRef, got %v", errs)
	}
}

func TestValidateCredentialExclusivity_OnlyExternalSecret(t *testing.T) {
	claw := baseClaw()
	claw.Spec.Credentials = &clawv1alpha1.CredentialSpec{
		ExternalSecret: &clawv1alpha1.ExternalSecretRef{Provider: "v", Store: "s", Path: "p"},
	}
	errs := validateCredentialExclusivity(claw)
	if len(errs) > 0 {
		t.Fatalf("expected no errors for only externalSecret, got %v", errs)
	}
}

func TestValidatePVCSizes_NilPersistence(t *testing.T) {
	errs := validatePVCSizes(baseClaw())
	if len(errs) > 0 {
		t.Fatalf("expected no errors for nil persistence, got %v", errs)
	}
}

func TestValidatePVCSizes_DisabledVolumes(t *testing.T) {
	claw := baseClaw()
	claw.Spec.Persistence = &clawv1alpha1.PersistenceSpec{
		Session: &clawv1alpha1.VolumeSpec{
			Enabled: false, Size: "invalid", MountPath: "/s",
		},
	}
	errs := validatePVCSizes(claw)
	if len(errs) > 0 {
		t.Fatalf("expected no errors for disabled volumes, got %v", errs)
	}
}

func containsFieldError(errStr, fieldName string) bool {
	return len(errStr) > 0 && contains(errStr, fieldName)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestValidateAutoUpdate_InvalidConstraint(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:           true,
				VersionConstraint: "not-a-semver",
			},
		},
	}
	v := &ClawValidator{Registry: newRegistry()}
	_, err := v.ValidateCreate(context.Background(), claw)
	if err == nil {
		t.Error("expected error for invalid version constraint")
	}
}

func TestValidateAutoUpdate_InvalidSchedule(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:  true,
				Schedule: "not-a-cron",
			},
		},
	}
	v := &ClawValidator{Registry: newRegistry()}
	_, err := v.ValidateCreate(context.Background(), claw)
	if err == nil {
		t.Error("expected error for invalid schedule")
	}
}

func TestValidateAutoUpdate_HealthTimeoutRange(t *testing.T) {
	tests := []struct {
		name    string
		timeout string
		wantErr bool
	}{
		{"valid 5m", "5m", false},
		{"valid 10m", "10m", false},
		{"too short", "1m", true},
		{"too long", "1h", true},
		{"invalid", "not-a-duration", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claw := &clawv1alpha1.Claw{
				Spec: clawv1alpha1.ClawSpec{
					Runtime: clawv1alpha1.RuntimeOpenClaw,
					AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
						Enabled:       true,
						HealthTimeout: tt.timeout,
					},
				},
			}
			v := &ClawValidator{Registry: newRegistry()}
			_, err := v.ValidateCreate(context.Background(), claw)
			if (err != nil) != tt.wantErr {
				t.Errorf("wantErr = %v, got err = %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidateAutoUpdate_MaxRollbacksPositive(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:      true,
				MaxRollbacks: -1,
			},
		},
	}
	v := &ClawValidator{Registry: newRegistry()}
	_, err := v.ValidateCreate(context.Background(), claw)
	if err == nil {
		t.Error("expected error for negative maxRollbacks")
	}
}

func TestValidateAutoUpdate_Disabled_NoValidation(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:           false,
				VersionConstraint: "invalid",
			},
		},
	}
	v := &ClawValidator{Registry: newRegistry()}
	_, err := v.ValidateCreate(context.Background(), claw)
	if err != nil {
		t.Errorf("expected no errors when disabled, got %v", err)
	}
}
