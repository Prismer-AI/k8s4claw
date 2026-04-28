package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestReconcile_InitiatesUpdate(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:           true,
				VersionConstraint: ">=0.0.0",
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{tags: []string{"1.0.0", "1.1.0"}},
		Clock:     &testClock{now: time.Now()},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != healthCheckPollInterval {
		t.Errorf("RequeueAfter = %v; want %v", result.RequeueAfter, healthCheckPollInterval)
	}

	// Verify annotations were set.
	var updated clawv1alpha1.Claw
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[annotationUpdatePhase] != "HealthCheck" {
		t.Errorf("phase = %q; want HealthCheck", updated.Annotations[annotationUpdatePhase])
	}
	if updated.Annotations[annotationTargetImage] == "" {
		t.Error("expected target-image annotation")
	}
}

func TestReconcile_SkipsDigestPinned(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test", Namespace: "default",
			Annotations: map[string]string{
				annotationTargetImage: "ghcr.io/prismer-ai/k8s4claw-openclaw@sha256:abc123",
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled: true,
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{tags: []string{"1.0.0"}},
		Clock:     &testClock{now: time.Now()},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should requeue at next cron, not initiate update.
	if result.RequeueAfter == healthCheckPollInterval {
		t.Error("should not enter health check for digest-pinned image")
	}
}

func TestReconcile_HealthCheckPassed(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()
	now := time.Now()

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test", Namespace: "default",
			Annotations: map[string]string{
				annotationUpdatePhase: "HealthCheck",
				annotationTargetImage: "ghcr.io/prismer-ai/k8s4claw-openclaw:1.1.0",
				annotationUpdateStart: now.Add(-1 * time.Minute).Format(time.RFC3339),
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled: true,
			},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{
				AvailableVersion: "1.1.0",
			},
		},
	}

	// StatefulSet with all replicas ready.
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
			},
		},
		Status: appsv1.StatefulSetStatus{
			UpdatedReplicas: 1,
			ReadyReplicas:   1,
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw, sts).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{},
		Clock:     &testClock{now: now},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should requeue at next cron (not healthCheckPollInterval).
	if result.RequeueAfter == healthCheckPollInterval {
		t.Error("should not continue health checking after success")
	}

	// Verify status updated.
	var updated clawv1alpha1.Claw
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.AutoUpdate == nil || updated.Status.AutoUpdate.CurrentVersion != "1.1.0" {
		t.Error("expected CurrentVersion=1.1.0")
	}
}

func TestReconcile_HealthCheckTimeout_Rollback(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()
	now := time.Now()

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test", Namespace: "default",
			Annotations: map[string]string{
				annotationUpdatePhase: "HealthCheck",
				annotationTargetImage: "ghcr.io/prismer-ai/k8s4claw-openclaw:1.1.0",
				annotationUpdateStart: now.Add(-15 * time.Minute).Format(time.RFC3339),
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled: true,
			},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{
				AvailableVersion: "1.1.0",
			},
		},
	}

	// STS not ready.
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
			},
		},
		Status: appsv1.StatefulSetStatus{
			UpdatedReplicas: 0,
			ReadyReplicas:   0,
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw, sts).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{},
		Clock:     &testClock{now: now}, // 15min elapsed > 10min default timeout
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify rollback: target-image annotation removed.
	var updated clawv1alpha1.Claw
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &updated); err != nil {
		t.Fatal(err)
	}
	if _, ok := updated.Annotations[annotationTargetImage]; ok {
		t.Error("expected target-image annotation to be removed after rollback")
	}
	if updated.Status.AutoUpdate == nil || updated.Status.AutoUpdate.RollbackCount != 1 {
		t.Error("expected RollbackCount=1")
	}
}

func TestReconcile_RollbackOpensCircuitBreaker(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()
	now := time.Now()

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test", Namespace: "default",
			Annotations: map[string]string{
				annotationUpdatePhase: "HealthCheck",
				annotationTargetImage: "ghcr.io/prismer-ai/k8s4claw-openclaw:1.1.0",
				annotationUpdateStart: now.Add(-15 * time.Minute).Format(time.RFC3339),
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:      true,
				MaxRollbacks: 2,
			},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{
				AvailableVersion: "1.1.0",
				RollbackCount:    1, // Already 1, threshold is 2 → will open
			},
		},
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw, sts).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{},
		Clock:     &testClock{now: now},
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated clawv1alpha1.Claw
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &updated); err != nil {
		t.Fatal(err)
	}
	if !updated.Status.AutoUpdate.CircuitOpen {
		t.Error("expected circuit breaker to be open")
	}
	if updated.Status.AutoUpdate.RollbackCount != 2 {
		t.Errorf("RollbackCount = %d; want 2", updated.Status.AutoUpdate.RollbackCount)
	}
}

func TestReconcile_TagListerError(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled: true,
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{err: fmt.Errorf("registry unavailable")},
		Clock:     &testClock{now: time.Now()},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should requeue after 5 minutes on registry error.
	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("RequeueAfter = %v; want 5m", result.RequeueAfter)
	}
}

func TestReconcile_HealthCheck_STSNotFound_WithinTimeout(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()
	now := time.Now()

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test", Namespace: "default",
			Annotations: map[string]string{
				annotationUpdatePhase: "HealthCheck",
				annotationTargetImage: "ghcr.io/prismer-ai/k8s4claw-openclaw:1.1.0",
				annotationUpdateStart: now.Add(-1 * time.Minute).Format(time.RFC3339),
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate:  &clawv1alpha1.AutoUpdateSpec{Enabled: true},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{AvailableVersion: "1.1.0"},
		},
	}

	// No STS exists.
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{},
		Clock:     &testClock{now: now},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != healthCheckPollInterval {
		t.Errorf("RequeueAfter = %v; want %v", result.RequeueAfter, healthCheckPollInterval)
	}
}

func TestReconcile_HealthCheck_STSNotFound_Timeout(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()
	now := time.Now()

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test", Namespace: "default",
			Annotations: map[string]string{
				annotationUpdatePhase: "HealthCheck",
				annotationTargetImage: "ghcr.io/prismer-ai/k8s4claw-openclaw:1.1.0",
				annotationUpdateStart: now.Add(-15 * time.Minute).Format(time.RFC3339),
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate:  &clawv1alpha1.AutoUpdateSpec{Enabled: true},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{AvailableVersion: "1.1.0"},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{},
		Clock:     &testClock{now: now},
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should rollback.
	var updated clawv1alpha1.Claw
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &updated); err != nil {
		t.Fatal(err)
	}
	if _, ok := updated.Annotations[annotationTargetImage]; ok {
		t.Error("expected annotations cleared after rollback")
	}
}

func TestReconcile_HealthCheck_InvalidStartTime(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test", Namespace: "default",
			Annotations: map[string]string{
				annotationUpdatePhase: "HealthCheck",
				annotationTargetImage: "ghcr.io/prismer-ai/k8s4claw-openclaw:1.1.0",
				annotationUpdateStart: "not-a-time",
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate:  &clawv1alpha1.AutoUpdateSpec{Enabled: true},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{AvailableVersion: "1.1.0"},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{},
		Clock:     &testClock{now: time.Now()},
	}

	// Should trigger rollback due to invalid start time.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReconcile_HealthCheck_WaitingForReplicas(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()
	now := time.Now()

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test", Namespace: "default",
			Annotations: map[string]string{
				annotationUpdatePhase: "HealthCheck",
				annotationTargetImage: "ghcr.io/prismer-ai/k8s4claw-openclaw:1.1.0",
				annotationUpdateStart: now.Add(-1 * time.Minute).Format(time.RFC3339),
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate:  &clawv1alpha1.AutoUpdateSpec{Enabled: true},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{AvailableVersion: "1.1.0"},
		},
	}

	// STS exists but replicas not ready yet.
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(2)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
			},
		},
		Status: appsv1.StatefulSetStatus{
			UpdatedReplicas: 1,
			ReadyReplicas:   1,
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw, sts).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{},
		Clock:     &testClock{now: now},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should continue polling.
	if result.RequeueAfter != healthCheckPollInterval {
		t.Errorf("RequeueAfter = %v; want %v", result.RequeueAfter, healthCheckPollInterval)
	}
}

func TestReconcile_HealthCheck_CustomTimeout(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()
	now := time.Now()

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test", Namespace: "default",
			Annotations: map[string]string{
				annotationUpdatePhase: "HealthCheck",
				annotationTargetImage: "ghcr.io/prismer-ai/k8s4claw-openclaw:1.1.0",
				annotationUpdateStart: now.Add(-25 * time.Minute).Format(time.RFC3339),
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:       true,
				HealthTimeout: "30m", // Custom longer timeout
			},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{AvailableVersion: "1.1.0"},
		},
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
			},
		},
		Status: appsv1.StatefulSetStatus{UpdatedReplicas: 0, ReadyReplicas: 0},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw, sts).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{},
		Clock:     &testClock{now: now},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 25min < 30m custom timeout, should still be polling.
	if result.RequeueAfter != healthCheckPollInterval {
		t.Errorf("expected continued polling with custom timeout")
	}
}

func TestReconcile_NotDueYet(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()
	now := time.Now()
	lastCheck := metav1.NewTime(now.Add(-1 * time.Minute)) // Checked 1 minute ago

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:  true,
				Schedule: "0 3 * * *", // Once a day at 3am
			},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{
				LastCheck: &lastCheck,
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{tags: []string{"1.0.0"}},
		Clock:     &testClock{now: now},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should requeue at next cron, not start checking.
	if result.RequeueAfter == healthCheckPollInterval {
		t.Error("should not start health check when not due")
	}
	if result.RequeueAfter == 0 {
		t.Error("should requeue at next cron")
	}
}
