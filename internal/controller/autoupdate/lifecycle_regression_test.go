package autoupdate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	"github.com/Prismer-AI/k8s4claw/internal/controller"
)

const prefix = "claw.prismer.ai/"
const good = "ghcr.io/prismer-ai/k8s4claw-openclaw:1.0.0"
const bad = "ghcr.io/prismer-ai/k8s4claw-openclaw:1.2.0"

func healthyWorkload(claw *v1.Claw, image string) (*appsv1.StatefulSet, *corev1.Pod) {
	claw.UID = "claw-uid"
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: claw.Name, Namespace: claw.Namespace, UID: "sts-uid", Generation: 7,
		OwnerReferences: []metav1.OwnerReference{{APIVersion: v1.GroupVersion.String(), Kind: "Claw", Name: claw.Name, UID: claw.UID, Controller: ptr.To(true)}}},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": claw.Name}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "runtime", Image: image}}}}},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 7, CurrentRevision: "revision", UpdateRevision: "revision", UpdatedReplicas: 1, ReadyReplicas: 1}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: claw.Name + "-0", Namespace: claw.Namespace, UID: "pod-uid",
		Labels:          map[string]string{"app": claw.Name, appsv1.ControllerRevisionHashLabelKey: "revision"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: sts.Name, UID: sts.UID, Controller: ptr.To(true)}}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "runtime", Image: image}}},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	return sts, pod
}

func activeClaw() *v1.Claw {
	return &v1.Claw{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", Annotations: map[string]string{
		prefix + "target-image": bad, prefix + "previous-image": good, prefix + "update-phase": "HealthCheck", prefix + "update-started": time.Now().Format(time.RFC3339Nano)}},
		Spec:   v1.ClawSpec{Runtime: v1.RuntimeOpenClaw, AutoUpdate: &v1.AutoUpdateSpec{Enabled: true, HealthTimeout: "10m"}},
		Status: v1.ClawStatus{AutoUpdate: &v1.AutoUpdateStatus{CurrentVersion: "1.0.0", AvailableVersion: "1.2.0"}}}
}

func reconcile(t *testing.T, r *controller.AutoUpdateReconciler) error {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"}})
	return err
}

func readClaw(t *testing.T, r *controller.AutoUpdateReconciler) *v1.Claw {
	t.Helper()
	var c v1.Claw
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &c); err != nil {
		t.Fatal(err)
	}
	return &c
}

func TestReadinessIsBoundToTargetAndActualPods(t *testing.T) {
	cases := []struct {
		name   string
		change func(*appsv1.StatefulSet, *corev1.Pod)
	}{
		{"old image", func(s *appsv1.StatefulSet, p *corev1.Pod) { s.Spec.Template.Spec.Containers[0].Image = good }},
		{"stale generation", func(s *appsv1.StatefulSet, p *corev1.Pod) { s.Status.ObservedGeneration-- }},
		{"old current revision", func(s *appsv1.StatefulSet, p *corev1.Pod) { s.Status.CurrentRevision = "old" }},
		{"old pod image", func(s *appsv1.StatefulSet, p *corev1.Pod) { p.Spec.Containers[0].Image = good }},
		{"old pod revision", func(s *appsv1.StatefulSet, p *corev1.Pod) { p.Labels[appsv1.ControllerRevisionHashLabelKey] = "old" }},
		{"foreign pod", func(s *appsv1.StatefulSet, p *corev1.Pod) { p.OwnerReferences = nil }},
		{"unready pod", func(s *appsv1.StatefulSet, p *corev1.Pod) { p.Status.Conditions = nil }},
		{"zero replicas", func(s *appsv1.StatefulSet, p *corev1.Pod) {
			s.Spec.Replicas = ptr.To(int32(0))
			s.Status.ReadyReplicas = 0
			s.Status.UpdatedReplicas = 0
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := activeClaw()
			s, p := healthyWorkload(c, bad)
			tc.change(s, p)
			r := newTestAutoUpdateReconciler([]runtime.Object{c, s, p}, &mockTagLister{})
			if err := reconcile(t, r); err != nil {
				t.Fatal(err)
			}
			got := readClaw(t, r)
			if got.Status.AutoUpdate.CurrentVersion != "1.0.0" || got.Annotations[prefix+"update-phase"] != "HealthCheck" {
				t.Fatalf("false success: %+v", got.Status.AutoUpdate)
			}
		})
	}
}

type failingClient struct {
	client.Client
	statusConflicts int
	rejectCleanup   bool
}
type failingStatus struct {
	client.SubResourceWriter
	owner *failingClient
}

func (c *failingClient) Status() client.SubResourceWriter {
	return &failingStatus{SubResourceWriter: c.Client.Status(), owner: c}
}
func (w *failingStatus) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if w.owner.statusConflicts > 0 {
		w.owner.statusConflicts--
		return apierrors.NewConflict(schema.GroupResource{Group: v1.GroupVersion.Group, Resource: "claws"}, obj.GetName(), errors.New("injected status conflict"))
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}
func (c *failingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, ok := obj.(*v1.Claw); ok && c.rejectCleanup && obj.GetAnnotations()[prefix+"update-phase"] == "" {
		return errors.New("injected process interruption before cleanup")
	}
	return c.Client.Update(ctx, obj, opts...)
}

func TestRollbackAccountingSurvivesConflictsAndRestart(t *testing.T) {
	c := activeClaw()
	c.Annotations[prefix+"update-started"] = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
	s, p := healthyWorkload(c, bad)
	s.Status.ReadyReplicas = 0
	p.Status.Conditions = nil
	r := newTestAutoUpdateReconciler([]runtime.Object{c, s, p}, &mockTagLister{})
	backend := r.Client
	f := &failingClient{Client: backend, statusConflicts: 100}
	r.Client = f
	if err := reconcile(t, r); err == nil {
		t.Fatal("wanted injected conflict")
	}
	got := readClaw(t, r)
	if got.Annotations[prefix+"update-phase"] != "RollingBack" || got.Annotations[prefix+"target-image"] != good {
		t.Fatal("rollback state was not durable")
	}
	if got.Status.AutoUpdate.RollbackCount != 0 {
		t.Fatal("failed write counted")
	}
	// New reconciler/process, same persisted API state.
	r = &controller.AutoUpdateReconciler{Client: backend, Scheme: r.Scheme, Recorder: r.Recorder, TagLister: &mockTagLister{}}
	if err := reconcile(t, r); err != nil {
		t.Fatal(err)
	}
	if err := reconcile(t, r); err != nil {
		t.Fatal(err)
	}
	got = readClaw(t, r)
	if got.Status.AutoUpdate.RollbackCount != 1 || len(got.Status.AutoUpdate.VersionHistory) != 1 {
		t.Fatalf("non-idempotent outcome: %+v", got.Status.AutoUpdate)
	}
	// Simulate the StatefulSet controller observing the restored template.
	var updated appsv1.StatefulSet
	if err := backend.Get(context.Background(), client.ObjectKeyFromObject(s), &updated); err != nil {
		t.Fatal(err)
	}
	updated.Spec.Template.Spec.Containers[0].Image = good
	updated.Status.UpdateRevision = "restored"
	if err := backend.Update(context.Background(), &updated); err != nil {
		t.Fatal(err)
	}
	updated.Status.UpdateRevision = "restored"
	if err := backend.Status().Update(context.Background(), &updated); err != nil {
		t.Fatal(err)
	}
	if err := reconcile(t, r); err != nil {
		t.Fatal(err)
	}
	var gone corev1.Pod
	if err := backend.Get(context.Background(), client.ObjectKeyFromObject(p), &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("failed old Pod not removed: %v", err)
	}
	// Workload becomes healthy, then crash after accounting but before cleanup.
	updated.Status.CurrentRevision = "restored"
	updated.Status.ReadyReplicas = 1
	updated.Status.UpdatedReplicas = 1
	if err := backend.Status().Update(context.Background(), &updated); err != nil {
		t.Fatal(err)
	}
	p.ResourceVersion = ""
	p.UID = "replacement-pod"
	p.Labels[appsv1.ControllerRevisionHashLabelKey] = "restored"
	p.Spec.Containers[0].Image = good
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := backend.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	r.Client = &failingClient{Client: backend, rejectCleanup: true}
	if err := reconcile(t, r); err == nil {
		t.Fatal("expected rollback cleanup interruption")
	}
	r.Client = backend
	if err := reconcile(t, r); err != nil {
		t.Fatal(err)
	}
	got = readClaw(t, r)
	if got.Status.AutoUpdate.RollbackCount != 1 || len(got.Status.AutoUpdate.VersionHistory) != 1 || got.Annotations[prefix+"update-phase"] != "" || got.Annotations[prefix+"target-image"] != good {
		t.Fatalf("rollback restart lost outcome: %+v", got)
	}
}

func TestSuccessStatusSurvivesCleanupInterruption(t *testing.T) {
	c := activeClaw()
	s, p := healthyWorkload(c, bad)
	r := newTestAutoUpdateReconciler([]runtime.Object{c, s, p}, &mockTagLister{})
	backend := r.Client
	r.Client = &failingClient{Client: backend, rejectCleanup: true}
	if err := reconcile(t, r); err == nil {
		t.Fatal("expected cleanup interruption")
	}
	got := readClaw(t, r)
	if len(got.Status.AutoUpdate.VersionHistory) != 1 || got.Annotations[prefix+"update-phase"] != "HealthCheck" {
		t.Fatal("wrong interrupted state")
	}
	r.Client = backend
	if err := reconcile(t, r); err != nil {
		t.Fatal(err)
	}
	got = readClaw(t, r)
	if len(got.Status.AutoUpdate.VersionHistory) != 1 || got.Annotations[prefix+"update-phase"] != "" {
		t.Fatal("completion not idempotent")
	}
}

func TestRollbackWithoutSavedImageDoesNotGuessLatest(t *testing.T) {
	c := activeClaw()
	delete(c.Annotations, prefix+"previous-image")
	c.Annotations[prefix+"update-started"] = "invalid"
	r := newTestAutoUpdateReconciler([]runtime.Object{c}, &mockTagLister{})
	if err := reconcile(t, r); err == nil {
		t.Fatal("unsafe fallback accepted")
	}
	if readClaw(t, r).Annotations[prefix+"target-image"] != bad {
		t.Fatal("target changed without known previous image")
	}
}

func TestRollbackProtectsHealthyForeignAndRestoredPods(t *testing.T) {
	for _, name := range []string{"healthy", "foreign", "restored revision", "different image"} {
		t.Run(name, func(t *testing.T) {
			c := activeClaw()
			c.Annotations[prefix+"update-phase"] = "RollingBack"
			c.Annotations[prefix+"target-image"] = good
			c.Annotations[prefix+"failed-image"] = bad
			c.Annotations[prefix+"rollback-reason"] = "test failure"
			s, p := healthyWorkload(c, good)
			s.Status.ReadyReplicas = 0
			p.Spec.Containers[0].Image = bad
			p.Labels[appsv1.ControllerRevisionHashLabelKey] = "old"
			p.Status.Conditions = nil
			switch name {
			case "healthy":
				p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			case "foreign":
				p.OwnerReferences = nil
			case "restored revision":
				p.Labels[appsv1.ControllerRevisionHashLabelKey] = "revision"
			case "different image":
				p.Spec.Containers[0].Image = good
			}
			r := newTestAutoUpdateReconciler([]runtime.Object{c, s, p}, &mockTagLister{})
			if err := reconcile(t, r); err != nil {
				t.Fatal(err)
			}
			var kept corev1.Pod
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(p), &kept); err != nil {
				t.Fatalf("protected pod was removed: %v", err)
			}
		})
	}
}

func TestUnhealthyBaselineDoesNotInitiateUpdate(t *testing.T) {
	c := activeClaw()
	c.Annotations = map[string]string{prefix + "target-image": good}
	s, p := healthyWorkload(c, good)
	p.Status.Conditions = nil
	r := newTestAutoUpdateReconciler([]runtime.Object{c, s, p}, &mockTagLister{tags: []string{"1.2.0"}})
	if err := reconcile(t, r); err != nil {
		t.Fatal(err)
	}
	got := readClaw(t, r)
	if got.Annotations[prefix+"target-image"] != good || got.Annotations[prefix+"update-phase"] != "" {
		t.Fatal("unhealthy baseline used as previous-good")
	}
}
