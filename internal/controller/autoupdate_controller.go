package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	annotationTargetImage = "claw.prismer.ai/target-image"
	annotationUpdatePhase = "claw.prismer.ai/update-phase"
	annotationUpdateStart = "claw.prismer.ai/update-started"

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

func (realClock) Now() time.Time                { return time.Now() }
func (realClock) Since(t time.Time) time.Duration { return time.Since(t) }

// AutoUpdateReconciler checks for new image versions and manages the update lifecycle.
type AutoUpdateReconciler struct {
	client.Client
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

func (r *AutoUpdateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	now := metav1.Now()
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
	claw.Annotations[annotationTargetImage] = targetImage
	claw.Annotations[annotationUpdatePhase] = "HealthCheck"
	claw.Annotations[annotationUpdateStart] = now.Format(time.RFC3339)
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
	if err := r.Get(ctx, req.NamespacedName, &claw); err != nil {
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

func (r *AutoUpdateReconciler) reconcileHealthCheck(ctx context.Context, claw *clawv1alpha1.Claw) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	status := claw.Status.AutoUpdate
	spec := claw.Spec.AutoUpdate

	targetImage := claw.Annotations[annotationTargetImage]
	startedStr := claw.Annotations[annotationUpdateStart]
	startedAt, err := time.Parse(time.RFC3339, startedStr)
	if err != nil {
		logger.Error(err, "failed to parse update-started annotation")
		return r.rollback(ctx, claw, "invalid start time")
	}

	healthTimeout := defaultHealthTimeout
	if spec.HealthTimeout != "" {
		if d, err := time.ParseDuration(spec.HealthTimeout); err == nil {
			healthTimeout = d
		}
	}

	// Check StatefulSet readiness.
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKeyFromObject(claw), &sts); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("StatefulSet not found during health check")
			if r.clock().Since(startedAt) > healthTimeout {
				return r.rollback(ctx, claw, "StatefulSet not found within health timeout")
			}
			return ctrl.Result{RequeueAfter: healthCheckPollInterval}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get StatefulSet: %w", err)
	}

	desiredReplicas := int32(1)
	if sts.Spec.Replicas != nil {
		desiredReplicas = *sts.Spec.Replicas
	}
	if sts.Status.UpdatedReplicas >= desiredReplicas && sts.Status.ReadyReplicas >= desiredReplicas {
		// Health check passed — all replicas are updated and ready.
		logger.Info("auto-update health check passed", "version", status.AvailableVersion)
		r.Recorder.Event(claw, corev1.EventTypeNormal, EventAutoUpdateComplete,
			fmt.Sprintf("Auto-update to version %s completed successfully", status.AvailableVersion))

		// Extract version from target image.
		version := extractVersionFromImage(targetImage)
		status.CurrentVersion = version
		status.RollbackCount = 0
		status.CircuitOpen = false
		status.VersionHistory = append(status.VersionHistory, clawv1alpha1.VersionHistoryEntry{
			Version:   version,
			AppliedAt: metav1.Now(),
			Status:    clawv1alpha1.VersionHistoryHealthy,
		})
		trimVersionHistory(status)

		SetAutoUpdateCircuit(claw.Namespace, claw.Name, false)
		RecordAutoUpdateResult(claw.Namespace, "success")

		pendingConditions := []metav1.Condition{
			{Type: conditionAutoUpdateAvailable, Status: metav1.ConditionFalse, Reason: "UpToDate",
				Message: fmt.Sprintf("Running version %s", version), ObservedGeneration: claw.Generation},
			{Type: conditionAutoUpdateInProgress, Status: metav1.ConditionFalse, Reason: "UpdateComplete",
				Message: fmt.Sprintf("Successfully updated to version %s", version), ObservedGeneration: claw.Generation},
		}

		// Clear update annotations.
		delete(claw.Annotations, annotationUpdatePhase)
		delete(claw.Annotations, annotationUpdateStart)

		if err := r.Update(ctx, claw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to clear update annotations: %w", err)
		}
		// Re-fetch to get updated resourceVersion before status update.
		if err := r.Get(ctx, client.ObjectKeyFromObject(claw), claw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to re-fetch after annotation update: %w", err)
		}
		mergeAutoUpdateStatus(claw, status)
		for _, c := range pendingConditions {
			apimeta.SetStatusCondition(&claw.Status.Conditions, c)
		}
		if err := r.Status().Update(ctx, claw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status after health check: %w", err)
		}

		return r.requeueAtNextCron(spec), nil
	}

	// Check timeout.
	if r.clock().Since(startedAt) > healthTimeout {
		logger.Info("health check timeout", "elapsed", r.clock().Since(startedAt), "timeout", healthTimeout)
		return r.rollback(ctx, claw, "health check timed out")
	}

	logger.Info("waiting for health check", "elapsed", r.clock().Since(startedAt), "timeout", healthTimeout)
	return ctrl.Result{RequeueAfter: healthCheckPollInterval}, nil
}

func (r *AutoUpdateReconciler) rollback(ctx context.Context, claw *clawv1alpha1.Claw, reason string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	status := claw.Status.AutoUpdate
	spec := claw.Spec.AutoUpdate

	failedVersion := status.AvailableVersion
	logger.Info("rolling back auto-update", "failedVersion", failedVersion, "reason", reason)

	r.Recorder.Event(claw, corev1.EventTypeWarning, EventAutoUpdateRollback,
		fmt.Sprintf("Rolling back version %s: %s", failedVersion, reason))

	// Add to failed versions (deduplicate).
	if !containsString(status.FailedVersions, failedVersion) {
		status.FailedVersions = append(status.FailedVersions, failedVersion)
	}
	status.RollbackCount++
	status.VersionHistory = append(status.VersionHistory, clawv1alpha1.VersionHistoryEntry{
		Version:   failedVersion,
		AppliedAt: metav1.Now(),
		Status:    clawv1alpha1.VersionHistoryRolledBack,
	})
	trimVersionHistory(status)

	RecordAutoUpdateResult(claw.Namespace, "rollback")

	// Check circuit breaker.
	maxRollbacks := defaultMaxRollbacks
	if spec.MaxRollbacks > 0 {
		maxRollbacks = spec.MaxRollbacks
	}
	if status.RollbackCount >= maxRollbacks {
		status.CircuitOpen = true
		SetAutoUpdateCircuit(claw.Namespace, claw.Name, true)
		r.Recorder.Event(claw, corev1.EventTypeWarning, EventAutoUpdateCircuitOpen,
			fmt.Sprintf("Circuit breaker opened after %d rollbacks", status.RollbackCount))
	}

	pendingConditions := []metav1.Condition{
		{Type: conditionAutoUpdateInProgress, Status: metav1.ConditionFalse, Reason: "RolledBack",
			Message: fmt.Sprintf("Version %s rolled back: %s", failedVersion, reason), ObservedGeneration: claw.Generation},
	}

	// Remove target-image annotation to revert to default.
	delete(claw.Annotations, annotationTargetImage)
	delete(claw.Annotations, annotationUpdatePhase)
	delete(claw.Annotations, annotationUpdateStart)

	if err := r.Update(ctx, claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to clear annotations on rollback: %w", err)
	}
	// Re-fetch to get updated resourceVersion before status update.
	if err := r.Get(ctx, client.ObjectKeyFromObject(claw), claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to re-fetch after rollback annotation update: %w", err)
	}
	mergeAutoUpdateStatus(claw, status)
	for _, c := range pendingConditions {
		apimeta.SetStatusCondition(&claw.Status.Conditions, c)
	}
	if err := r.Status().Update(ctx, claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status on rollback: %w", err)
	}

	return r.requeueAtNextCron(spec), nil
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
func extractVersionFromImage(image string) string {
	if idx := strings.LastIndex(image, ":"); idx >= 0 {
		return image[idx+1:]
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
}

func (r *AutoUpdateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clawv1alpha1.Claw{}).
		Complete(r)
}
