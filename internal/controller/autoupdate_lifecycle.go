package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func (r *AutoUpdateReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func runtimeImage(containers []corev1.Container) string {
	for i := range containers {
		if containers[i].Name == "runtime" {
			return containers[i].Image
		}
	}
	return ""
}

func podReady(pod *corev1.Pod) bool {
	if !pod.DeletionTimestamp.IsZero() {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// Counts alone may still describe the previous template. Require the target
// image, observed generation, converged revision and each actual owned Pod.
func (r *AutoUpdateReconciler) rolloutReady(ctx context.Context, claw *clawv1alpha1.Claw, sts *appsv1.StatefulSet, image string) (bool, error) {
	n := int32(1)
	if sts.Spec.Replicas != nil {
		n = *sts.Spec.Replicas
	}
	if image == "" || n <= 0 || !metav1.IsControlledBy(sts, claw) ||
		runtimeImage(sts.Spec.Template.Spec.Containers) != image ||
		sts.Status.ObservedGeneration < sts.Generation ||
		sts.Status.UpdateRevision == "" || sts.Status.CurrentRevision != sts.Status.UpdateRevision ||
		sts.Status.UpdatedReplicas != n || sts.Status.ReadyReplicas != n {
		return false, nil
	}
	pods, err := r.ownedPods(ctx, sts)
	if err != nil {
		return false, err
	}
	var ready int32
	for i := range pods {
		p := &pods[i]
		if !p.DeletionTimestamp.IsZero() {
			return false, nil
		}
		if !podReady(p) || runtimeImage(p.Spec.Containers) != image || p.Labels[appsv1.ControllerRevisionHashLabelKey] != sts.Status.UpdateRevision {
			return false, nil
		}
		ready++
	}
	return ready == n, nil
}

func (r *AutoUpdateReconciler) ownedPods(ctx context.Context, sts *appsv1.StatefulSet) ([]corev1.Pod, error) {
	if sts.Spec.Selector == nil {
		return nil, fmt.Errorf("StatefulSet %s has no selector", sts.Name)
	}
	selector, err := metav1.LabelSelectorAsSelector(sts.Spec.Selector)
	if err != nil {
		return nil, err
	}
	var list corev1.PodList
	if err := r.reader().List(ctx, &list, client.InNamespace(sts.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}
	var pods []corev1.Pod
	for i := range list.Items {
		if metav1.IsControlledBy(&list.Items[i], sts) {
			pods = append(pods, list.Items[i])
		}
	}
	return pods, nil
}

func (r *AutoUpdateReconciler) reconcileHealthCheck(ctx context.Context, claw *clawv1alpha1.Claw) (ctrl.Result, error) {
	started, err := time.Parse(time.RFC3339Nano, claw.Annotations[annotationUpdateStart])
	if err != nil {
		return r.rollback(ctx, claw, "invalid start time")
	}
	target := claw.Annotations[annotationTargetImage]
	// A crash after the status commit but before annotation cleanup must not
	// account for this outcome again or turn a completed update into a rollback.
	if claw.Status.AutoUpdate.LastCompletedUpdate == outcomeID(claw, target, clawv1alpha1.VersionHistoryHealthy) {
		return r.finishPhase(ctx, claw, "UpdateComplete", "Target image is healthy")
	}
	var sts appsv1.StatefulSet
	err = r.reader().Get(ctx, client.ObjectKeyFromObject(claw), &sts)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if err == nil {
		ready, checkErr := r.rolloutReady(ctx, claw, &sts, target)
		if checkErr != nil {
			return ctrl.Result{}, checkErr
		}
		if ready {
			if err := r.recordOutcome(ctx, claw, target, clawv1alpha1.VersionHistoryHealthy); err != nil {
				return ctrl.Result{}, err
			}
			return r.finishPhase(ctx, claw, "UpdateComplete", "Target image is healthy")
		}
	}
	timeout := defaultHealthTimeout
	if d, parseErr := time.ParseDuration(claw.Spec.AutoUpdate.HealthTimeout); parseErr == nil && d > 0 {
		timeout = d
	}
	if r.clock().Since(started) > timeout {
		return r.rollback(ctx, claw, "health check timed out")
	}
	return ctrl.Result{RequeueAfter: healthCheckPollInterval}, nil
}

// First persist the rollback target and phase together. Neither failed-version
// accounting nor Pod recovery depends on this process surviving the next step.
func (r *AutoUpdateReconciler) rollback(ctx context.Context, claw *clawv1alpha1.Claw, reason string) (ctrl.Result, error) {
	previous := claw.Annotations[annotationPreviousImage]
	if previous == "" {
		return ctrl.Result{}, fmt.Errorf("rollback blocked: no saved previous-good image for %s/%s", claw.Namespace, claw.Name)
	}
	claw.Annotations[annotationFailedImage] = claw.Annotations[annotationTargetImage]
	claw.Annotations[annotationTargetImage] = previous
	claw.Annotations[annotationUpdatePhase] = "RollingBack"
	claw.Annotations[annotationRollbackReason] = reason
	if err := r.Update(ctx, claw); err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcileRollback(ctx, claw)
}

func (r *AutoUpdateReconciler) reconcileRollback(ctx context.Context, claw *clawv1alpha1.Claw) (ctrl.Result, error) {
	previous, failed := claw.Annotations[annotationPreviousImage], claw.Annotations[annotationFailedImage]
	if previous == "" || failed == "" || claw.Annotations[annotationTargetImage] != previous {
		return ctrl.Result{}, fmt.Errorf("incomplete or changed rollback image state for %s/%s", claw.Namespace, claw.Name)
	}
	if err := r.recordOutcome(ctx, claw, failed, clawv1alpha1.VersionHistoryRolledBack); err != nil {
		return ctrl.Result{}, err
	}
	var sts appsv1.StatefulSet
	if err := r.reader().Get(ctx, client.ObjectKeyFromObject(claw), &sts); err != nil {
		return ctrl.Result{RequeueAfter: healthCheckPollInterval}, client.IgnoreNotFound(err)
	}
	ready, err := r.rolloutReady(ctx, claw, &sts, previous)
	if err != nil {
		return ctrl.Result{}, err
	}
	if ready {
		return r.finishPhase(ctx, claw, "RolledBack", "Previous-good image is healthy")
	}
	// StatefulSet rolling updates can remain stuck on an unready failed Pod
	// after a template rollback. Only remove that Pod once the restored template
	// has been observed; never remove healthy, new-revision or foreign Pods.
	if metav1.IsControlledBy(&sts, claw) && runtimeImage(sts.Spec.Template.Spec.Containers) == previous &&
		sts.Status.ObservedGeneration >= sts.Generation && sts.Status.UpdateRevision != "" {
		if err := r.removeFailedPods(ctx, &sts, failed); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: healthCheckPollInterval}, nil
}

func (r *AutoUpdateReconciler) removeFailedPods(ctx context.Context, sts *appsv1.StatefulSet, failed string) error {
	pods, err := r.ownedPods(ctx, sts)
	if err != nil {
		return err
	}
	for i := range pods {
		pod := &pods[i]
		if !pod.DeletionTimestamp.IsZero() || podReady(pod) || runtimeImage(pod.Spec.Containers) != failed ||
			pod.Labels[appsv1.ControllerRevisionHashLabelKey] == sts.Status.UpdateRevision {
			continue
		}
		uid, rv := pod.UID, pod.ResourceVersion
		if err := r.Delete(ctx, pod, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

func outcomeID(claw *clawv1alpha1.Claw, image string, outcome clawv1alpha1.VersionHistoryStatus) string {
	return claw.Annotations[annotationUpdateStart] + "|" + image + "|" + string(outcome)
}

func (r *AutoUpdateReconciler) updateStatus(ctx context.Context, claw *clawv1alpha1.Claw, mutate func(*clawv1alpha1.Claw)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var fresh clawv1alpha1.Claw
		if err := r.reader().Get(ctx, client.ObjectKeyFromObject(claw), &fresh); err != nil {
			return err
		}
		if fresh.Annotations[annotationUpdateStart] != claw.Annotations[annotationUpdateStart] ||
			fresh.Annotations[annotationUpdatePhase] != claw.Annotations[annotationUpdatePhase] ||
			fresh.Annotations[annotationTargetImage] != claw.Annotations[annotationTargetImage] {
			return fmt.Errorf("update operation changed while committing status")
		}
		base := fresh.DeepCopy()
		if fresh.Status.AutoUpdate == nil {
			fresh.Status.AutoUpdate = &clawv1alpha1.AutoUpdateStatus{}
		}
		mutate(&fresh)
		return r.Status().Patch(ctx, &fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	})
}

func (r *AutoUpdateReconciler) recordOutcome(ctx context.Context, claw *clawv1alpha1.Claw, image string, outcome clawv1alpha1.VersionHistoryStatus) error {
	id := outcomeID(claw, image, outcome)
	recorded := false
	circuitOpen := false
	err := r.updateStatus(ctx, claw, func(fresh *clawv1alpha1.Claw) {
		recorded = false
		s := fresh.Status.AutoUpdate
		if s.LastCompletedUpdate == id {
			return
		}
		version := extractVersionFromImage(image)
		s.LastCompletedUpdate = id
		s.VersionHistory = append(s.VersionHistory, clawv1alpha1.VersionHistoryEntry{Version: version, AppliedAt: metav1.NewTime(r.clock().Now()), Status: outcome})
		trimVersionHistory(s)
		if outcome == clawv1alpha1.VersionHistoryHealthy {
			s.CurrentVersion = version
			s.RollbackCount, s.CircuitOpen = 0, false
			apimeta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{Type: conditionAutoUpdateAvailable, Status: metav1.ConditionFalse, Reason: "UpToDate", Message: "Target image is healthy", ObservedGeneration: fresh.Generation})
		} else {
			if !containsString(s.FailedVersions, version) {
				s.FailedVersions = append(s.FailedVersions, version)
			}
			s.RollbackCount++
			maxRollbacks := defaultMaxRollbacks
			if fresh.Spec.AutoUpdate.MaxRollbacks > 0 {
				maxRollbacks = fresh.Spec.AutoUpdate.MaxRollbacks
			}
			s.CircuitOpen = s.RollbackCount >= maxRollbacks
			apimeta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{Type: conditionAutoUpdateInProgress, Status: metav1.ConditionTrue, Reason: "RollingBack", Message: fresh.Annotations[annotationRollbackReason], ObservedGeneration: fresh.Generation})
		}
		circuitOpen = s.CircuitOpen
		recorded = true
	})
	if err == nil && recorded {
		SetAutoUpdateCircuit(claw.Namespace, claw.Name, circuitOpen)
		if outcome == clawv1alpha1.VersionHistoryHealthy {
			RecordAutoUpdateResult(claw.Namespace, "success")
			r.Recorder.Event(claw, corev1.EventTypeNormal, EventAutoUpdateComplete, "Target image is healthy")
		} else {
			RecordAutoUpdateResult(claw.Namespace, "rollback")
			r.Recorder.Event(claw, corev1.EventTypeWarning, EventAutoUpdateRollback, "Restoring saved previous-good image: "+claw.Annotations[annotationRollbackReason])
			if circuitOpen {
				r.Recorder.Event(claw, corev1.EventTypeWarning, EventAutoUpdateCircuitOpen, "Auto-update circuit breaker opened after consecutive rollbacks")
			}
		}
	}
	return err
}

// Status is committed before the phase is removed. On conflict/crash the next
// reconcile resumes the phase, with LastCompletedUpdate preventing duplicates.
func (r *AutoUpdateReconciler) finishPhase(ctx context.Context, claw *clawv1alpha1.Claw, reason, message string) (ctrl.Result, error) {
	if err := r.updateStatus(ctx, claw, func(fresh *clawv1alpha1.Claw) {
		if reason == "RolledBack" {
			fresh.Status.AutoUpdate.CurrentVersion = extractVersionFromImage(fresh.Annotations[annotationTargetImage])
		}
		apimeta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{Type: conditionAutoUpdateInProgress, Status: metav1.ConditionFalse, Reason: reason, Message: message, ObservedGeneration: fresh.Generation})
	}); err != nil {
		return ctrl.Result{}, err
	}
	var fresh clawv1alpha1.Claw
	if err := r.reader().Get(ctx, client.ObjectKeyFromObject(claw), &fresh); err != nil {
		return ctrl.Result{}, err
	}
	if fresh.Annotations[annotationUpdateStart] != claw.Annotations[annotationUpdateStart] || fresh.Annotations[annotationUpdatePhase] != claw.Annotations[annotationUpdatePhase] || fresh.Annotations[annotationTargetImage] != claw.Annotations[annotationTargetImage] {
		return ctrl.Result{}, fmt.Errorf("update operation changed before cleanup")
	}
	for _, key := range []string{annotationUpdatePhase, annotationUpdateStart, annotationPreviousImage, annotationFailedImage, annotationRollbackReason} {
		delete(fresh.Annotations, key)
	}
	if err := r.Update(ctx, &fresh); err != nil {
		return ctrl.Result{}, err
	}
	SetAutoUpdateCircuit(fresh.Namespace, fresh.Name, fresh.Status.AutoUpdate.CircuitOpen)
	return r.requeueAtNextCron(fresh.Spec.AutoUpdate), nil
}
