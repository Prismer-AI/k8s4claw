package controller

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// waitForSTS polls until the StatefulSet for the given Claw exists.
func waitForSTS(t *testing.T, nn types.NamespacedName) {
	t.Helper()
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var sts appsv1.StatefulSet
		err := k8sClient.Get(ctx, nn, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

// waitForIntentGen polls until the Claw's ops-intent-gen annotation equals the expected value.
// This is the authoritative signal that the intent was consumed — more reliable than
// checking for intent absence (which can false-positive due to informer cache lag).
func waitForIntentGen(t *testing.T, nn types.NamespacedName, expectedGen string) {
	t.Helper()
	waitForCondition(t, 15*time.Second, 200*time.Millisecond, func() (bool, error) {
		var fetched clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
			return false, err
		}
		return fetched.Annotations[AnnotationOpsIntentGen] == expectedGen, nil
	})
}

// writeIntentAnnotation patches the Claw to add an ops-intent annotation.
func writeIntentAnnotation(t *testing.T, nn types.NamespacedName, intent OpsIntent) {
	t.Helper()
	intentJSON, err := json.Marshal(intent)
	require.NoError(t, err)

	var latest clawv1alpha1.Claw
	require.NoError(t, k8sClient.Get(ctx, nn, &latest))
	patch := client.MergeFrom(latest.DeepCopy())
	if latest.Annotations == nil {
		latest.Annotations = make(map[string]string)
	}
	latest.Annotations[AnnotationOpsIntent] = string(intentJSON)
	require.NoError(t, k8sClient.Patch(ctx, &latest, patch))
}

// ---------------------------------------------------------------------------
// Integration test: bump-memory full loop
//
// 1. Create Claw → ClawReconciler creates StatefulSet
// 2. Write ops-intent annotation with bump-memory action
// 3. Wait for ClawReconciler to consume the intent (gen counter updated)
// 4. Verify: intent annotation cleared, generation counter bumped
// ---------------------------------------------------------------------------

func TestOpsIntentIntegration_BumpMemory(t *testing.T) {
	ns := fmt.Sprintf("test-intent-mem-%d", time.Now().UnixNano())
	createNamespace(t, ns)
	ensureTestSecret(t, ns)

	clawName := "intent-bump-mem"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	waitForSTS(t, nn)

	// Record original memory limit.
	var sts appsv1.StatefulSet
	require.NoError(t, k8sClient.Get(ctx, nn, &sts))
	var originalMemLimit resource.Quantity
	for _, c := range sts.Spec.Template.Spec.Containers {
		if c.Name == "runtime" {
			originalMemLimit = c.Resources.Limits[corev1.ResourceMemory]
			break
		}
	}
	t.Logf("original memory limit: %s", originalMemLimit.String())

	// Write bump-memory intent.
	writeIntentAnnotation(t, nn, OpsIntent{
		Action:     "bump-memory",
		Params:     map[string]string{"target": "2Gi"},
		Generation: 100,
		Source:     "rule-engine",
	})

	// Wait for intent consumption (gen counter = "100").
	waitForIntentGen(t, nn, "100")

	// Verify intent annotation was cleared.
	var postClaw clawv1alpha1.Claw
	require.NoError(t, k8sClient.Get(ctx, nn, &postClaw))
	_, hasIntent := postClaw.Annotations[AnnotationOpsIntent]
	assert.False(t, hasIntent, "intent annotation should be cleared after consumption")

	// Verify spec.resources was updated (this is how the change survives
	// ensureStatefulSet rebuild).
	require.NotNil(t, postClaw.Spec.Resources, "spec.resources must be set after bump-memory")
	require.NotNil(t, postClaw.Spec.Resources.Limits, "spec.resources.limits must be set")
	assert.Equal(t, resource.MustParse("2Gi"),
		postClaw.Spec.Resources.Limits[corev1.ResourceMemory],
		"bump-memory must write to claw.Spec.Resources.Limits")

	// Verify the StatefulSet picked up the new memory limit (regression test
	// for the ensureStatefulSet overwrite bug).
	waitForCondition(t, 15*time.Second, 200*time.Millisecond, func() (bool, error) {
		var postSTS appsv1.StatefulSet
		if err := k8sClient.Get(ctx, nn, &postSTS); err != nil {
			return false, err
		}
		for _, c := range postSTS.Spec.Template.Spec.Containers {
			if c.Name == "runtime" {
				lim := c.Resources.Limits[corev1.ResourceMemory]
				return lim.Equal(resource.MustParse("2Gi")), nil
			}
		}
		return false, nil
	})
}

// ---------------------------------------------------------------------------
// Integration test: rollout-restart full loop
// ---------------------------------------------------------------------------

func TestOpsIntentIntegration_RolloutRestart(t *testing.T) {
	ns := fmt.Sprintf("test-intent-restart-%d", time.Now().UnixNano())
	createNamespace(t, ns)
	ensureTestSecret(t, ns)

	clawName := "intent-rollout"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	waitForSTS(t, nn)

	// Write rollout-restart intent.
	writeIntentAnnotation(t, nn, OpsIntent{
		Action:     "rollout-restart",
		Params:     map[string]string{},
		Generation: 50,
		Source:     "companion-claw",
	})

	// Wait for consumption.
	waitForIntentGen(t, nn, "50")

	// Verify intent cleared.
	var postClaw clawv1alpha1.Claw
	require.NoError(t, k8sClient.Get(ctx, nn, &postClaw))
	_, hasIntent := postClaw.Annotations[AnnotationOpsIntent]
	assert.False(t, hasIntent, "intent annotation should be cleared")
}

// ---------------------------------------------------------------------------
// Integration test: stale generation is skipped
// ---------------------------------------------------------------------------

func TestOpsIntentIntegration_StaleGenerationSkipped(t *testing.T) {
	ns := fmt.Sprintf("test-intent-stale-%d", time.Now().UnixNano())
	createNamespace(t, ns)
	ensureTestSecret(t, ns)

	clawName := "intent-stale-gen"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	waitForSTS(t, nn)

	// First: consume a valid intent to set gen counter to 100.
	writeIntentAnnotation(t, nn, OpsIntent{
		Action:     "rollout-restart",
		Params:     map[string]string{},
		Generation: 100,
		Source:     "rule-engine",
	})
	waitForIntentGen(t, nn, "100")

	// Now write a stale intent (generation 50 < last processed 100).
	writeIntentAnnotation(t, nn, OpsIntent{
		Action:     "bump-memory",
		Params:     map[string]string{"target": "10Gi"},
		Generation: 50,
		Source:     "rule-engine",
	})

	// Wait for at least one reconcile to process the annotation change.
	// The stale intent should be ignored (gen 50 <= 100).
	time.Sleep(2 * time.Second)

	var postClaw clawv1alpha1.Claw
	require.NoError(t, k8sClient.Get(ctx, nn, &postClaw))
	assert.Equal(t, "100", postClaw.Annotations[AnnotationOpsIntentGen],
		"generation counter should remain at 100, stale intent ignored")
}

// ---------------------------------------------------------------------------
// Integration test: invalid intent is cleared with warning
// ---------------------------------------------------------------------------

func TestOpsIntentIntegration_InvalidIntentCleared(t *testing.T) {
	ns := fmt.Sprintf("test-intent-invalid-%d", time.Now().UnixNano())
	createNamespace(t, ns)
	ensureTestSecret(t, ns)

	clawName := "intent-invalid"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	waitForSTS(t, nn)

	// Write an invalid intent (unknown action). The reconciler should clear it.
	var latest clawv1alpha1.Claw
	require.NoError(t, k8sClient.Get(ctx, nn, &latest))
	patch := client.MergeFrom(latest.DeepCopy())
	if latest.Annotations == nil {
		latest.Annotations = make(map[string]string)
	}
	latest.Annotations[AnnotationOpsIntent] = `{"action":"delete-namespace","params":{},"generation":1,"source":"attacker"}`
	require.NoError(t, k8sClient.Patch(ctx, &latest, patch))

	// Invalid intents clear the annotation without lowering the replay-protection
	// high-water mark. Since no prior intent has been processed, gen is either
	// absent or preserved at its prior value. Either way, the intent annotation
	// must disappear.
	waitForCondition(t, 15*time.Second, 200*time.Millisecond, func() (bool, error) {
		var fetched clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
			return false, err
		}
		_, hasIntent := fetched.Annotations[AnnotationOpsIntent]
		return !hasIntent, nil
	})

	t.Log("invalid intent was correctly cleared by the reconciler")
}

// ---------------------------------------------------------------------------
// Integration test: invalid intent does NOT lower the generation counter
// ---------------------------------------------------------------------------

func TestOpsIntentIntegration_InvalidIntentPreservesGeneration(t *testing.T) {
	ns := fmt.Sprintf("test-intent-replay-%d", time.Now().UnixNano())
	createNamespace(t, ns)
	ensureTestSecret(t, ns)

	clawName := "intent-replay-guard"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: clawName, Namespace: ns},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	waitForSTS(t, nn)

	// Establish high-water mark at gen=500 via a valid rollout-restart intent.
	writeIntentAnnotation(t, nn, OpsIntent{
		Action:     "rollout-restart",
		Params:     map[string]string{},
		Generation: 500,
		Source:     "rule-engine",
	})
	waitForIntentGen(t, nn, "500")

	// Now write an invalid intent. Before the fix this reset gen to "0"; after
	// the fix it must preserve "500".
	var latest clawv1alpha1.Claw
	require.NoError(t, k8sClient.Get(ctx, nn, &latest))
	p := client.MergeFrom(latest.DeepCopy())
	latest.Annotations[AnnotationOpsIntent] = `{"action":"delete-namespace","params":{},"generation":999,"source":"attacker"}`
	require.NoError(t, k8sClient.Patch(ctx, &latest, p))

	// Wait for invalid intent to be cleared.
	waitForCondition(t, 15*time.Second, 200*time.Millisecond, func() (bool, error) {
		var fetched clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
			return false, err
		}
		_, hasIntent := fetched.Annotations[AnnotationOpsIntent]
		return !hasIntent, nil
	})

	// Gen must still be 500, not lowered to 0 or raised to 999 (attacker gen
	// should never be trusted).
	var postClaw clawv1alpha1.Claw
	require.NoError(t, k8sClient.Get(ctx, nn, &postClaw))
	assert.Equal(t, "500", postClaw.Annotations[AnnotationOpsIntentGen],
		"invalid intent must not alter the replay-protection high-water mark")
}

// ---------------------------------------------------------------------------
// Integration test: scale-replicas
// ---------------------------------------------------------------------------

func TestOpsIntentIntegration_ScaleReplicas(t *testing.T) {
	ns := fmt.Sprintf("test-intent-scale-%d", time.Now().UnixNano())
	createNamespace(t, ns)
	ensureTestSecret(t, ns)

	clawName := "intent-scale"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	waitForSTS(t, nn)

	// Write scale-replicas intent.
	writeIntentAnnotation(t, nn, OpsIntent{
		Action:     "scale-replicas",
		Params:     map[string]string{"replicas": "3"},
		Generation: 200,
		Source:     "companion-claw",
	})

	// Wait for consumption.
	waitForIntentGen(t, nn, "200")

	// Verify intent cleared.
	var postClaw clawv1alpha1.Claw
	require.NoError(t, k8sClient.Get(ctx, nn, &postClaw))
	_, hasIntent := postClaw.Annotations[AnnotationOpsIntent]
	assert.False(t, hasIntent, "intent annotation should be cleared")

	// Verify spec.replicas was updated (regression test: before the fix,
	// ensureStatefulSet reset replicas to hardcoded 1).
	require.NotNil(t, postClaw.Spec.Replicas, "spec.replicas must be set by scale-replicas")
	assert.Equal(t, int32(3), *postClaw.Spec.Replicas)

	// Verify the StatefulSet picked up replicas=3.
	waitForCondition(t, 15*time.Second, 200*time.Millisecond, func() (bool, error) {
		var postSTS appsv1.StatefulSet
		if err := k8sClient.Get(ctx, nn, &postSTS); err != nil {
			return false, err
		}
		return postSTS.Spec.Replicas != nil && *postSTS.Spec.Replicas == 3, nil
	})
}

// ---------------------------------------------------------------------------
// Integration test: rollout-restart annotation survives ensureStatefulSet
// rebuild (regression test for intent-patch overwrite bug)
// ---------------------------------------------------------------------------

func TestOpsIntentIntegration_RolloutRestartSurvivesRebuild(t *testing.T) {
	ns := fmt.Sprintf("test-intent-restart-rebuild-%d", time.Now().UnixNano())
	createNamespace(t, ns)
	ensureTestSecret(t, ns)

	clawName := "intent-restart-rebuild"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: clawName, Namespace: ns},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:     clawv1alpha1.RuntimeOpenClaw,
			Credentials: testCredentials(),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, claw))

	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	waitForSTS(t, nn)

	writeIntentAnnotation(t, nn, OpsIntent{
		Action:     "rollout-restart",
		Params:     map[string]string{},
		Generation: 42,
		Source:     "rule-engine",
	})
	waitForIntentGen(t, nn, "42")

	// The Claw annotation should hold the restart timestamp, and the STS pod
	// template should reflect it (ensureStatefulSet applies it via
	// applyResourceOverrides on every reconcile).
	waitForCondition(t, 15*time.Second, 200*time.Millisecond, func() (bool, error) {
		var postClaw clawv1alpha1.Claw
		if err := k8sClient.Get(ctx, nn, &postClaw); err != nil {
			return false, err
		}
		if postClaw.Annotations[AnnotationRestartedAt] == "" {
			return false, nil
		}
		var postSTS appsv1.StatefulSet
		if err := k8sClient.Get(ctx, nn, &postSTS); err != nil {
			return false, err
		}
		return postSTS.Spec.Template.Annotations[AnnotationRestartedAt] ==
			postClaw.Annotations[AnnotationRestartedAt], nil
	})
}
