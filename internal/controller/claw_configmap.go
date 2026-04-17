package controller

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

// ensureConfigMap creates or updates the ConfigMap for the given Claw.
func (r *ClawReconciler) ensureConfigMap(ctx context.Context, claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) error {
	logger := log.FromContext(ctx)

	desired, err := r.buildConfigMap(claw, adapter)
	if err != nil {
		return fmt.Errorf("failed to build ConfigMap: %w", err)
	}
	if err := controllerutil.SetControllerReference(claw, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on ConfigMap: %w", err)
	}

	var existing corev1.ConfigMap
	err = r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating ConfigMap", "name", desired.Name, "namespace", desired.Namespace)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create ConfigMap: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get ConfigMap: %w", err)
	}

	// Update the existing ConfigMap with desired data.
	existing.Data = desired.Data
	existing.Labels = desired.Labels
	if err := r.Update(ctx, &existing); err != nil {
		return fmt.Errorf("failed to update ConfigMap: %w", err)
	}

	return nil
}

// buildConfigMap constructs the desired ConfigMap for the given Claw and adapter.
func (r *ClawReconciler) buildConfigMap(claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) (*corev1.ConfigMap, error) {
	defaultJSON, err := defaultConfigJSON(claw, adapter)
	if err != nil {
		return nil, fmt.Errorf("failed to get default config JSON: %w", err)
	}

	var userJSON []byte
	if claw.Spec.Config != nil {
		userJSON = claw.Spec.Config.Raw
	}

	merged, err := mergeConfig(defaultJSON, userJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to merge config: %w", err)
	}

	labels := clawLabels(claw)

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-config", claw.Name),
			Namespace: claw.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			"config.json": merged,
		},
	}, nil
}

func defaultConfigJSON(claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) (string, error) {
	if claw.Spec.Runtime == clawv1alpha1.RuntimeHermesClaw {
		return "{}", nil
	}
	return clawruntime.DefaultConfigJSON(adapter)
}

// mergeConfig deep-merges user config over defaults. If userJSON is nil or
// empty, the defaultJSON is returned as-is.
func mergeConfig(defaultJSON string, userJSON []byte) (string, error) {
	if len(userJSON) == 0 {
		return defaultJSON, nil
	}

	var dst map[string]interface{}
	if err := json.Unmarshal([]byte(defaultJSON), &dst); err != nil {
		return "", fmt.Errorf("failed to unmarshal default config: %w", err)
	}

	var src map[string]interface{}
	if err := json.Unmarshal(userJSON, &src); err != nil {
		return "", fmt.Errorf("failed to unmarshal user config: %w", err)
	}

	merged := deepMerge(dst, src)

	data, err := json.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("failed to marshal merged config: %w", err)
	}
	return string(data), nil
}

// deepMerge recursively merges src into dst. Values in src override values in
// dst. When both src and dst have a map value for the same key, the maps are
// merged recursively.
func deepMerge(dst, src map[string]interface{}) map[string]interface{} {
	for key, srcVal := range src {
		dstVal, exists := dst[key]
		if !exists {
			dst[key] = srcVal
			continue
		}

		srcMap, srcIsMap := srcVal.(map[string]interface{})
		dstMap, dstIsMap := dstVal.(map[string]interface{})
		if srcIsMap && dstIsMap {
			dst[key] = deepMerge(dstMap, srcMap)
		} else {
			dst[key] = srcVal
		}
	}
	return dst
}
