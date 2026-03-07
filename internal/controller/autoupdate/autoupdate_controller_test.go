package autoupdate_test

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	"github.com/Prismer-AI/k8s4claw/internal/controller"
)

// mockTagLister is a test double for registry.RegistryClient.
type mockTagLister struct {
	tags []string
	err  error
}

func (m *mockTagLister) ListTags(_ context.Context, _ string) ([]string, error) {
	return m.tags, m.err
}

func newTestAutoUpdateReconciler(objs []runtime.Object, lister controller.TagLister) *controller.AutoUpdateReconciler {
	scheme := runtime.NewScheme()
	_ = clawv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(&clawv1alpha1.Claw{}).
		Build()

	return &controller.AutoUpdateReconciler{
		Client:    client,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: lister,
	}
}

func TestAutoUpdate_SkipsWhenDisabled(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
		},
	}

	r := newTestAutoUpdateReconciler([]runtime.Object{claw}, &mockTagLister{})
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter > 0 {
		t.Errorf("expected no requeue, got %+v", result)
	}
}

func TestAutoUpdate_SkipsDigestPinnedImage(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			Annotations: map[string]string{
				"claw.prismer.ai/target-image": "ghcr.io/prismer-ai/k8s4claw-openclaw@sha256:abc123",
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:    clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{Enabled: true, VersionConstraint: "^1.0.0"},
		},
	}

	r := newTestAutoUpdateReconciler([]runtime.Object{claw}, &mockTagLister{tags: []string{"1.1.0"}})
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func TestAutoUpdate_FindsNewVersion(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:           true,
				VersionConstraint: "^1.0.0",
				Schedule:          "* * * * *",
			},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{
				CurrentVersion: "1.0.0",
			},
		},
	}

	lister := &mockTagLister{tags: []string{"1.0.0", "1.1.0", "1.2.0", "latest"}}
	r := newTestAutoUpdateReconciler([]runtime.Object{claw}, lister)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var updated clawv1alpha1.Claw
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if updated.Status.AutoUpdate == nil {
		t.Fatal("AutoUpdate status is nil")
	}
	if updated.Status.AutoUpdate.AvailableVersion != "1.2.0" {
		t.Errorf("AvailableVersion = %q, want 1.2.0", updated.Status.AutoUpdate.AvailableVersion)
	}
	ann := updated.Annotations["claw.prismer.ai/target-image"]
	if ann == "" {
		t.Error("target-image annotation not set")
	}
}

func TestAutoUpdate_CircuitBreakerBlocks(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:           true,
				VersionConstraint: "^1.0.0",
				MaxRollbacks:      3,
			},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{
				CurrentVersion: "1.0.0",
				RollbackCount:  3,
				CircuitOpen:    true,
			},
		},
	}

	lister := &mockTagLister{tags: []string{"1.1.0"}}
	r := newTestAutoUpdateReconciler([]runtime.Object{claw}, lister)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var updated clawv1alpha1.Claw
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ann := updated.Annotations["claw.prismer.ai/target-image"]; ann != "" {
		t.Errorf("target-image should not be set when circuit is open, got %q", ann)
	}
}

func TestAutoUpdate_HealthCheckSuccess(t *testing.T) {
	now := metav1.Now()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			Annotations: map[string]string{
				"claw.prismer.ai/target-image":   "ghcr.io/prismer-ai/k8s4claw-openclaw:1.2.0",
				"claw.prismer.ai/update-phase":   "HealthCheck",
				"claw.prismer.ai/update-started": now.Format(time.RFC3339),
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:           true,
				VersionConstraint: "^1.0.0",
				HealthTimeout:     "10m",
			},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{
				CurrentVersion:   "1.0.0",
				AvailableVersion: "1.2.0",
			},
		},
	}

	replicas := int32(1)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &replicas},
		Status: appsv1.StatefulSetStatus{
			Replicas:        1,
			ReadyReplicas:   1,
			UpdatedReplicas: 1,
		},
	}

	r := newTestAutoUpdateReconciler([]runtime.Object{claw, sts}, &mockTagLister{})
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var updated clawv1alpha1.Claw
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if updated.Status.AutoUpdate.CurrentVersion != "1.2.0" {
		t.Errorf("CurrentVersion = %q, want 1.2.0", updated.Status.AutoUpdate.CurrentVersion)
	}
	if updated.Annotations["claw.prismer.ai/update-phase"] != "" {
		t.Errorf("update-phase should be cleared, got %q", updated.Annotations["claw.prismer.ai/update-phase"])
	}
}

func TestAutoUpdate_HealthCheckTimeout_Rollback(t *testing.T) {
	pastTime := metav1.NewTime(time.Now().Add(-15 * time.Minute))
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			Annotations: map[string]string{
				"claw.prismer.ai/target-image":   "ghcr.io/prismer-ai/k8s4claw-openclaw:1.2.0",
				"claw.prismer.ai/update-phase":   "HealthCheck",
				"claw.prismer.ai/update-started": pastTime.Format(time.RFC3339),
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:           true,
				VersionConstraint: "^1.0.0",
				HealthTimeout:     "10m",
				MaxRollbacks:      3,
			},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{
				CurrentVersion:   "1.0.0",
				AvailableVersion: "1.2.0",
				RollbackCount:    0,
			},
		},
	}

	replicas := int32(1)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &replicas},
		Status: appsv1.StatefulSetStatus{
			Replicas:        1,
			ReadyReplicas:   0,
			UpdatedReplicas: 0,
		},
	}

	r := newTestAutoUpdateReconciler([]runtime.Object{claw, sts}, &mockTagLister{})
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var updated clawv1alpha1.Claw
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if updated.Status.AutoUpdate.RollbackCount != 1 {
		t.Errorf("RollbackCount = %d, want 1", updated.Status.AutoUpdate.RollbackCount)
	}
	if len(updated.Status.AutoUpdate.FailedVersions) != 1 || updated.Status.AutoUpdate.FailedVersions[0] != "1.2.0" {
		t.Errorf("FailedVersions = %v, want [1.2.0]", updated.Status.AutoUpdate.FailedVersions)
	}
	if ann := updated.Annotations["claw.prismer.ai/target-image"]; ann != "" {
		t.Errorf("target-image should be cleared on rollback, got %q", ann)
	}
}
