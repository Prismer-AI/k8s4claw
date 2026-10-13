package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/robfig/cron/v3"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	"github.com/Prismer-AI/k8s4claw/internal/registry"
)

const (
	annotationTargetImage    = "claw.prismer.ai/target-image"
	annotationUpdatePhase    = "claw.prismer.ai/update-phase"
	annotationUpdateStart    = "claw.prismer.ai/update-started"
	annotationPreviousImage  = "claw.prismer.ai/previous-image"
	annotationFailedImage    = "claw.prismer.ai/failed-image"
	annotationRollbackReason = "claw.prismer.ai/rollback-reason"

	conditionAutoUpdateAvailable  = "AutoUpdateAvailable"
	conditionAutoUpdateInProgress = "AutoUpdateInProgress"

	defaultSchedule      = "0 3 * * *"
	defaultHealthTimeout = 10 * time.Minute
	defaultMaxRollbacks  = 3

	// healthCheckPollInterval is how often we requeue during health check.
	healthCheckPollInterval = 15 * time.Second

	// maxVersionHistory caps the version history to prevent etcd bloat.
	maxVersionHistory = 50
)

// TagLister abstracts registry tag listing for testability.
type TagLister interface {
	ListTags(ctx context.Context, image string) ([]string, error)
}

// Clock abstracts time operations for testability.
type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
}

type realClock struct{}

func (realClock) Now() time.Time                  { return time.Now() }
func (realClock) Since(t time.Time) time.Duration { return time.Since(t) }

// AutoUpdateReconciler checks for new image versions and manages the update lifecycle.
type AutoUpdateReconciler struct {
	client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder
	TagLister TagLister
	Clock     Clock
}

func (r *AutoUpdateReconciler) clock() Clock {
	if r.Clock != nil {
		return r.Clock
	}
	return realClock{}
}

// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;delete

func (r *AutoUpdateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) { //nolint:gocyclo // justified: reconciler state machine
	logger := log.FromContext(ctx)

	var claw clawv1alpha1.Claw
	if err := r.Get(ctx, req.NamespacedName, &claw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Skip if auto-update is not enabled.
	if claw.Spec.AutoUpdate == nil || !claw.Spec.AutoUpdate.Enabled {
		return ctrl.Result{}, nil
	}

	spec := claw.Spec.AutoUpdate
	status := claw.Status.AutoUpdate
	if status == nil {
		status = &clawv1alpha1.AutoUpdateStatus{}
		claw.Status.AutoUpdate = status
	}

	// Check if we're in the middle of an update (health check phase).
	phase := claw.Annotations[annotationUpdatePhase]
	if phase == "HealthCheck" {
		return r.reconcileHealthCheck(ctx, &claw)
	}
	if phase == "RollingBack" {
		return r.reconcileRollback(ctx, &claw)
	}

	// Check if the current image is digest-pinned.
	currentImage := claw.Annotations[annotationTargetImage]
	if currentImage != "" && registry.IsDigestPinned(currentImage) {
		logger.Info("skipping auto-update: image is digest-pinned", "image", currentImage)
		return r.requeueAtNextCron(spec), nil
	}

	// Determine if a version check is due.
	schedule := spec.Schedule
	if schedule == "" {
		schedule = defaultSchedule
	}

	if status.LastCheck != nil && !r.isCheckDue(schedule, status.LastCheck.Time, r.clock().Now()) {
		return r.requeueAtNextCron(spec), nil
	}

	// Perform version check.
	logger.Info("checking for new version")
	RecordAutoUpdateCheck(claw.Namespace)

	baseImage := registry.ImageForRuntime(string(claw.Spec.Runtime))
	if baseImage == "" {
		logger.Info("unknown runtime, skipping auto-update", "runtime", claw.Spec.Runtime)
		return r.requeueAtNextCron(spec), nil
	}

	registryURL := registry.RegistryURLForImage(baseImage)
	tags, err := r.TagLister.ListTags(ctx, registryURL)
	if err != nil {
		logger.Error(err, "failed to list tags from registry")
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	now := metav1.NewTime(r.clock().Now())
	status.LastCheck = &now

	constraint := spec.VersionConstraint
	if constraint == "" {
		constraint = ">=0.0.0" // match any version
	}

	newVersion, found := registry.ResolveBestVersion(tags, constraint, status.CurrentVersion, status.FailedVersions)
	if !found {
		logger.Info("no new version available")
		if err := r.Status().Update(ctx, &claw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status after version check: %w", err)
		}
		return r.requeueAtNextCron(spec), nil
	}

	status.AvailableVersion = newVersion

	// Check circuit breaker.
	if status.CircuitOpen {
		logger.Info("circuit breaker is open, skipping update", "rollbackCount", status.RollbackCount)
		r.Recorder.Event(&claw, corev1.EventTypeWarning, EventAutoUpdateCircuitOpen,
			fmt.Sprintf("Auto-update circuit breaker open (rollbacks: %d), version %s available but not applied", status.RollbackCount, newVersion))
		SetAutoUpdateCircuit(claw.Namespace, claw.Name, true)
		apimeta.SetStatusCondition(&claw.Status.Conditions, metav1.Condition{
			Type: conditionAutoUpdateAvailable, Status: metav1.ConditionTrue, Reason: "NewVersionFound",
			Message: fmt.Sprintf("Version %s is available (circuit open)", newVersion), ObservedGeneration: claw.Generation,
		})
		if err := r.Status().Update(ctx, &claw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
		}
		return r.requeueAtNextCron(spec), nil
	}

	// Save a verified previous-good image before changing the target. A newly
	// created or unhealthy workload cannot establish a rollback baseline.
	var previous appsv1.StatefulSet
	if err := r.reader().Get(ctx, client.ObjectKeyFromObject(&claw), &previous); err != nil {
		return ctrl.Result{RequeueAfter: healthCheckPollInterval}, client.IgnoreNotFound(err)
	}
	previousImage := runtimeImage(previous.Spec.Template.Spec.Containers)
	ready, err := r.rolloutReady(ctx, &claw, &previous, previousImage)
	if err != nil || !ready {
		return ctrl.Result{RequeueAfter: healthCheckPollInterval}, err
	}

	// Initiate update.
	logger.Info("initiating auto-update", "from", status.CurrentVersion, "to", newVersion)
	r.Recorder.Event(&claw, corev1.EventTypeNormal, EventAutoUpdateAvailable,
		fmt.Sprintf("New version available: %s → %s", status.CurrentVersion, newVersion))
	r.Recorder.Event(&claw, corev1.EventTypeNormal, EventAutoUpdateStarting,
		fmt.Sprintf("Starting auto-update to version %s", newVersion))

	// Set target-image annotation and update phase.
	targetImage := baseImage + ":" + newVersion
	if claw.Annotations == nil {
		claw.Annotations = make(map[string]string)
	}
	claw.Annotations[annotationPreviousImage] = previousImage
	claw.Annotations[annotationTargetImage] = targetImage
	claw.Annotations[annotationUpdatePhase] = "HealthCheck"
	claw.Annotations[annotationUpdateStart] = now.Format(time.RFC3339Nano)
	status.LastUpdate = &now

	// Collect conditions to apply after re-fetch.
	pendingConditions := []metav1.Condition{
		{Type: conditionAutoUpdateAvailable, Status: metav1.ConditionTrue, Reason: "NewVersionFound",
			Message: fmt.Sprintf("Version %s is available", newVersion), ObservedGeneration: claw.Generation},
		{Type: conditionAutoUpdateInProgress, Status: metav1.ConditionTrue, Reason: "UpdateStarted",
			Message: fmt.Sprintf("Updating to version %s", newVersion), ObservedGeneration: claw.Generation},
	}

	// Update annotations first, then re-fetch and merge status.
	if err := r.Update(ctx, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set target-image annotation: %w", err)
	}
	// Re-fetch to get updated resourceVersion before status update.
	if err := r.reader().Get(ctx, req.NamespacedName, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to re-fetch after annotation update: %w", err)
	}
	mergeAutoUpdateStatus(&claw, status)
	for _, c := range pendingConditions {
		apimeta.SetStatusCondition(&claw.Status.Conditions, c)
	}
	if err := r.Status().Update(ctx, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}

	return ctrl.Result{RequeueAfter: healthCheckPollInterval}, nil
}

// isCheckDue returns true if the next cron tick is in the past relative to lastCheck.
func (r *AutoUpdateReconciler) isCheckDue(schedule string, lastCheck, now time.Time) bool {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(schedule)
	if err != nil {
		return true // invalid schedule: check every reconcile
	}
	next := sched.Next(lastCheck)
	return now.After(next)
}

// requeueAtNextCron calculates the delay until the next cron tick.
func (r *AutoUpdateReconciler) requeueAtNextCron(spec *clawv1alpha1.AutoUpdateSpec) ctrl.Result {
	schedule := spec.Schedule
	if schedule == "" {
		schedule = defaultSchedule
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(schedule)
	if err != nil {
		return ctrl.Result{RequeueAfter: 1 * time.Hour} // fallback
	}
	now := r.clock().Now()
	next := sched.Next(now)
	delay := next.Sub(now)
	if delay < 1*time.Minute {
		delay = 1 * time.Minute
	}
	return ctrl.Result{RequeueAfter: delay}
}

// extractVersionFromImage extracts the tag from an image reference.
// e.g., "ghcr.io/prismer-ai/k8s4claw-openclaw:1.2.0" → "1.2.0"
// Handles port numbers: "registry:5000/org/repo:1.2.0" → "1.2.0"
func extractVersionFromImage(image string) string {
	// Find the last path segment to avoid confusion with port numbers.
	lastSlash := strings.LastIndex(image, "/")
	search := image
	if lastSlash >= 0 {
		search = image[lastSlash+1:]
	}
	if idx := strings.LastIndex(search, ":"); idx >= 0 {
		return search[idx+1:]
	}
	return ""
}

// trimVersionHistory caps the version history to maxVersionHistory entries.
func trimVersionHistory(status *clawv1alpha1.AutoUpdateStatus) {
	if len(status.VersionHistory) > maxVersionHistory {
		status.VersionHistory = status.VersionHistory[len(status.VersionHistory)-maxVersionHistory:]
	}
}

// containsString checks if a string slice contains a specific value.
func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// mergeAutoUpdateStatus applies local status changes into a freshly fetched Claw object,
// avoiding wholesale pointer replacement that could silently overwrite concurrent updates.
func mergeAutoUpdateStatus(claw *clawv1alpha1.Claw, local *clawv1alpha1.AutoUpdateStatus) {
	if claw.Status.AutoUpdate == nil {
		claw.Status.AutoUpdate = &clawv1alpha1.AutoUpdateStatus{}
	}
	s := claw.Status.AutoUpdate
	s.CurrentVersion = local.CurrentVersion
	s.AvailableVersion = local.AvailableVersion
	s.LastCheck = local.LastCheck
	s.LastUpdate = local.LastUpdate
	s.RollbackCount = local.RollbackCount
	s.CircuitOpen = local.CircuitOpen
	s.FailedVersions = local.FailedVersions
	s.VersionHistory = local.VersionHistory
	s.LastCompletedUpdate = local.LastCompletedUpdate
}

func (r *AutoUpdateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("claw-autoupdate").
		For(&clawv1alpha1.Claw{}).
		Complete(r)
}
