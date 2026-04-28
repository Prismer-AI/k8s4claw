package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type testClock struct {
	now time.Time
}

func (c *testClock) Now() time.Time                  { return c.now }
func (c *testClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

type testTagLister struct {
	tags []string
	err  error
}

func (m *testTagLister) ListTags(_ context.Context, _ string) ([]string, error) {
	return m.tags, m.err
}

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clawv1alpha1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	return s
}

// ---------------------------------------------------------------------------
// Pure function tests
// ---------------------------------------------------------------------------

func TestExtractVersionFromImage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		image string
		want  string
	}{
		{"ghcr.io/prismer-ai/k8s4claw-openclaw:1.2.0", "1.2.0"},
		{"registry:5000/org/repo:1.2.0", "1.2.0"},
		{"repo:latest", "latest"},
		{"no-tag-image", ""},
		{"registry:5000/org/repo", ""},
		{"ghcr.io/org/img:v0.1.0-rc1", "v0.1.0-rc1"},
	}
	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			t.Parallel()
			if got := extractVersionFromImage(tt.image); got != tt.want {
				t.Errorf("extractVersionFromImage(%q) = %q; want %q", tt.image, got, tt.want)
			}
		})
	}
}

func TestTrimVersionHistory(t *testing.T) {
	t.Parallel()

	t.Run("under limit", func(t *testing.T) {
		t.Parallel()
		s := &clawv1alpha1.AutoUpdateStatus{
			VersionHistory: make([]clawv1alpha1.VersionHistoryEntry, 10),
		}
		trimVersionHistory(s)
		if len(s.VersionHistory) != 10 {
			t.Errorf("len = %d; want 10", len(s.VersionHistory))
		}
	})

	t.Run("at limit", func(t *testing.T) {
		t.Parallel()
		s := &clawv1alpha1.AutoUpdateStatus{
			VersionHistory: make([]clawv1alpha1.VersionHistoryEntry, maxVersionHistory),
		}
		trimVersionHistory(s)
		if len(s.VersionHistory) != maxVersionHistory {
			t.Errorf("len = %d; want %d", len(s.VersionHistory), maxVersionHistory)
		}
	})

	t.Run("over limit", func(t *testing.T) {
		t.Parallel()
		entries := make([]clawv1alpha1.VersionHistoryEntry, maxVersionHistory+10)
		for i := range entries {
			entries[i].Version = string(rune('a' + i%26))
		}
		s := &clawv1alpha1.AutoUpdateStatus{VersionHistory: entries}
		trimVersionHistory(s)
		if len(s.VersionHistory) != maxVersionHistory {
			t.Errorf("len = %d; want %d", len(s.VersionHistory), maxVersionHistory)
		}
		// Should keep the newest entries (tail).
		if s.VersionHistory[0] != entries[10] {
			t.Error("expected oldest entries to be trimmed")
		}
	})
}

func TestContainsString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		slice []string
		s     string
		want  bool
	}{
		{"found", []string{"a", "b", "c"}, "b", true},
		{"not found", []string{"a", "b", "c"}, "d", false},
		{"empty slice", []string{}, "a", false},
		{"nil slice", nil, "a", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := containsString(tt.slice, tt.s); got != tt.want {
				t.Errorf("containsString() = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestMergeAutoUpdateStatus(t *testing.T) {
	t.Parallel()

	t.Run("nil status creates new", func(t *testing.T) {
		t.Parallel()
		claw := &clawv1alpha1.Claw{}
		local := &clawv1alpha1.AutoUpdateStatus{
			CurrentVersion:   "1.0.0",
			AvailableVersion: "1.1.0",
			RollbackCount:    2,
			CircuitOpen:      true,
			FailedVersions:   []string{"0.9.0"},
		}
		mergeAutoUpdateStatus(claw, local)
		if claw.Status.AutoUpdate == nil {
			t.Fatal("expected AutoUpdate status to be created")
		}
		if claw.Status.AutoUpdate.CurrentVersion != "1.0.0" {
			t.Errorf("CurrentVersion = %q", claw.Status.AutoUpdate.CurrentVersion)
		}
		if claw.Status.AutoUpdate.RollbackCount != 2 {
			t.Errorf("RollbackCount = %d", claw.Status.AutoUpdate.RollbackCount)
		}
		if !claw.Status.AutoUpdate.CircuitOpen {
			t.Error("expected CircuitOpen=true")
		}
	})

	t.Run("existing status merged", func(t *testing.T) {
		t.Parallel()
		now := metav1.Now()
		claw := &clawv1alpha1.Claw{
			Status: clawv1alpha1.ClawStatus{
				AutoUpdate: &clawv1alpha1.AutoUpdateStatus{CurrentVersion: "old"},
			},
		}
		local := &clawv1alpha1.AutoUpdateStatus{
			CurrentVersion: "new",
			LastCheck:      &now,
		}
		mergeAutoUpdateStatus(claw, local)
		if claw.Status.AutoUpdate.CurrentVersion != "new" {
			t.Errorf("CurrentVersion = %q", claw.Status.AutoUpdate.CurrentVersion)
		}
		if claw.Status.AutoUpdate.LastCheck == nil {
			t.Error("LastCheck should be set")
		}
	})
}

// ---------------------------------------------------------------------------
// isCheckDue / requeueAtNextCron
// ---------------------------------------------------------------------------

func TestIsCheckDue(t *testing.T) {
	t.Parallel()
	r := &AutoUpdateReconciler{}

	t.Run("due", func(t *testing.T) {
		t.Parallel()
		lastCheck := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
		now := time.Date(2026, 1, 2, 3, 1, 0, 0, time.UTC) // next day past 3am
		if !r.isCheckDue("0 3 * * *", lastCheck, now) {
			t.Error("expected check to be due")
		}
	})

	t.Run("not due", func(t *testing.T) {
		t.Parallel()
		lastCheck := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
		now := time.Date(2026, 1, 1, 4, 0, 0, 0, time.UTC) // same day, not past next 3am
		if r.isCheckDue("0 3 * * *", lastCheck, now) {
			t.Error("expected check not due")
		}
	})

	t.Run("invalid schedule returns true", func(t *testing.T) {
		t.Parallel()
		if !r.isCheckDue("invalid", time.Now(), time.Now()) {
			t.Error("expected true for invalid schedule")
		}
	})
}

func TestRequeueAtNextCron(t *testing.T) {
	t.Parallel()

	t.Run("valid schedule", func(t *testing.T) {
		t.Parallel()
		clk := &testClock{now: time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)}
		r := &AutoUpdateReconciler{Clock: clk}
		result := r.requeueAtNextCron(&clawv1alpha1.AutoUpdateSpec{Schedule: "0 3 * * *"})
		if result.RequeueAfter < 59*time.Minute || result.RequeueAfter > 61*time.Minute {
			t.Errorf("RequeueAfter = %v; expected ~1h", result.RequeueAfter)
		}
	})

	t.Run("empty schedule uses default", func(t *testing.T) {
		t.Parallel()
		clk := &testClock{now: time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)}
		r := &AutoUpdateReconciler{Clock: clk}
		result := r.requeueAtNextCron(&clawv1alpha1.AutoUpdateSpec{})
		if result.RequeueAfter == 0 {
			t.Error("expected non-zero requeue")
		}
	})

	t.Run("invalid schedule fallback", func(t *testing.T) {
		t.Parallel()
		r := &AutoUpdateReconciler{Clock: &testClock{now: time.Now()}}
		result := r.requeueAtNextCron(&clawv1alpha1.AutoUpdateSpec{Schedule: "bad"})
		if result.RequeueAfter != 1*time.Hour {
			t.Errorf("RequeueAfter = %v; want 1h", result.RequeueAfter)
		}
	})

	t.Run("minimum 1 minute", func(t *testing.T) {
		t.Parallel()
		// Set clock to just before the next cron tick.
		clk := &testClock{now: time.Date(2026, 1, 1, 2, 59, 59, 0, time.UTC)}
		r := &AutoUpdateReconciler{Clock: clk}
		result := r.requeueAtNextCron(&clawv1alpha1.AutoUpdateSpec{Schedule: "0 3 * * *"})
		if result.RequeueAfter < 1*time.Minute {
			t.Errorf("RequeueAfter = %v; want >= 1m", result.RequeueAfter)
		}
	})
}

func TestClock_Fallback(t *testing.T) {
	t.Parallel()

	t.Run("nil clock returns realClock", func(t *testing.T) {
		t.Parallel()
		r := &AutoUpdateReconciler{}
		c := r.clock()
		if _, ok := c.(realClock); !ok {
			t.Errorf("expected realClock, got %T", c)
		}
	})

	t.Run("custom clock returned", func(t *testing.T) {
		t.Parallel()
		tc := &testClock{now: time.Now()}
		r := &AutoUpdateReconciler{Clock: tc}
		if r.clock() != tc {
			t.Error("expected custom clock")
		}
	})
}

func TestRealClock(t *testing.T) {
	t.Parallel()
	c := realClock{}
	now := c.Now()
	if now.IsZero() {
		t.Error("Now() returned zero")
	}
	since := c.Since(now.Add(-time.Second))
	if since < time.Second {
		t.Errorf("Since = %v; want >= 1s", since)
	}
}

// ---------------------------------------------------------------------------
// Reconcile integration tests (fake client)
// ---------------------------------------------------------------------------

func TestReconcile_AutoUpdateDisabled(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate:  nil, // disabled
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got %v", result.RequeueAfter)
	}
}

func TestReconcile_AutoUpdateEnabled_NoNewVersion(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:  true,
				Schedule: "0 3 * * *",
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claw).WithStatusSubresource(claw).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{tags: []string{}}, // no tags
		Clock:     &testClock{now: time.Now()},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue for next cron")
	}
}

func TestReconcile_NotFound(t *testing.T) {
	t.Parallel()
	scheme := newTestScheme()
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AutoUpdateReconciler{
		Client:    cl,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: &testTagLister{},
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Error("expected no requeue for not-found")
	}
}

func TestReconcile_CircuitBreakerOpen(t *testing.T) {
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
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{
				CircuitOpen:   true,
				RollbackCount: 3,
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
	if result.RequeueAfter == 0 {
		t.Error("expected requeue")
	}
}
