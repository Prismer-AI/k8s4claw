package runtime

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestHermesClawValidate_RequiresCredentials(t *testing.T) {
	t.Parallel()

	a := &HermesClawAdapter{}
	errs := a.Validate(context.Background(), &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeHermesClaw,
	})
	if len(errs) == 0 {
		t.Fatal("expected error when credentials are missing")
	}
	if errs[0].Field != "spec.credentials" {
		t.Fatalf("field = %q, want spec.credentials", errs[0].Field)
	}
}

func TestHermesClawValidate_RejectsChannels(t *testing.T) {
	t.Parallel()

	a := &HermesClawAdapter{}
	errs := a.Validate(context.Background(), &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeHermesClaw,
		Credentials: &v1alpha1.CredentialSpec{
			SecretRef: &corev1.LocalObjectReference{Name: "hermes-creds"},
		},
		Channels: []v1alpha1.ChannelRef{{Name: "slack-team"}},
	})
	if len(errs) == 0 {
		t.Fatal("expected error when channels are configured")
	}
	found := false
	for _, err := range errs {
		if err.Field == "spec.channels" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected spec.channels validation error, got %v", errs)
	}
}

func TestHermesClawValidate_RequiresHermesMountPaths(t *testing.T) {
	t.Parallel()

	a := &HermesClawAdapter{}
	errs := a.Validate(context.Background(), &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeHermesClaw,
		Credentials: &v1alpha1.CredentialSpec{
			SecretRef: &corev1.LocalObjectReference{Name: "hermes-creds"},
		},
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{
				Enabled:   true,
				Size:      "5Gi",
				MountPath: "/data/memory",
			},
			Workspace: &v1alpha1.VolumeSpec{
				Enabled:   true,
				Size:      "10Gi",
				MountPath: "/data/skills",
			},
		},
	})
	if len(errs) != 2 {
		t.Fatalf("expected 2 mountPath errors, got %d: %v", len(errs), errs)
	}
}

func TestHermesClawValidate_AcceptsSupportedPaths(t *testing.T) {
	t.Parallel()

	a := &HermesClawAdapter{}
	errs := a.Validate(context.Background(), &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeHermesClaw,
		Credentials: &v1alpha1.CredentialSpec{
			SecretRef: &corev1.LocalObjectReference{Name: "hermes-creds"},
		},
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{
				Enabled:   true,
				Size:      "5Gi",
				MountPath: "/opt/data",
			},
			Workspace: &v1alpha1.VolumeSpec{
				Enabled:   true,
				Size:      "20Gi",
				MountPath: "/opt/data/skills",
			},
		},
	})
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestHermesClawPodTemplate_UsesWritableRootAndGatewayArgs(t *testing.T) {
	t.Parallel()

	a := &HermesClawAdapter{}
	tmpl := a.PodTemplate(&v1alpha1.Claw{
		Spec: v1alpha1.ClawSpec{
			Runtime: v1alpha1.RuntimeHermesClaw,
		},
	})

	if len(tmpl.Spec.Containers) != 1 {
		t.Fatalf("expected 1 runtime container, got %d", len(tmpl.Spec.Containers))
	}
	runtime := tmpl.Spec.Containers[0]
	if len(runtime.Args) != 2 || runtime.Args[0] != "gateway" || runtime.Args[1] != "run" {
		t.Fatalf("runtime args = %v, want [gateway run]", runtime.Args)
	}
	if runtime.SecurityContext == nil || runtime.SecurityContext.ReadOnlyRootFilesystem == nil || *runtime.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatal("expected writable root filesystem for Hermes runtime")
	}
	if runtime.SecurityContext.RunAsUser == nil || *runtime.SecurityContext.RunAsUser != 10000 {
		t.Fatalf("runtime RunAsUser = %v, want 10000", runtime.SecurityContext.RunAsUser)
	}

	foundConfigMount := false
	for _, mount := range runtime.VolumeMounts {
		if mount.Name == "config-vol" && mount.MountPath == "/opt/data/config.yaml" && mount.SubPath == "config.json" {
			foundConfigMount = true
			break
		}
	}
	if !foundConfigMount {
		t.Fatal("expected Hermes config-vol subPath mount at /opt/data/config.yaml")
	}
}

func TestHermesClawValidateUpdate_UsesStandardPersistenceRules(t *testing.T) {
	t.Parallel()

	a := &HermesClawAdapter{}
	errs := a.ValidateUpdate(context.Background(),
		&v1alpha1.ClawSpec{
			Runtime: v1alpha1.RuntimeHermesClaw,
			Persistence: &v1alpha1.PersistenceSpec{
				Session: &v1alpha1.VolumeSpec{
					Enabled:      true,
					Size:         "10Gi",
					MountPath:    "/opt/data",
					StorageClass: "gp3",
				},
			},
		},
		&v1alpha1.ClawSpec{
			Runtime: v1alpha1.RuntimeHermesClaw,
			Persistence: &v1alpha1.PersistenceSpec{
				Session: &v1alpha1.VolumeSpec{
					Enabled:      true,
					Size:         "5Gi",
					MountPath:    "/opt/data",
					StorageClass: "gp3",
				},
			},
		},
	)
	if len(errs) == 0 {
		t.Fatal("expected error when Hermes session PVC shrinks")
	}
}
