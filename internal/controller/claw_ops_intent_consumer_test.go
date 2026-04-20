package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// consumeOpsIntent — parse + generation guard
// ---------------------------------------------------------------------------

func TestConsumeOpsIntent_NoAnnotation(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{}
	intent, err := parseOpsIntent(claw)
	assert.NoError(t, err)
	assert.Nil(t, intent, "no annotation → nil intent")
}

func TestConsumeOpsIntent_ValidIntent(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				AnnotationOpsIntent:    `{"action":"bump-memory","params":{"target":"768Mi"},"generation":10,"source":"rule-engine"}`,
				AnnotationOpsIntentGen: "5",
			},
		},
	}
	intent, err := parseOpsIntent(claw)
	require.NoError(t, err)
	require.NotNil(t, intent)
	assert.Equal(t, "bump-memory", intent.Action)
	assert.Equal(t, "768Mi", intent.Params["target"])
	assert.Equal(t, int64(10), intent.Generation)
}

func TestConsumeOpsIntent_StaleGeneration(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				AnnotationOpsIntent:    `{"action":"bump-memory","params":{"target":"768Mi"},"generation":5,"source":"rule-engine"}`,
				AnnotationOpsIntentGen: "10",
			},
		},
	}
	intent, err := parseOpsIntent(claw)
	assert.NoError(t, err)
	assert.Nil(t, intent, "generation <= last processed → skip")
}

func TestConsumeOpsIntent_EqualGeneration(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				AnnotationOpsIntent:    `{"action":"bump-memory","params":{"target":"768Mi"},"generation":10,"source":"rule-engine"}`,
				AnnotationOpsIntentGen: "10",
			},
		},
	}
	intent, err := parseOpsIntent(claw)
	assert.NoError(t, err)
	assert.Nil(t, intent, "generation == last processed → skip")
}

func TestConsumeOpsIntent_MalformedJSON(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				AnnotationOpsIntent: `{bad json`,
			},
		},
	}
	intent, err := parseOpsIntent(claw)
	assert.Error(t, err)
	assert.Nil(t, intent)
}

func TestConsumeOpsIntent_UnknownAction(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				AnnotationOpsIntent: `{"action":"delete-namespace","params":{},"generation":1,"source":"rule-engine"}`,
			},
		},
	}
	intent, err := parseOpsIntent(claw)
	assert.Error(t, err)
	assert.Nil(t, intent)
}

func TestConsumeOpsIntent_NoGenAnnotation(t *testing.T) {
	t.Parallel()
	// Missing ops-intent-gen means lastProcessed=0, so any generation > 0 passes.
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				AnnotationOpsIntent: `{"action":"restart-pod","params":{},"generation":1,"source":"rule-engine"}`,
			},
		},
	}
	intent, err := parseOpsIntent(claw)
	require.NoError(t, err)
	require.NotNil(t, intent)
	assert.Equal(t, "restart-pod", intent.Action)
}

func TestConsumeOpsIntent_BadGenAnnotation(t *testing.T) {
	t.Parallel()
	// Non-numeric gen annotation → treated as 0.
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				AnnotationOpsIntent:    `{"action":"restart-pod","params":{},"generation":1,"source":"rule-engine"}`,
				AnnotationOpsIntentGen: "not-a-number",
			},
		},
	}
	intent, err := parseOpsIntent(claw)
	require.NoError(t, err)
	require.NotNil(t, intent, "bad gen annotation treated as 0, generation 1 > 0 passes")
}

// ---------------------------------------------------------------------------
// clearOpsIntentAnnotations
// ---------------------------------------------------------------------------

func TestClearOpsIntentAnnotations(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				AnnotationOpsIntent:    `{"action":"bump-memory","params":{},"generation":5,"source":"rule-engine"}`,
				AnnotationOpsIntentGen: "3",
				"some-other-key":       "keep-me",
			},
		},
	}
	updatedGen := clearOpsIntentAnnotations(claw, 5)
	assert.Equal(t, int64(5), updatedGen)
	assert.NotContains(t, claw.Annotations, AnnotationOpsIntent)
	assert.Equal(t, "5", claw.Annotations[AnnotationOpsIntentGen])
	assert.Equal(t, "keep-me", claw.Annotations["some-other-key"])
}

func TestClearOpsIntentAnnotations_NeverLowersGen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		existingGen  string
		processedGen int64
		wantGen      string
	}{
		{"processed > existing: advance", "10", 20, "20"},
		{"processed == existing: keep", "10", 10, "10"},
		{"processed < existing: keep (replay protection)", "100", 5, "100"},
		{"processed=0 (invalid intent path) must NOT reset", "100", 0, "100"},
		{"no existing gen: write processed", "", 7, "7"},
		{"no existing gen + processed=0: no write", "", 0, ""},
		{"malformed existing gen: treated as 0, advance", "abc", 5, "5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			anns := map[string]string{
				AnnotationOpsIntent: `{"action":"bump-memory","generation":1}`,
			}
			if tt.existingGen != "" {
				anns[AnnotationOpsIntentGen] = tt.existingGen
			}
			claw := &clawv1alpha1.Claw{
				ObjectMeta: metav1.ObjectMeta{Annotations: anns},
			}
			clearOpsIntentAnnotations(claw, tt.processedGen)
			assert.NotContains(t, claw.Annotations, AnnotationOpsIntent,
				"intent annotation must always be cleared")
			got := claw.Annotations[AnnotationOpsIntentGen]
			assert.Equal(t, tt.wantGen, got)
		})
	}
}

func TestCurrentIntentGen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		anns map[string]string
		want int64
	}{
		{"nil annotations", nil, 0},
		{"no gen key", map[string]string{"other": "1"}, 0},
		{"valid gen", map[string]string{AnnotationOpsIntentGen: "42"}, 42},
		{"malformed gen", map[string]string{AnnotationOpsIntentGen: "abc"}, 0},
		{"zero gen", map[string]string{AnnotationOpsIntentGen: "0"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claw := &clawv1alpha1.Claw{ObjectMeta: metav1.ObjectMeta{Annotations: tt.anns}}
			assert.Equal(t, tt.want, currentIntentGen(claw))
		})
	}
}

// ---------------------------------------------------------------------------
// buildIntentPatch — validate patch output per action
// ---------------------------------------------------------------------------

func TestBuildIntentPatch_BumpMemory(t *testing.T) {
	t.Parallel()
	intent := &OpsIntent{
		Action: "bump-memory",
		Params: map[string]string{"target": "1Gi"},
	}
	patch, err := buildIntentPatch(intent)
	require.NoError(t, err)
	require.NotNil(t, patch)
	assert.Equal(t, "bump-memory", patch.Action)
	assert.Equal(t, resource.MustParse("1Gi"), patch.MemoryLimit)
}

func TestBuildIntentPatch_BumpCPU(t *testing.T) {
	t.Parallel()
	intent := &OpsIntent{
		Action: "bump-cpu",
		Params: map[string]string{"target": "2"},
	}
	patch, err := buildIntentPatch(intent)
	require.NoError(t, err)
	assert.Equal(t, "bump-cpu", patch.Action)
	assert.Equal(t, resource.MustParse("2"), patch.CPULimit)
}

func TestBuildIntentPatch_RestartPod(t *testing.T) {
	t.Parallel()
	intent := &OpsIntent{
		Action: "restart-pod",
		Params: map[string]string{"strategy": "delete-oldest"},
	}
	patch, err := buildIntentPatch(intent)
	require.NoError(t, err)
	assert.Equal(t, "restart-pod", patch.Action)
}

func TestBuildIntentPatch_RolloutRestart(t *testing.T) {
	t.Parallel()
	intent := &OpsIntent{
		Action: "rollout-restart",
		Params: map[string]string{},
	}
	patch, err := buildIntentPatch(intent)
	require.NoError(t, err)
	assert.Equal(t, "rollout-restart", patch.Action)
}

func TestBuildIntentPatch_ScaleReplicas(t *testing.T) {
	t.Parallel()
	intent := &OpsIntent{
		Action: "scale-replicas",
		Params: map[string]string{"replicas": "3"},
	}
	patch, err := buildIntentPatch(intent)
	require.NoError(t, err)
	assert.Equal(t, "scale-replicas", patch.Action)
	assert.Equal(t, int32(3), patch.Replicas)
}

func TestBuildIntentPatch_BumpMemory_MissingTarget(t *testing.T) {
	t.Parallel()
	intent := &OpsIntent{
		Action: "bump-memory",
		Params: map[string]string{},
	}
	patch, err := buildIntentPatch(intent)
	assert.Error(t, err, "bump-memory without target param should fail")
	assert.Nil(t, patch)
}

func TestBuildIntentPatch_BumpCPU_InvalidQuantity(t *testing.T) {
	t.Parallel()
	intent := &OpsIntent{
		Action: "bump-cpu",
		Params: map[string]string{"target": "not-a-cpu-value"},
	}
	patch, err := buildIntentPatch(intent)
	assert.Error(t, err)
	assert.Nil(t, patch)
}

func TestBuildIntentPatch_ScaleReplicas_InvalidReplicas(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		replicas string
	}{
		{"not a number", "abc"},
		{"negative", "-1"},
		{"zero", "0"},
		{"too high", "100"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			intent := &OpsIntent{
				Action: "scale-replicas",
				Params: map[string]string{"replicas": tt.replicas},
			}
			patch, err := buildIntentPatch(intent)
			assert.Error(t, err)
			assert.Nil(t, patch)
		})
	}
}

func TestBuildIntentPatch_ScaleReplicas_MissingParam(t *testing.T) {
	t.Parallel()
	intent := &OpsIntent{
		Action: "scale-replicas",
		Params: map[string]string{},
	}
	patch, err := buildIntentPatch(intent)
	assert.Error(t, err)
	assert.Nil(t, patch)
}
