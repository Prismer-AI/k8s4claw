# Claw Controller MVP Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement the core Claw controller that reconciles a Claw CR into a StatefulSet with finalizer-based lifecycle and status reporting.

**Architecture:** Single-file reconciler with private helper methods (handleDeletion, ensureStatefulSet, updateStatus). Uses controller-runtime patterns: controllerutil for finalizers/ownerRefs, envtest for integration testing.

**Tech Stack:** Go 1.25, controller-runtime v0.23.1, k8s.io/api v0.35.2, envtest

---

### Task 1: Install envtest and set up test suite

**Files:**
- Create: `internal/controller/suite_test.go`

**Step 1: Install setup-envtest**

Run: `go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest`
Expected: Binary installed

**Step 2: Download envtest binaries**

Run: `setup-envtest use --bin-dir /home/willamhou/codes/k8s4claw/bin`
Expected: K8s API server + etcd binaries downloaded

**Step 3: Write test suite bootstrap**

```go
// internal/controller/suite_test.go
package controller

import (
	"context"
	"path/filepath"
	"testing"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

var (
	testEnv    *envtest.Environment
	cfg        *rest.Config
	k8sClient  client.Client
	ctx        context.Context
	cancel     context.CancelFunc
)

func TestMain(m *testing.M) {
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
		},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		panic(err)
	}

	err = clawv1alpha1.AddToScheme(scheme.Scheme)
	if err != nil {
		panic(err)
	}

	// Create manager with controller
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		panic(err)
	}

	registry := clawruntime.NewRegistry()
	registry.Register(clawv1alpha1.RuntimeOpenClaw, &clawruntime.OpenClawAdapter{})
	registry.Register(clawv1alpha1.RuntimeNanoClaw, &clawruntime.NanoClawAdapter{})
	registry.Register(clawv1alpha1.RuntimeZeroClaw, &clawruntime.ZeroClawAdapter{})
	registry.Register(clawv1alpha1.RuntimePicoClaw, &clawruntime.PicoClawAdapter{})

	err = (&ClawReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Registry: registry,
	}).SetupWithManager(mgr)
	if err != nil {
		panic(err)
	}

	go func() {
		if err := mgr.Start(ctx); err != nil {
			panic(err)
		}
	}()

	k8sClient = mgr.GetClient()

	code := m.Run()

	cancel()
	testEnv.Stop()
	os.Exit(code)
}
```

**Step 4: Verify test suite compiles**

Run: `go build ./internal/controller/...`
Expected: Build succeeds (no tests run yet)

**Step 5: Commit**

```bash
git add internal/controller/suite_test.go bin/
git commit -m "test: set up envtest suite for controller tests"
```

---

### Task 2: Write failing tests for finalizer management

**Files:**
- Create: `internal/controller/claw_controller_test.go`

**Step 1: Write test for finalizer added on create**

```go
// internal/controller/claw_controller_test.go
package controller

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const (
	timeout  = 10 * time.Second
	interval = 250 * time.Millisecond
)

func waitForCondition(t *testing.T, key types.NamespacedName, check func(*clawv1alpha1.Claw) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var claw clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, key, &claw); err == nil && check(&claw) {
			return
		}
		time.Sleep(interval)
	}
	t.Fatal("timed out waiting for condition")
}

func createNamespace(t *testing.T, name string) {
	t.Helper()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}
	t.Cleanup(func() {
		k8sClient.Delete(ctx, ns)
	})
}

func TestClawReconciler_FinalizerAdded(t *testing.T) {
	ns := "test-finalizer-add"
	createNamespace(t, ns)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claw",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	key := types.NamespacedName{Name: "test-claw", Namespace: ns}
	waitForCondition(t, key, func(c *clawv1alpha1.Claw) bool {
		for _, f := range c.Finalizers {
			if f == "claw.prismer.ai/cleanup" {
				return true
			}
		}
		return false
	})
}

func TestClawReconciler_FinalizerRunsOnDelete(t *testing.T) {
	ns := "test-finalizer-del"
	createNamespace(t, ns)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claw-del",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeZeroClaw,
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	key := types.NamespacedName{Name: "test-claw-del", Namespace: ns}

	// Wait for finalizer to be added
	waitForCondition(t, key, func(c *clawv1alpha1.Claw) bool {
		for _, f := range c.Finalizers {
			if f == "claw.prismer.ai/cleanup" {
				return true
			}
		}
		return false
	})

	// Delete the Claw
	if err := k8sClient.Delete(ctx, claw); err != nil {
		t.Fatalf("failed to delete Claw: %v", err)
	}

	// Wait for Claw to be fully deleted (finalizer removed)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var c clawv1alpha1.Claw
		err := k8sClient.Get(ctx, key, &c)
		if client.IgnoreNotFound(err) == nil && err != nil {
			return // Object deleted
		}
		time.Sleep(interval)
	}
	t.Fatal("timed out waiting for Claw deletion")
}
```

**Step 2: Run tests to verify they fail**

Run: `go test -v -count=1 ./internal/controller/... -run TestClawReconciler_Finalizer 2>&1 | tail -20`
Expected: FAIL (current reconciler has no finalizer logic)

**Step 3: Commit failing tests**

```bash
git add internal/controller/claw_controller_test.go
git commit -m "test: add failing finalizer tests for Claw controller"
```

---

### Task 3: Implement finalizer management (GREEN)

**Files:**
- Modify: `internal/controller/claw_controller.go`

**Step 1: Implement handleDeletion and ensureFinalizer**

Replace the full `claw_controller.go` with finalizer logic:

```go
package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

const clawFinalizer = "claw.prismer.ai/cleanup"

// ClawReconciler reconciles a Claw object.
type ClawReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Registry *clawruntime.Registry
}

// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods;services;persistentvolumeclaims;secrets;events;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get;list;watch;create;delete

func (r *ClawReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var claw clawv1alpha1.Claw
	if err := r.Get(ctx, req.NamespacedName, &claw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Resolve runtime adapter
	adapter, ok := r.Registry.Get(claw.Spec.Runtime)
	if !ok {
		logger.Error(fmt.Errorf("unknown runtime: %s", claw.Spec.Runtime), "unsupported runtime type")
		return ctrl.Result{}, nil
	}

	// Handle deletion
	if !claw.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &claw)
	}

	// Ensure finalizer
	if err := r.ensureFinalizer(ctx, &claw); err != nil {
		return ctrl.Result{}, err
	}

	// TODO: ensureStatefulSet
	// TODO: updateStatus
	_ = adapter

	return ctrl.Result{}, nil
}

func (r *ClawReconciler) handleDeletion(ctx context.Context, claw *clawv1alpha1.Claw) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(claw, clawFinalizer) {
		return ctrl.Result{}, nil
	}

	logger.Info("running finalizer", "reclaimPolicy", claw.Spec.Persistence.GetReclaimPolicy())

	// MVP: no-op cleanup for all reclaim policies.
	// Future: Retain removes ownerRef, Archive triggers archival, Delete removes PVCs.

	// Remove finalizer
	controllerutil.RemoveFinalizer(claw, clawFinalizer)
	if err := r.Update(ctx, claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *ClawReconciler) ensureFinalizer(ctx context.Context, claw *clawv1alpha1.Claw) error {
	if controllerutil.ContainsFinalizer(claw, clawFinalizer) {
		return nil
	}
	controllerutil.AddFinalizer(claw, clawFinalizer)
	return r.Update(ctx, claw)
}

func (r *ClawReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clawv1alpha1.Claw{}).
		Owns(&appsv1.StatefulSet{}).
		Complete(r)
}
```

Note: `claw.Spec.Persistence.GetReclaimPolicy()` requires a helper. Add to `ClawSpec` or inline. Since `Persistence` is optional (pointer), we need a nil-safe accessor. Add a helper method on `PersistenceSpec`.

**Step 2: Add nil-safe GetReclaimPolicy helper**

Add to `api/v1alpha1/common_types.go`:

```go
// GetReclaimPolicy returns the reclaim policy, defaulting to Retain if unset.
func (p *PersistenceSpec) GetReclaimPolicy() ReclaimPolicy {
	if p == nil || p.ReclaimPolicy == "" {
		return ReclaimRetain
	}
	return p.ReclaimPolicy
}
```

And on ClawSpec (since Persistence is a pointer):

```go
// in claw_types.go — no, better to just check inline in the controller
```

Actually, simpler to just call it inline:

```go
policy := clawv1alpha1.ReclaimRetain
if claw.Spec.Persistence != nil && claw.Spec.Persistence.ReclaimPolicy != "" {
    policy = claw.Spec.Persistence.ReclaimPolicy
}
logger.Info("running finalizer", "reclaimPolicy", policy)
```

**Step 3: Run finalizer tests**

Run: `go test -v -count=1 ./internal/controller/... -run TestClawReconciler_Finalizer`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/controller/claw_controller.go
git commit -m "feat: implement finalizer management for Claw controller"
```

---

### Task 4: Write failing tests for StatefulSet management

**Files:**
- Modify: `internal/controller/claw_controller_test.go`

**Step 1: Add StatefulSet creation test**

```go
func TestClawReconciler_StatefulSetCreated(t *testing.T) {
	ns := "test-sts-create"
	createNamespace(t, ns)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sts",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for StatefulSet to be created
	var sts appsv1.StatefulSet
	key := types.NamespacedName{Name: "test-sts", Namespace: ns}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := k8sClient.Get(ctx, key, &sts); err == nil {
			break
		}
		time.Sleep(interval)
	}

	// Verify StatefulSet properties
	if sts.Name == "" {
		t.Fatal("StatefulSet was not created")
	}
	if *sts.Spec.Replicas != 1 {
		t.Errorf("replicas = %d; want 1", *sts.Spec.Replicas)
	}
	if sts.Spec.ServiceName != "test-sts" {
		t.Errorf("serviceName = %q; want %q", sts.Spec.ServiceName, "test-sts")
	}

	// Verify labels
	labels := sts.Spec.Template.Labels
	if labels["app.kubernetes.io/name"] != "claw" {
		t.Errorf("label app.kubernetes.io/name = %q; want 'claw'", labels["app.kubernetes.io/name"])
	}
	if labels["app.kubernetes.io/instance"] != "test-sts" {
		t.Errorf("label app.kubernetes.io/instance = %q; want 'test-sts'", labels["app.kubernetes.io/instance"])
	}
	if labels["claw.prismer.ai/runtime"] != "openclaw" {
		t.Errorf("label claw.prismer.ai/runtime = %q; want 'openclaw'", labels["claw.prismer.ai/runtime"])
	}

	// Verify ownerReference
	if len(sts.OwnerReferences) == 0 {
		t.Fatal("StatefulSet has no ownerReferences")
	}
	if sts.OwnerReferences[0].Kind != "Claw" {
		t.Errorf("ownerReference kind = %q; want 'Claw'", sts.OwnerReferences[0].Kind)
	}

	// Verify pod security context
	podSec := sts.Spec.Template.Spec.SecurityContext
	if podSec == nil || !*podSec.RunAsNonRoot {
		t.Error("pod should have runAsNonRoot=true")
	}

	// Verify terminationGracePeriodSeconds = shutdown + 10
	// OpenClaw shutdown = 30, so expect 40
	tgps := sts.Spec.Template.Spec.TerminationGracePeriodSeconds
	if tgps == nil || *tgps != 40 {
		t.Errorf("terminationGracePeriodSeconds = %v; want 40", tgps)
	}
}

func TestClawReconciler_UnknownRuntime(t *testing.T) {
	ns := "test-unknown-rt"
	createNamespace(t, ns)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-unknown",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: "nonexistent",
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait a bit for reconciliation
	time.Sleep(2 * time.Second)

	// Verify no StatefulSet created
	var sts appsv1.StatefulSet
	key := types.NamespacedName{Name: "test-unknown", Namespace: ns}
	err := k8sClient.Get(ctx, key, &sts)
	if err == nil {
		t.Error("StatefulSet should not be created for unknown runtime")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test -v -count=1 ./internal/controller/... -run TestClawReconciler_StatefulSet`
Expected: FAIL (StatefulSet not created)

**Step 3: Commit**

```bash
git add internal/controller/claw_controller_test.go
git commit -m "test: add failing StatefulSet tests for Claw controller"
```

---

### Task 5: Implement StatefulSet management (GREEN)

**Files:**
- Modify: `internal/controller/claw_controller.go`

**Step 1: Implement ensureStatefulSet**

Add to `claw_controller.go` after `ensureFinalizer`:

```go
func (r *ClawReconciler) ensureStatefulSet(ctx context.Context, claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) error {
	logger := log.FromContext(ctx)

	desired := r.buildStatefulSet(claw, adapter)

	var existing appsv1.StatefulSet
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
	if client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to get StatefulSet: %w", err)
	}

	if err != nil {
		// StatefulSet does not exist — create
		if err := controllerutil.SetControllerReference(claw, desired, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create StatefulSet: %w", err)
		}
		logger.Info("created StatefulSet", "name", desired.Name)
		return nil
	}

	// StatefulSet exists — update spec if changed
	existing.Spec.Template = desired.Spec.Template
	existing.Spec.Replicas = desired.Spec.Replicas
	if err := r.Update(ctx, &existing); err != nil {
		return fmt.Errorf("failed to update StatefulSet: %w", err)
	}
	logger.Info("updated StatefulSet", "name", existing.Name)
	return nil
}

func (r *ClawReconciler) buildStatefulSet(claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) *appsv1.StatefulSet {
	labels := map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": claw.Name,
		"claw.prismer.ai/runtime":    string(claw.Spec.Runtime),
	}

	replicas := int32(1)
	runAsNonRoot := true
	fsGroup := int64(1000)
	shutdownSeconds := int64(adapter.GracefulShutdownSeconds()) + 10

	podTemplate := adapter.PodTemplate(claw)

	// Overlay controller-managed fields
	podTemplate.Labels = labels
	podTemplate.Spec.SecurityContext = &corev1.PodSecurityContext{
		RunAsNonRoot: &runAsNonRoot,
		FSGroup:      &fsGroup,
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
	podTemplate.Spec.TerminationGracePeriodSeconds = &shutdownSeconds

	// If adapter PodTemplate has no containers, inject placeholder
	if len(podTemplate.Spec.Containers) == 0 {
		cfg := adapter.DefaultConfig()
		var envVars []corev1.EnvVar
		for k, v := range cfg.Environment {
			envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
		}

		readOnly := true
		allowEscalation := false
		runAsUser := int64(1000)
		runAsGroup := int64(1000)

		podTemplate.Spec.Containers = []corev1.Container{
			{
				Name:            "runtime",
				Image:           "busybox:latest",
				Env:             envVars,
				LivenessProbe:   adapter.HealthProbe(claw),
				ReadinessProbe:  adapter.ReadinessProbe(claw),
				SecurityContext: &corev1.SecurityContext{
					RunAsUser:                &runAsUser,
					RunAsGroup:               &runAsGroup,
					RunAsNonRoot:             &runAsNonRoot,
					ReadOnlyRootFilesystem:   &readOnly,
					AllowPrivilegeEscalation: &allowEscalation,
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
					},
					SeccompProfile: &corev1.SeccompProfile{
						Type: corev1.SeccompProfileTypeRuntimeDefault,
					},
				},
			},
		}
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claw.Name,
			Namespace: claw.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: claw.Name,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: *podTemplate,
		},
	}
}
```

**Step 2: Wire ensureStatefulSet into Reconcile**

Update the Reconcile method to call `ensureStatefulSet` after `ensureFinalizer`:

```go
// After ensureFinalizer:
if err := r.ensureStatefulSet(ctx, &claw, adapter); err != nil {
    return ctrl.Result{}, err
}
```

**Step 3: Add missing imports**

Add to imports:
```go
appsv1 "k8s.io/api/apps/v1"
corev1 "k8s.io/api/core/v1"
metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
```

**Step 4: Run StatefulSet tests**

Run: `go test -v -count=1 ./internal/controller/... -run TestClawReconciler_StatefulSet`
Expected: PASS

**Step 5: Run all controller tests**

Run: `go test -v -count=1 ./internal/controller/...`
Expected: All tests PASS

**Step 6: Commit**

```bash
git add internal/controller/claw_controller.go
git commit -m "feat: implement StatefulSet management in Claw controller"
```

---

### Task 6: Write failing tests for status management

**Files:**
- Modify: `internal/controller/claw_controller_test.go`

**Step 1: Add status tests**

```go
func TestClawReconciler_StatusPending(t *testing.T) {
	ns := "test-status-pending"
	createNamespace(t, ns)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pending",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	key := types.NamespacedName{Name: "test-pending", Namespace: ns}

	// Wait for status to be set (Provisioning since StatefulSet exists but no ready pods)
	waitForCondition(t, key, func(c *clawv1alpha1.Claw) bool {
		return c.Status.Phase == clawv1alpha1.ClawPhaseProvisioning
	})

	// Verify observedGeneration is set
	var updated clawv1alpha1.Claw
	if err := k8sClient.Get(ctx, key, &updated); err != nil {
		t.Fatalf("failed to get Claw: %v", err)
	}
	if updated.Status.ObservedGeneration != updated.Generation {
		t.Errorf("observedGeneration = %d; want %d", updated.Status.ObservedGeneration, updated.Generation)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test -v -count=1 ./internal/controller/... -run TestClawReconciler_Status`
Expected: FAIL (status not updated)

**Step 3: Commit**

```bash
git add internal/controller/claw_controller_test.go
git commit -m "test: add failing status tests for Claw controller"
```

---

### Task 7: Implement status management (GREEN)

**Files:**
- Modify: `internal/controller/claw_controller.go`

**Step 1: Implement updateStatus**

```go
func (r *ClawReconciler) updateStatus(ctx context.Context, claw *clawv1alpha1.Claw) error {
	// Determine phase from StatefulSet state
	var sts appsv1.StatefulSet
	stsKey := client.ObjectKey{Name: claw.Name, Namespace: claw.Namespace}
	err := r.Get(ctx, stsKey, &sts)

	var phase clawv1alpha1.ClawPhase
	var runtimeReady bool

	switch {
	case err != nil:
		phase = clawv1alpha1.ClawPhasePending
	case sts.Status.ReadyReplicas >= 1:
		phase = clawv1alpha1.ClawPhaseRunning
		runtimeReady = true
	default:
		phase = clawv1alpha1.ClawPhaseProvisioning
	}

	claw.Status.Phase = phase
	claw.Status.ObservedGeneration = claw.Generation

	// Set RuntimeReady condition
	condition := metav1.Condition{
		Type:               "RuntimeReady",
		ObservedGeneration: claw.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if runtimeReady {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "StatefulSetReady"
		condition.Message = "StatefulSet has ready replicas"
	} else {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "StatefulSetNotReady"
		condition.Message = "Waiting for StatefulSet to become ready"
	}
	meta.SetStatusCondition(&claw.Status.Conditions, condition)

	return r.Status().Update(ctx, claw)
}
```

**Step 2: Wire updateStatus into Reconcile**

After `ensureStatefulSet`:

```go
if err := r.updateStatus(ctx, &claw); err != nil {
    return ctrl.Result{}, err
}
```

**Step 3: Add meta import**

```go
"k8s.io/apimachinery/pkg/api/meta"
```

Note: `meta.SetStatusCondition` is from `apimachinery/pkg/api/meta`. Verify import path — in controller-runtime it may be from:

```go
import "sigs.k8s.io/controller-runtime/pkg/client/apiutil"
```

Actually, the standard location in newer k8s is:

```go
import "k8s.io/apimachinery/pkg/api/meta"
```

Use `apimeta "k8s.io/apimachinery/pkg/api/meta"` to avoid collision with the `meta` in metav1.

**Step 4: Run status tests**

Run: `go test -v -count=1 ./internal/controller/... -run TestClawReconciler_Status`
Expected: PASS

**Step 5: Run all controller tests**

Run: `go test -v -count=1 ./internal/controller/...`
Expected: All tests PASS

**Step 6: Commit**

```bash
git add internal/controller/claw_controller.go
git commit -m "feat: implement status management in Claw controller"
```

---

### Task 8: Register controller in operator main

**Files:**
- Modify: `cmd/operator/main.go`

**Step 1: Wire up controllers**

Replace the TODO comment in `main.go`:

```go
// Register controllers
registry := clawruntime.NewRegistry()
registry.Register(clawv1alpha1.RuntimeOpenClaw, &clawruntime.OpenClawAdapter{})
registry.Register(clawv1alpha1.RuntimeNanoClaw, &clawruntime.NanoClawAdapter{})
registry.Register(clawv1alpha1.RuntimeZeroClaw, &clawruntime.ZeroClawAdapter{})
registry.Register(clawv1alpha1.RuntimePicoClaw, &clawruntime.PicoClawAdapter{})

if err := (&controller.ClawReconciler{
    Client:   mgr.GetClient(),
    Scheme:   mgr.GetScheme(),
    Registry: registry,
}).SetupWithManager(mgr); err != nil {
    setupLog.Error(err, "unable to create controller", "controller", "Claw")
    os.Exit(1)
}
```

Add imports:

```go
"github.com/Prismer-AI/k8s4claw/internal/controller"
clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
```

**Step 2: Verify build**

Run: `go build ./cmd/operator/...`
Expected: Build succeeds

**Step 3: Commit**

```bash
git add cmd/operator/main.go
git commit -m "feat: register Claw controller with operator manager"
```

---

### Task 9: Run full test suite and check coverage

**Step 1: Run all tests**

Run: `go test -race -cover ./...`
Expected: All tests PASS

**Step 2: Check coverage for controller package**

Run: `go test -coverprofile=coverage.out ./internal/controller/... && go tool cover -func=coverage.out`
Expected: 80%+ coverage on controller package

**Step 3: Commit any fixes needed**

---

### Task 10: Final code review

**Step 1: Run go vet**

Run: `go vet ./...`
Expected: No issues

**Step 2: Run build**

Run: `go build ./...`
Expected: Clean build

**Step 3: Review with go-reviewer agent**

Use the go-reviewer agent to review all changed files.

**Step 4: Final commit with all fixes**

```bash
git add -A
git commit -m "chore: address code review feedback for Claw controller MVP"
```
