package controller

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// ensurePDB — full lifecycle via envtest
// ---------------------------------------------------------------------------

func TestClawReconciler_PDBCreated(t *testing.T) {
	ns := fmt.Sprintf("test-pdb-create-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "pdb-create"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: clawName, Namespace: ns},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimePicoClaw},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	// Wait for PDB to appear (default: enabled).
	var pdb policyv1.PodDisruptionBudget
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &pdb)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Verify spec.
	require.NotNil(t, pdb.Spec.MinAvailable)
	assert.Equal(t, int32(1), pdb.Spec.MinAvailable.IntVal, "default minAvailable should be 1")

	// Verify selector.
	require.NotNil(t, pdb.Spec.Selector)
	assert.Equal(t, clawName, pdb.Spec.Selector.MatchLabels["claw.prismer.ai/instance"])

	// Verify ownerReference.
	require.Len(t, pdb.OwnerReferences, 1)
	assert.Equal(t, "Claw", pdb.OwnerReferences[0].Kind)
}

func TestClawReconciler_PDBCustomMinAvailable(t *testing.T) {
	ns := fmt.Sprintf("test-pdb-custom-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "pdb-custom"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: clawName, Namespace: ns},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Availability: &clawv1alpha1.AvailabilitySpec{
				PDB: &clawv1alpha1.PDBSpec{Enabled: true, MinAvailable: 2},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	var pdb policyv1.PodDisruptionBudget
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &pdb)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	require.NotNil(t, pdb.Spec.MinAvailable)
	assert.Equal(t, int32(2), pdb.Spec.MinAvailable.IntVal, "custom minAvailable should be 2")
}

func TestClawReconciler_PDBDisabled(t *testing.T) {
	ns := fmt.Sprintf("test-pdb-disabled-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "pdb-disabled"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: clawName, Namespace: ns},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Availability: &clawv1alpha1.AvailabilitySpec{
				PDB: &clawv1alpha1.PDBSpec{Enabled: false},
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	// Wait for StatefulSet (proves reconcile ran).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var sts appsv1.StatefulSet
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Verify no PDB was created.
	var pdb policyv1.PodDisruptionBudget
	err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &pdb)
	assert.True(t, client.IgnoreNotFound(err) == nil, "PDB should not exist when disabled")
}

func TestClawReconciler_PDBDeletedOnDisable(t *testing.T) {
	ns := fmt.Sprintf("test-pdb-del-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "pdb-del"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: clawName, Namespace: ns},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimePicoClaw},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	// Wait for PDB to be created (default: enabled).
	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var pdb policyv1.PodDisruptionBudget
		err := k8sClient.Get(ctx, nn, &pdb)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Disable PDB by updating the Claw spec.
	var latest clawv1alpha1.Claw
	require.NoError(t, k8sClient.Get(ctx, nn, &latest))
	latest.Spec.Availability = &clawv1alpha1.AvailabilitySpec{
		PDB: &clawv1alpha1.PDBSpec{Enabled: false},
	}
	require.NoError(t, k8sClient.Update(ctx, &latest))

	// Wait for PDB to be deleted by reconciler.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var pdb policyv1.PodDisruptionBudget
		err := k8sClient.Get(ctx, nn, &pdb)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return true, nil // PDB gone = success
			}
			return false, err
		}
		// PDB still exists or has deletion timestamp.
		return !pdb.DeletionTimestamp.IsZero(), nil
	})
}
