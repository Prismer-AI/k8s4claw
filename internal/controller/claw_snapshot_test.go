package controller

import (
	"fmt"
	"testing"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// reconcileSnapshots — envtest integration
// ---------------------------------------------------------------------------

func TestClawReconciler_SnapshotCreated(t *testing.T) {
	ns := fmt.Sprintf("test-snap-create-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "snap-create"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: clawName, Namespace: ns},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				Session: &clawv1alpha1.VolumeSpec{
					Enabled:   true,
					Size:      "1Gi",
					MountPath: "/data/session",
					Snapshot: &clawv1alpha1.SnapshotSpec{
						Enabled:  true,
						Schedule: "* * * * *", // every minute
						Retain:   3,
					},
				},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	// Wait for a VolumeSnapshot to be created.
	waitForCondition(t, 30*time.Second, 500*time.Millisecond, func() (bool, error) {
		var snapList snapshotv1.VolumeSnapshotList
		if err := k8sClient.List(ctx, &snapList,
			client.InNamespace(ns),
			client.MatchingLabels{"claw.prismer.ai/instance": clawName},
		); err != nil {
			return false, err
		}
		return len(snapList.Items) > 0, nil
	})

	// Verify the snapshot was created with correct labels and source.
	var snapList snapshotv1.VolumeSnapshotList
	require.NoError(t, k8sClient.List(ctx, &snapList,
		client.InNamespace(ns),
		client.MatchingLabels{"claw.prismer.ai/instance": clawName},
	))

	require.NotEmpty(t, snapList.Items)
	snap := snapList.Items[0]
	assert.Contains(t, snap.Name, clawName+"-session-")
	require.NotNil(t, snap.Spec.Source.PersistentVolumeClaimName)
	assert.Equal(t, fmt.Sprintf("session-%s-0", clawName), *snap.Spec.Source.PersistentVolumeClaimName)

	// Wait for last-snapshot-time annotation (reconciler retries on stale object conflict).
	annoKey := lastSnapshotTimeAnnotation + "-session"
	waitForCondition(t, 15*time.Second, 500*time.Millisecond, func() (bool, error) {
		var updated clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(claw), &updated); err != nil {
			return false, err
		}
		return updated.Annotations[annoKey] != "", nil
	})
}

func TestClawReconciler_SnapshotNotCreatedWithoutPersistence(t *testing.T) {
	ns := fmt.Sprintf("test-snap-nop-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "snap-nop"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: clawName, Namespace: ns},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimePicoClaw},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	// Wait for reconcile to complete (StatefulSet exists).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var sts appsv1.StatefulSet
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(claw), &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// No VolumeSnapshots should exist.
	var snapList snapshotv1.VolumeSnapshotList
	require.NoError(t, k8sClient.List(ctx, &snapList,
		client.InNamespace(ns),
		client.MatchingLabels{"claw.prismer.ai/instance": clawName},
	))
	assert.Empty(t, snapList.Items, "no snapshots should be created without persistence config")
}

// ---------------------------------------------------------------------------
// pruneSnapshots — envtest integration
// ---------------------------------------------------------------------------

func TestClawReconciler_SnapshotPruning(t *testing.T) {
	ns := fmt.Sprintf("test-snap-prune-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "snap-prune"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: clawName, Namespace: ns},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				Session: &clawv1alpha1.VolumeSpec{
					Enabled:   true,
					Size:      "1Gi",
					MountPath: "/data/session",
					Snapshot: &clawv1alpha1.SnapshotSpec{
						Enabled:  true,
						Schedule: "* * * * *",
						Retain:   2,
					},
				},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	// Pre-create 4 VolumeSnapshots to exceed retain=2.
	pvcName := fmt.Sprintf("session-%s-0", clawName)
	for i := 0; i < 4; i++ {
		snapName := fmt.Sprintf("%s-session-2026010%d-120000", clawName, i)
		snap := &snapshotv1.VolumeSnapshot{
			ObjectMeta: metav1.ObjectMeta{
				Name:      snapName,
				Namespace: ns,
				Labels:    map[string]string{"claw.prismer.ai/instance": clawName},
				// Stagger creation timestamps.
				CreationTimestamp: metav1.NewTime(time.Date(2026, 1, i+1, 12, 0, 0, 0, time.UTC)),
			},
			Spec: snapshotv1.VolumeSnapshotSpec{
				Source: snapshotv1.VolumeSnapshotSource{
					PersistentVolumeClaimName: &pvcName,
				},
			},
		}
		require.NoError(t, k8sClient.Create(ctx, snap))
	}

	// Wait for the reconciler's snapshot creation + pruning to run.
	// The reconciler will create a new snapshot (5th) and prune down to retain=2.
	waitForCondition(t, 30*time.Second, 500*time.Millisecond, func() (bool, error) {
		var snapList snapshotv1.VolumeSnapshotList
		if err := k8sClient.List(ctx, &snapList,
			client.InNamespace(ns),
			client.MatchingLabels{"claw.prismer.ai/instance": clawName},
		); err != nil {
			return false, err
		}
		// Count non-deleted snapshots.
		active := 0
		for i := range snapList.Items {
			if snapList.Items[i].DeletionTimestamp.IsZero() {
				active++
			}
		}
		// Should have pruned down to retain=2.
		return active <= 2, nil
	})

	// Final count.
	var snapList snapshotv1.VolumeSnapshotList
	require.NoError(t, k8sClient.List(ctx, &snapList,
		client.InNamespace(ns),
		client.MatchingLabels{"claw.prismer.ai/instance": clawName},
	))
	active := 0
	for i := range snapList.Items {
		if snapList.Items[i].DeletionTimestamp.IsZero() {
			active++
		}
	}
	assert.LessOrEqual(t, active, 2, "should have pruned to retain count (retain=2)")
	t.Logf("active snapshots after pruning: %d", active)
}

// ---------------------------------------------------------------------------
// buildVolumeSnapshot — unit test
// ---------------------------------------------------------------------------

func TestBuildVolumeSnapshot_Fields(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "my-claw", Namespace: "ns"},
	}
	snap := buildVolumeSnapshot(claw, "my-claw-session-20260101-120000", "session-my-claw-0", "csi-snapclass")

	assert.Equal(t, "my-claw-session-20260101-120000", snap.Name)
	assert.Equal(t, "ns", snap.Namespace)
	assert.Equal(t, "my-claw", snap.Labels["claw.prismer.ai/instance"])
	require.NotNil(t, snap.Spec.Source.PersistentVolumeClaimName)
	assert.Equal(t, "session-my-claw-0", *snap.Spec.Source.PersistentVolumeClaimName)
	require.NotNil(t, snap.Spec.VolumeSnapshotClassName)
	assert.Equal(t, "csi-snapclass", *snap.Spec.VolumeSnapshotClassName)
}

func TestBuildVolumeSnapshot_NoSnapshotClass(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
	}
	snap := buildVolumeSnapshot(claw, "snap-1", "pvc-1", "")
	assert.Nil(t, snap.Spec.VolumeSnapshotClassName, "should be nil when no class specified")
}
