package webhook

import (
	"context"
	"fmt"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

// ClawValidator implements admission.Validator for the Claw CRD.
type ClawValidator struct {
	Registry *clawruntime.Registry
}

func (v *ClawValidator) ValidateCreate(ctx context.Context, obj *clawv1alpha1.Claw) (admission.Warnings, error) {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateCredentialExclusivity(obj)...)
	allErrs = append(allErrs, validatePVCSizes(obj)...)
	allErrs = append(allErrs, validateAutoUpdate(obj)...)
	allErrs = append(allErrs, v.validateRuntime(ctx, obj)...)

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, nil
}

func (v *ClawValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *clawv1alpha1.Claw) (admission.Warnings, error) {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, validateRuntimeImmutability(oldObj, newObj)...)
	allErrs = append(allErrs, validateCredentialExclusivity(newObj)...)
	allErrs = append(allErrs, validatePVCSizes(newObj)...)
	allErrs = append(allErrs, validateAutoUpdate(newObj)...)
	allErrs = append(allErrs, v.validateRuntimeUpdate(ctx, oldObj, newObj)...)

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, nil
}

func (v *ClawValidator) ValidateDelete(_ context.Context, _ *clawv1alpha1.Claw) (admission.Warnings, error) {
	return nil, nil
}

// validateRuntimeImmutability ensures spec.runtime cannot change after creation.
func validateRuntimeImmutability(oldObj, newObj *clawv1alpha1.Claw) field.ErrorList {
	if oldObj.Spec.Runtime != newObj.Spec.Runtime {
		return field.ErrorList{
			field.Forbidden(
				field.NewPath("spec", "runtime"),
				fmt.Sprintf("runtime is immutable after creation (was %q, got %q)", oldObj.Spec.Runtime, newObj.Spec.Runtime),
			),
		}
	}
	return nil
}

// validateCredentialExclusivity ensures secretRef and externalSecret are mutually exclusive.
func validateCredentialExclusivity(obj *clawv1alpha1.Claw) field.ErrorList {
	creds := obj.Spec.Credentials
	if creds == nil {
		return nil
	}
	if creds.SecretRef != nil && creds.ExternalSecret != nil {
		return field.ErrorList{
			field.Invalid(
				field.NewPath("spec", "credentials"),
				"secretRef + externalSecret",
				"secretRef and externalSecret are mutually exclusive",
			),
		}
	}
	return nil
}

// validatePVCSizes validates that all persistence size fields are valid K8s quantities.
func validatePVCSizes(obj *clawv1alpha1.Claw) field.ErrorList {
	p := obj.Spec.Persistence
	if p == nil {
		return nil
	}

	var allErrs field.ErrorList
	basePath := field.NewPath("spec", "persistence")

	if p.Session != nil && p.Session.Enabled {
		allErrs = append(allErrs, validateVolumeSize(basePath.Child("session"), p.Session)...)
	}
	if p.Output != nil && p.Output.Enabled {
		allErrs = append(allErrs, validateVolumeSize(basePath.Child("output"), &p.Output.VolumeSpec)...)
	}
	if p.Workspace != nil && p.Workspace.Enabled {
		allErrs = append(allErrs, validateVolumeSize(basePath.Child("workspace"), p.Workspace)...)
	}
	if p.Cache != nil && p.Cache.Enabled {
		cachePath := basePath.Child("cache")
		if _, err := resource.ParseQuantity(p.Cache.Size); err != nil {
			allErrs = append(allErrs, field.Invalid(cachePath.Child("size"), p.Cache.Size, "must be a valid Kubernetes quantity"))
		}
	}

	return allErrs
}

// validateVolumeSize validates size and maxSize fields on a VolumeSpec.
func validateVolumeSize(fldPath *field.Path, vol *clawv1alpha1.VolumeSpec) field.ErrorList {
	var allErrs field.ErrorList

	if _, err := resource.ParseQuantity(vol.Size); err != nil {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("size"), vol.Size, "must be a valid Kubernetes quantity"))
	}
	if vol.MaxSize != "" {
		if _, err := resource.ParseQuantity(vol.MaxSize); err != nil {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("maxSize"), vol.MaxSize, "must be a valid Kubernetes quantity"))
		}
	}

	return allErrs
}

// validateRuntime delegates CREATE validation to the runtime adapter.
func (v *ClawValidator) validateRuntime(ctx context.Context, obj *clawv1alpha1.Claw) field.ErrorList {
	adapter, ok := v.Registry.Get(obj.Spec.Runtime)
	if !ok {
		return field.ErrorList{
			field.NotSupported(field.NewPath("spec", "runtime"), obj.Spec.Runtime, []string{
				string(clawv1alpha1.RuntimeOpenClaw),
				string(clawv1alpha1.RuntimeNanoClaw),
				string(clawv1alpha1.RuntimeZeroClaw),
				string(clawv1alpha1.RuntimePicoClaw),
			}),
		}
	}
	return adapter.Validate(ctx, &obj.Spec)
}

// validateAutoUpdate validates auto-update spec fields.
func validateAutoUpdate(obj *clawv1alpha1.Claw) field.ErrorList {
	au := obj.Spec.AutoUpdate
	if au == nil || !au.Enabled {
		return nil
	}

	var allErrs field.ErrorList
	basePath := field.NewPath("spec", "autoUpdate")

	if au.VersionConstraint != "" {
		if _, err := semver.NewConstraint(au.VersionConstraint); err != nil {
			allErrs = append(allErrs, field.Invalid(
				basePath.Child("versionConstraint"),
				au.VersionConstraint,
				"must be a valid semver constraint (e.g., ^1.0.0, ~1.x)",
			))
		}
	}

	if au.Schedule != "" {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(au.Schedule); err != nil {
			allErrs = append(allErrs, field.Invalid(
				basePath.Child("schedule"),
				au.Schedule,
				"must be a valid cron expression",
			))
		}
	}

	if au.HealthTimeout != "" {
		d, err := time.ParseDuration(au.HealthTimeout)
		if err != nil {
			allErrs = append(allErrs, field.Invalid(
				basePath.Child("healthTimeout"),
				au.HealthTimeout,
				"must be a valid duration",
			))
		} else if d < 2*time.Minute || d > 30*time.Minute {
			allErrs = append(allErrs, field.Invalid(
				basePath.Child("healthTimeout"),
				au.HealthTimeout,
				"must be between 2m and 30m",
			))
		}
	}

	if au.MaxRollbacks < 0 {
		allErrs = append(allErrs, field.Invalid(
			basePath.Child("maxRollbacks"),
			au.MaxRollbacks,
			"must be >= 0",
		))
	}

	return allErrs
}

// validateRuntimeUpdate delegates UPDATE validation to the runtime adapter.
func (v *ClawValidator) validateRuntimeUpdate(ctx context.Context, oldObj, newObj *clawv1alpha1.Claw) field.ErrorList {
	adapter, ok := v.Registry.Get(newObj.Spec.Runtime)
	if !ok {
		// Already caught by validateRuntime or validateRuntimeImmutability.
		return nil
	}
	return adapter.ValidateUpdate(ctx, &oldObj.Spec, &newObj.Spec)
}
