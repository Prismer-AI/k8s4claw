package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveWatchNamespaces(t *testing.T) {
	// Not parallel — uses os.Setenv via t.Setenv.

	tests := []struct {
		name           string
		watchEnv       string
		podNamespace   string
		wantNamespaces []string
		wantScopeHas   string
	}{
		{
			name:           "empty_env_falls_back_to_pod_namespace",
			watchEnv:       "",
			podNamespace:   "team-a",
			wantNamespaces: []string{"team-a"},
			wantScopeHas:   "namespace=team-a",
		},
		{
			name:           "whitespace_env_falls_back_to_pod_namespace",
			watchEnv:       "   ",
			podNamespace:   "team-a",
			wantNamespaces: []string{"team-a"},
			wantScopeHas:   "namespace=team-a",
		},
		{
			name:           "wildcard_means_cluster_wide",
			watchEnv:       "*",
			podNamespace:   "ignored",
			wantNamespaces: nil,
			wantScopeHas:   "cluster-wide",
		},
		{
			name:           "single_namespace_in_env",
			watchEnv:       "team-a",
			podNamespace:   "ignored",
			wantNamespaces: []string{"team-a"},
			wantScopeHas:   "namespaces=team-a",
		},
		{
			name:           "comma_separated_list",
			watchEnv:       "team-a,team-b , team-c",
			podNamespace:   "ignored",
			wantNamespaces: []string{"team-a", "team-b", "team-c"},
			wantScopeHas:   "namespaces=team-a,team-b,team-c",
		},
		{
			name:           "whitespace_only_list_falls_back",
			watchEnv:       ", ,",
			podNamespace:   "team-a",
			wantNamespaces: []string{"team-a"},
			wantScopeHas:   "namespace=team-a",
		},
		{
			name:           "no_env_no_pod_ns_defaults_to_default",
			watchEnv:       "",
			podNamespace:   "",
			wantNamespaces: []string{"default"},
			wantScopeHas:   "namespace=default",
		},
	}

	logger := discardLogger()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CLAW4K8S_WATCH_NAMESPACES", tt.watchEnv)
			t.Setenv("CLAW4K8S_WATCH_NS", "")
			t.Setenv("POD_NAMESPACE", tt.podNamespace)

			namespaces, scope := resolveWatchNamespaces(logger)
			assert.Equal(t, tt.wantNamespaces, namespaces)
			assert.Contains(t, scope, tt.wantScopeHas)
		})
	}
}

// TestResolveWatchNamespaces_LegacyEnvFallback verifies that the deprecated
// CLAW4K8S_WATCH_NS env var is honored only when the canonical key is *unset*.
// Once the canonical key is exported (even as ""), legacy is ignored.
//
// Cases use sentinel string values ("UNSET") to express "unset", "" to express
// "set to empty", because t.Setenv only supports setting; explicit os.Unsetenv
// is used for the unset case.
func TestResolveWatchNamespaces_LegacyEnvFallback(t *testing.T) {
	const unset = "__UNSET__"

	tests := []struct {
		name           string
		canonical      string
		legacy         string
		podNamespace   string
		wantNamespaces []string
		wantScopeHas   string
	}{
		{
			name:           "legacy_used_when_canonical_unset",
			canonical:      unset,
			legacy:         "team-legacy",
			podNamespace:   "default",
			wantNamespaces: []string{"team-legacy"},
			wantScopeHas:   "namespaces=team-legacy",
		},
		{
			name:           "legacy_ignored_when_canonical_set_empty",
			canonical:      "",
			legacy:         "team-legacy",
			podNamespace:   "team-x",
			wantNamespaces: []string{"team-x"},
			wantScopeHas:   "namespace=team-x",
		},
		{
			name:           "canonical_wins_when_both_set",
			canonical:      "team-new",
			legacy:         "team-legacy",
			podNamespace:   "default",
			wantNamespaces: []string{"team-new"},
			wantScopeHas:   "namespaces=team-new",
		},
		{
			name:           "legacy_wildcard_works_when_canonical_unset",
			canonical:      unset,
			legacy:         "*",
			podNamespace:   "default",
			wantNamespaces: nil,
			wantScopeHas:   "cluster-wide",
		},
		{
			name:           "both_unset_falls_back_to_pod_ns",
			canonical:      unset,
			legacy:         unset,
			podNamespace:   "team-x",
			wantNamespaces: []string{"team-x"},
			wantScopeHas:   "namespace=team-x",
		},
	}

	logger := discardLogger()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setOrUnset(t, "CLAW4K8S_WATCH_NAMESPACES", tt.canonical, unset)
			setOrUnset(t, "CLAW4K8S_WATCH_NS", tt.legacy, unset)
			t.Setenv("POD_NAMESPACE", tt.podNamespace)

			namespaces, scope := resolveWatchNamespaces(logger)
			assert.Equal(t, tt.wantNamespaces, namespaces)
			assert.Contains(t, scope, tt.wantScopeHas)
		})
	}
}

// setOrUnset honors the unset-sentinel: if val == sentinel, the env var is
// removed for the duration of the test; otherwise it is set to val. The
// pre-test value is restored on test cleanup.
func setOrUnset(t *testing.T, key, val, sentinel string) {
	t.Helper()
	prev, hadPrev := os.LookupEnv(key)
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv(key, prev)
		} else {
			_ = os.Unsetenv(key)
		}
	})
	if val == sentinel {
		_ = os.Unsetenv(key)
	} else {
		_ = os.Setenv(key, val)
	}
}
