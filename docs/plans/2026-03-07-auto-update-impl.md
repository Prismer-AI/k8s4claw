# Auto-Update Controller Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add an auto-update controller that monitors OCI registries for new runtime image tags, applies updates with health verification, and rolls back on failure with circuit-breaker protection.

**Architecture:** A dedicated `AutoUpdateReconciler` watches Claw resources with `spec.autoUpdate.enabled=true`. It queries the OCI registry on a cron schedule, filters tags by semver constraint, and drives a state machine (Idle → Updating → HealthCheck → Running/Rollback). It coordinates with the existing `ClawReconciler` via a `claw.prismer.ai/target-image` annotation.

**Tech Stack:** Go 1.25, controller-runtime v0.23.1, Docker Registry V2 API, `github.com/Masterminds/semver/v3`

---

## Task 1: Add CRD Types (AutoUpdateSpec + AutoUpdateStatus)

**Files:**
- Modify: `api/v1alpha1/claw_types.go:9-53` (add AutoUpdate field to ClawSpec and ClawStatus)
- Modify: `api/v1alpha1/common_types.go` (add new types at end of file)

**Step 1: Add AutoUpdateSpec and related types to `common_types.go`**

Append at the end of `api/v1alpha1/common_types.go` (after `BackpressureSpec`):

```go
// AutoUpdateSpec configures automatic version updates.
type AutoUpdateSpec struct {
	// Enabled controls whether auto-update is active.
	Enabled bool `json:"enabled"`

	// VersionConstraint is a semver constraint (e.g., "~1.x", "^2.0.0").
	// +optional
	VersionConstraint string `json:"versionConstraint,omitempty"`

	// Schedule is a cron expression for version checks (default: "0 3 * * *").
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// HealthTimeout is how long to wait for Pod readiness after update (default: "10m", range: 2m-30m).
	// +optional
	HealthTimeout string `json:"healthTimeout,omitempty"`

	// MaxRollbacks is the circuit breaker threshold (default: 3).
	// +optional
	MaxRollbacks int `json:"maxRollbacks,omitempty"`
}

// AutoUpdateStatus reports auto-update state.
type AutoUpdateStatus struct {
	// CurrentVersion is the currently running image tag.
	// +optional
	CurrentVersion string `json:"currentVersion,omitempty"`

	// AvailableVersion is the latest version found in the registry.
	// +optional
	AvailableVersion string `json:"availableVersion,omitempty"`

	// LastCheck is when the registry was last queried.
	// +optional
	LastCheck *metav1.Time `json:"lastCheck,omitempty"`

	// LastUpdate is when the last update was applied.
	// +optional
	LastUpdate *metav1.Time `json:"lastUpdate,omitempty"`

	// RollbackCount is the number of consecutive rollbacks.
	// +optional
	RollbackCount int `json:"rollbackCount,omitempty"`

	// FailedVersions are versions that failed health checks and will be skipped.
	// +optional
	FailedVersions []string `json:"failedVersions,omitempty"`

	// CircuitOpen is true when rollbackCount >= maxRollbacks.
	// +optional
	CircuitOpen bool `json:"circuitOpen,omitempty"`

	// VersionHistory records past update attempts.
	// +optional
	VersionHistory []VersionHistoryEntry `json:"versionHistory,omitempty"`
}

// VersionHistoryEntry records a single update attempt.
type VersionHistoryEntry struct {
	// Version is the image tag.
	Version string `json:"version"`

	// AppliedAt is when the update was applied.
	AppliedAt metav1.Time `json:"appliedAt"`

	// Status is "Healthy" or "RolledBack".
	Status string `json:"status"`
}
```

Note: `common_types.go` already imports `metav1` indirectly via the corev1 types. You need to add `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"` to the imports.

**Step 2: Add AutoUpdate fields to ClawSpec and ClawStatus**

In `api/v1alpha1/claw_types.go`, add to `ClawSpec` (after `SelfConfigure`):

```go
	// AutoUpdate configures automatic version updates.
	// +optional
	AutoUpdate *AutoUpdateSpec `json:"autoUpdate,omitempty"`
```

Add to `ClawStatus` (after `Persistence`):

```go
	// AutoUpdate reports auto-update state.
	// +optional
	AutoUpdate *AutoUpdateStatus `json:"autoUpdate,omitempty"`
```

**Step 3: Verify build**

Run: `go build ./...`
Expected: PASS

**Step 4: Commit**

```bash
git add api/v1alpha1/claw_types.go api/v1alpha1/common_types.go
git commit -m "feat: add AutoUpdateSpec and AutoUpdateStatus CRD types"
```

---

## Task 2: OCI Registry Tag Resolver

**Files:**
- Create: `internal/registry/resolver.go`
- Create: `internal/registry/resolver_test.go`

**Step 1: Add semver dependency**

Run: `go get github.com/Masterminds/semver/v3`

**Step 2: Write the failing tests**

Create `internal/registry/resolver_test.go`:

```go
package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestListTags(t *testing.T) {
	// Mock a Docker Registry V2 API server.
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/prismer-ai/k8s4claw-openclaw/tags/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"name": "prismer-ai/k8s4claw-openclaw",
			"tags": []string{"1.0.0", "1.1.0", "1.2.0", "latest", "sha-abc123"},
		})
	})
	ts := httptest.NewTLSServer(mux)
	defer ts.Close()

	client := NewRegistryClient(WithHTTPClient(ts.Client()))
	tags, err := client.ListTags(context.Background(), ts.URL+"/v2/prismer-ai/k8s4claw-openclaw")
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 5 {
		t.Errorf("got %d tags, want 5", len(tags))
	}
}

func TestResolveBestVersion(t *testing.T) {
	tags := []string{"1.0.0", "1.1.0", "1.2.0", "2.0.0", "latest", "sha-abc"}

	tests := []struct {
		name       string
		constraint string
		current    string
		failed     []string
		want       string
		wantFound  bool
	}{
		{
			name:       "finds newer version within constraint",
			constraint: "^1.0.0",
			current:    "1.0.0",
			want:       "1.2.0",
			wantFound:  true,
		},
		{
			name:       "skips failed versions",
			constraint: "^1.0.0",
			current:    "1.0.0",
			failed:     []string{"1.2.0"},
			want:       "1.1.0",
			wantFound:  true,
		},
		{
			name:       "no newer version available",
			constraint: "^1.0.0",
			current:    "1.2.0",
			wantFound:  false,
		},
		{
			name:       "major version constraint filters",
			constraint: "^2.0.0",
			current:    "1.0.0",
			want:       "2.0.0",
			wantFound:  true,
		},
		{
			name:       "non-semver tags are ignored",
			constraint: "^1.0.0",
			current:    "0.9.0",
			want:       "1.2.0",
			wantFound:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := ResolveBestVersion(tags, tt.constraint, tt.current, tt.failed)
			if found != tt.wantFound {
				t.Errorf("found = %v, want %v", found, tt.wantFound)
			}
			if found && got != tt.want {
				t.Errorf("got = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCachedResolver(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/test/repo/tags/list", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(map[string]any{
			"tags": []string{"1.0.0", "1.1.0"},
		})
	})
	ts := httptest.NewTLSServer(mux)
	defer ts.Close()

	client := NewRegistryClient(
		WithHTTPClient(ts.Client()),
		WithCacheTTL(1*time.Minute),
	)
	image := ts.URL + "/v2/test/repo"

	// First call hits the server.
	_, err := client.ListTags(context.Background(), image)
	if err != nil {
		t.Fatalf("first ListTags: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("callCount = %d, want 1", callCount)
	}

	// Second call should be cached.
	_, err = client.ListTags(context.Background(), image)
	if err != nil {
		t.Fatalf("second ListTags: %v", err)
	}
	if callCount != 1 {
		t.Errorf("callCount = %d, want 1 (cached)", callCount)
	}
}

func TestTokenExchange(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"token": "test-token-123",
		})
	})
	mux.HandleFunc("/v2/org/repo/tags/list", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"tags": []string{"1.0.0"},
		})
	})
	ts := httptest.NewTLSServer(mux)
	defer ts.Close()

	client := NewRegistryClient(
		WithHTTPClient(ts.Client()),
		WithTokenURL(ts.URL+"/token"),
	)

	tags, err := client.ListTags(context.Background(), ts.URL+"/v2/org/repo")
	if err != nil {
		t.Fatalf("ListTags with token: %v", err)
	}
	if len(tags) != 1 || tags[0] != "1.0.0" {
		t.Errorf("tags = %v, want [1.0.0]", tags)
	}
}

func TestParseImageRef(t *testing.T) {
	tests := []struct {
		image      string
		wantDigest bool
	}{
		{"ghcr.io/prismer-ai/k8s4claw-openclaw:1.0.0", false},
		{"ghcr.io/prismer-ai/k8s4claw-openclaw:latest", false},
		{"ghcr.io/prismer-ai/k8s4claw-openclaw@sha256:abc123def", true},
	}
	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			got := IsDigestPinned(tt.image)
			if got != tt.wantDigest {
				t.Errorf("IsDigestPinned(%q) = %v, want %v", tt.image, got, tt.wantDigest)
			}
		})
	}
}
```

**Step 3: Run tests to verify they fail**

Run: `go test -race ./internal/registry/`
Expected: FAIL — package doesn't exist yet

**Step 4: Implement the registry resolver**

Create `internal/registry/resolver.go`:

```go
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
)

// RegistryClient queries an OCI registry for available image tags.
type RegistryClient struct {
	httpClient *http.Client
	tokenURL   string
	cacheTTL   time.Duration

	mu    sync.Mutex
	cache map[string]*cacheEntry
}

type cacheEntry struct {
	tags      []string
	expiresAt time.Time
}

// Option configures a RegistryClient.
type Option func(*RegistryClient)

// WithHTTPClient sets a custom HTTP client (useful for testing with TLS).
func WithHTTPClient(c *http.Client) Option {
	return func(r *RegistryClient) { r.httpClient = c }
}

// WithCacheTTL sets the TTL for cached tag lists.
func WithCacheTTL(d time.Duration) Option {
	return func(r *RegistryClient) { r.cacheTTL = d }
}

// WithTokenURL sets the token exchange URL for authenticated registries.
func WithTokenURL(url string) Option {
	return func(r *RegistryClient) { r.tokenURL = url }
}

// NewRegistryClient creates a new RegistryClient.
func NewRegistryClient(opts ...Option) *RegistryClient {
	c := &RegistryClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		cacheTTL:   15 * time.Minute,
		cache:      make(map[string]*cacheEntry),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// tagsResponse is the Docker Registry V2 tags/list response.
type tagsResponse struct {
	Tags []string `json:"tags"`
}

// tokenResponse is the Docker token exchange response.
type tokenResponse struct {
	Token string `json:"token"`
}

// ListTags queries the registry for available tags.
// The image parameter should be the base URL without /tags/list suffix,
// e.g., "https://ghcr.io/v2/prismer-ai/k8s4claw-openclaw".
func (c *RegistryClient) ListTags(ctx context.Context, image string) ([]string, error) {
	// Check cache.
	c.mu.Lock()
	if entry, ok := c.cache[image]; ok && time.Now().Before(entry.expiresAt) {
		tags := append([]string(nil), entry.tags...)
		c.mu.Unlock()
		return tags, nil
	}
	c.mu.Unlock()

	tagsURL := image + "/tags/list"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tagsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// If token URL is configured, exchange for a bearer token.
	if c.tokenURL != "" {
		token, err := c.exchangeToken(ctx, image)
		if err != nil {
			return nil, fmt.Errorf("failed to exchange token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list tags: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned status %d", resp.StatusCode)
	}

	var result tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode tags response: %w", err)
	}

	// Update cache.
	c.mu.Lock()
	c.cache[image] = &cacheEntry{
		tags:      append([]string(nil), result.Tags...),
		expiresAt: time.Now().Add(c.cacheTTL),
	}
	c.mu.Unlock()

	return result.Tags, nil
}

func (c *RegistryClient) exchangeToken(ctx context.Context, image string) (string, error) {
	// Extract scope from image URL.
	// e.g., "https://ghcr.io/v2/prismer-ai/k8s4claw-openclaw" → "repository:prismer-ai/k8s4claw-openclaw:pull"
	scope := extractScope(image)

	tokenURL := c.tokenURL + "?scope=" + scope
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create token request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange returned status %d", resp.StatusCode)
	}

	var result tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}
	return result.Token, nil
}

// extractScope derives the Docker scope from an image URL.
func extractScope(image string) string {
	// Strip protocol and /v2/ prefix.
	u := image
	for _, prefix := range []string{"https://", "http://"} {
		u = strings.TrimPrefix(u, prefix)
	}
	// Remove host.
	if idx := strings.Index(u, "/"); idx >= 0 {
		u = u[idx+1:]
	}
	// Remove /v2/ prefix.
	u = strings.TrimPrefix(u, "v2/")
	return "repository:" + u + ":pull"
}

// ResolveBestVersion finds the highest semver tag that satisfies the constraint,
// is greater than current, and is not in the failed list.
func ResolveBestVersion(tags []string, constraint, current string, failedVersions []string) (string, bool) {
	c, err := semver.NewConstraint(constraint)
	if err != nil {
		return "", false
	}

	var currentVer *semver.Version
	if current != "" {
		currentVer, _ = semver.NewVersion(current)
	}

	failedSet := make(map[string]bool, len(failedVersions))
	for _, f := range failedVersions {
		failedSet[f] = true
	}

	var best *semver.Version
	for _, tag := range tags {
		v, err := semver.NewVersion(tag)
		if err != nil {
			continue // skip non-semver tags like "latest", "sha-abc"
		}
		if !c.Check(v) {
			continue
		}
		if failedSet[v.Original()] {
			continue
		}
		if currentVer != nil && !v.GreaterThan(currentVer) {
			continue
		}
		if best == nil || v.GreaterThan(best) {
			best = v
		}
	}

	if best == nil {
		return "", false
	}
	return best.Original(), true
}

// IsDigestPinned returns true if the image reference uses a digest (@sha256:...).
func IsDigestPinned(image string) bool {
	return strings.Contains(image, "@sha256:")
}

// ImageForRuntime returns the base OCI image reference for a runtime type.
// This is used to construct the registry URL for tag listing.
func ImageForRuntime(runtime string) string {
	switch runtime {
	case "openclaw":
		return "ghcr.io/prismer-ai/k8s4claw-openclaw"
	case "nanoclaw":
		return "ghcr.io/prismer-ai/k8s4claw-nanoclaw"
	case "zeroclaw":
		return "ghcr.io/prismer-ai/k8s4claw-zeroclaw"
	case "picoclaw":
		return "ghcr.io/prismer-ai/k8s4claw-picoclaw"
	default:
		return ""
	}
}

// RegistryURLForImage converts a base image ref to a registry API URL.
// e.g., "ghcr.io/prismer-ai/k8s4claw-openclaw" → "https://ghcr.io/v2/prismer-ai/k8s4claw-openclaw"
func RegistryURLForImage(image string) string {
	parts := strings.SplitN(image, "/", 2)
	if len(parts) != 2 {
		return ""
	}
	return "https://" + parts[0] + "/v2/" + parts[1]
}

// TokenURLForRegistry returns the token exchange URL for a registry host.
func TokenURLForRegistry(registryHost string) string {
	switch registryHost {
	case "ghcr.io":
		return "https://ghcr.io/token"
	case "registry-1.docker.io", "docker.io":
		return "https://auth.docker.io/token"
	default:
		return ""
	}
}
```

**Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/registry/`
Expected: PASS

**Step 6: Verify full build**

Run: `go build ./...`
Expected: PASS

**Step 7: Commit**

```bash
git add internal/registry/ go.mod go.sum
git commit -m "feat: add OCI registry tag resolver with semver filtering and TTL cache"
```

---

## Task 3: Auto-Update Events and Metrics

**Files:**
- Modify: `internal/controller/events.go`
- Modify: `internal/controller/metrics.go`

**Step 1: Add auto-update event constants**

In `internal/controller/events.go`, add after existing constants:

```go
	EventAutoUpdateAvailable  = "AutoUpdateAvailable"
	EventAutoUpdateStarting   = "AutoUpdateStarting"
	EventAutoUpdateComplete   = "AutoUpdateComplete"
	EventAutoUpdateRollback   = "AutoUpdateRollback"
	EventAutoUpdateCircuitOpen = "AutoUpdateCircuitOpen"
```

**Step 2: Add auto-update metrics**

In `internal/controller/metrics.go`, add new metric vars after `resourceCreationFailures`:

```go
	autoUpdateChecks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "claw_autoupdate_checks_total",
		Help: "Total auto-update version checks.",
	}, []string{"namespace"})

	autoUpdateResults = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "claw_autoupdate_updates_total",
		Help: "Total auto-update attempts by result.",
	}, []string{"namespace", "result"})

	autoUpdateCircuit = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claw_autoupdate_circuit_open",
		Help: "Whether the auto-update circuit breaker is open (1=open, 0=closed).",
	}, []string{"namespace", "instance"})
```

Add helper functions after existing helpers:

```go
// RecordAutoUpdateCheck increments the version check counter.
func RecordAutoUpdateCheck(namespace string) {
	autoUpdateChecks.WithLabelValues(namespace).Inc()
}

// RecordAutoUpdateResult increments the update result counter.
// result should be "success" or "rollback".
func RecordAutoUpdateResult(namespace, result string) {
	autoUpdateResults.WithLabelValues(namespace, result).Inc()
}

// SetAutoUpdateCircuit sets the circuit breaker gauge.
func SetAutoUpdateCircuit(namespace, instance string, open bool) {
	val := float64(0)
	if open {
		val = 1
	}
	autoUpdateCircuit.WithLabelValues(namespace, instance).Set(val)
}
```

**Step 3: Verify build**

Run: `go build ./...`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/controller/events.go internal/controller/metrics.go
git commit -m "feat: add auto-update events and metrics"
```

---

## Task 4: AutoUpdateReconciler

**Files:**
- Create: `internal/controller/autoupdate_controller.go`
- Create: `internal/controller/autoupdate_controller_test.go`

**Step 1: Write the tests**

Create `internal/controller/autoupdate_controller_test.go`:

```go
package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// mockTagLister is a test double for registry.RegistryClient.
type mockTagLister struct {
	tags []string
	err  error
}

func (m *mockTagLister) ListTags(_ context.Context, _ string) ([]string, error) {
	return m.tags, m.err
}

func newTestAutoUpdateReconciler(objs []runtime.Object, lister TagLister) *AutoUpdateReconciler {
	scheme := runtime.NewScheme()
	_ = clawv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(&clawv1alpha1.Claw{}).
		Build()

	return &AutoUpdateReconciler{
		Client:    client,
		Scheme:    scheme,
		Recorder:  record.NewFakeRecorder(10),
		TagLister: lister,
	}
}

func TestAutoUpdate_SkipsWhenDisabled(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			// AutoUpdate is nil — disabled.
		},
	}

	r := newTestAutoUpdateReconciler([]runtime.Object{claw}, &mockTagLister{})
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Should not requeue (no auto-update work to do).
	if result.Requeue || result.RequeueAfter > 0 {
		t.Errorf("expected no requeue, got %+v", result)
	}
}

func TestAutoUpdate_SkipsDigestPinnedImage(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			Annotations: map[string]string{
				"claw.prismer.ai/target-image": "ghcr.io/prismer-ai/k8s4claw-openclaw@sha256:abc123",
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime:    clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{Enabled: true, VersionConstraint: "^1.0.0"},
		},
	}

	r := newTestAutoUpdateReconciler([]runtime.Object{claw}, &mockTagLister{tags: []string{"1.1.0"}})
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Should still requeue (auto-update enabled) but NOT set available version.
	_ = result
}

func TestAutoUpdate_FindsNewVersion(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:           true,
				VersionConstraint: "^1.0.0",
				Schedule:          "* * * * *", // every minute for testing
			},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{
				CurrentVersion: "1.0.0",
			},
		},
	}

	lister := &mockTagLister{tags: []string{"1.0.0", "1.1.0", "1.2.0", "latest"}}
	r := newTestAutoUpdateReconciler([]runtime.Object{claw}, lister)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Re-fetch and check status.
	var updated clawv1alpha1.Claw
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if updated.Status.AutoUpdate == nil {
		t.Fatal("AutoUpdate status is nil")
	}
	if updated.Status.AutoUpdate.AvailableVersion != "1.2.0" {
		t.Errorf("AvailableVersion = %q, want 1.2.0", updated.Status.AutoUpdate.AvailableVersion)
	}
	// Should have set target-image annotation.
	ann := updated.Annotations["claw.prismer.ai/target-image"]
	if ann == "" {
		t.Error("target-image annotation not set")
	}
}

func TestAutoUpdate_CircuitBreakerBlocks(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:           true,
				VersionConstraint: "^1.0.0",
				MaxRollbacks:      3,
			},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{
				CurrentVersion: "1.0.0",
				RollbackCount:  3,
				CircuitOpen:    true,
			},
		},
	}

	lister := &mockTagLister{tags: []string{"1.1.0"}}
	r := newTestAutoUpdateReconciler([]runtime.Object{claw}, lister)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Should NOT set target-image annotation when circuit is open.
	var updated clawv1alpha1.Claw
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ann := updated.Annotations["claw.prismer.ai/target-image"]; ann != "" {
		t.Errorf("target-image should not be set when circuit is open, got %q", ann)
	}
}

func TestAutoUpdate_HealthCheckSuccess(t *testing.T) {
	now := metav1.Now()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			Annotations: map[string]string{
				"claw.prismer.ai/target-image":  "ghcr.io/prismer-ai/k8s4claw-openclaw:1.2.0",
				"claw.prismer.ai/update-phase":  "HealthCheck",
				"claw.prismer.ai/update-started": now.Format(time.RFC3339),
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:           true,
				VersionConstraint: "^1.0.0",
				HealthTimeout:     "10m",
			},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{
				CurrentVersion:   "1.0.0",
				AvailableVersion: "1.2.0",
			},
		},
	}

	// Create a ready StatefulSet.
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: 1,
		},
	}

	r := newTestAutoUpdateReconciler([]runtime.Object{claw, sts}, &mockTagLister{})
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var updated clawv1alpha1.Claw
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if updated.Status.AutoUpdate.CurrentVersion != "1.2.0" {
		t.Errorf("CurrentVersion = %q, want 1.2.0", updated.Status.AutoUpdate.CurrentVersion)
	}
	if updated.Annotations["claw.prismer.ai/update-phase"] != "" {
		t.Errorf("update-phase should be cleared, got %q", updated.Annotations["claw.prismer.ai/update-phase"])
	}
}

func TestAutoUpdate_HealthCheckTimeout_Rollback(t *testing.T) {
	pastTime := metav1.NewTime(time.Now().Add(-15 * time.Minute))
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			Annotations: map[string]string{
				"claw.prismer.ai/target-image":  "ghcr.io/prismer-ai/k8s4claw-openclaw:1.2.0",
				"claw.prismer.ai/update-phase":  "HealthCheck",
				"claw.prismer.ai/update-started": pastTime.Format(time.RFC3339),
			},
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:           true,
				VersionConstraint: "^1.0.0",
				HealthTimeout:     "10m",
				MaxRollbacks:      3,
			},
		},
		Status: clawv1alpha1.ClawStatus{
			AutoUpdate: &clawv1alpha1.AutoUpdateStatus{
				CurrentVersion:   "1.0.0",
				AvailableVersion: "1.2.0",
				RollbackCount:    0,
			},
		},
	}

	// StatefulSet NOT ready.
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: 0,
		},
	}

	r := newTestAutoUpdateReconciler([]runtime.Object{claw, sts}, &mockTagLister{})
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var updated clawv1alpha1.Claw
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Should have rolled back.
	if updated.Status.AutoUpdate.RollbackCount != 1 {
		t.Errorf("RollbackCount = %d, want 1", updated.Status.AutoUpdate.RollbackCount)
	}
	if len(updated.Status.AutoUpdate.FailedVersions) != 1 || updated.Status.AutoUpdate.FailedVersions[0] != "1.2.0" {
		t.Errorf("FailedVersions = %v, want [1.2.0]", updated.Status.AutoUpdate.FailedVersions)
	}
	// target-image annotation should be removed (rollback).
	if ann := updated.Annotations["claw.prismer.ai/target-image"]; ann != "" {
		t.Errorf("target-image should be cleared on rollback, got %q", ann)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/controller/ -run TestAutoUpdate`
Expected: FAIL — `AutoUpdateReconciler` not defined

**Step 3: Implement AutoUpdateReconciler**

Create `internal/controller/autoupdate_controller.go`:

```go
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
	annotationTargetImage  = "claw.prismer.ai/target-image"
	annotationUpdatePhase  = "claw.prismer.ai/update-phase"
	annotationUpdateStart  = "claw.prismer.ai/update-started"

	defaultSchedule      = "0 3 * * *"
	defaultHealthTimeout = 10 * time.Minute
	defaultMaxRollbacks  = 3

	// healthCheckPollInterval is how often we requeue during health check.
	healthCheckPollInterval = 15 * time.Second
)

// TagLister abstracts registry tag listing for testability.
type TagLister interface {
	ListTags(ctx context.Context, image string) ([]string, error)
}

// AutoUpdateReconciler checks for new image versions and manages the update lifecycle.
type AutoUpdateReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder
	TagLister TagLister
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

	if status.LastCheck != nil && !r.isCheckDue(schedule, status.LastCheck.Time) {
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

	// Update both annotations (via main object Update) and status.
	if err := r.Update(ctx, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set target-image annotation: %w", err)
	}
	if err := r.Status().Update(ctx, &claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}

	apimeta.SetStatusCondition(&claw.Status.Conditions, metav1.Condition{
		Type:               "AutoUpdateAvailable",
		Status:             metav1.ConditionTrue,
		Reason:             "NewVersionFound",
		Message:            fmt.Sprintf("Version %s is available", newVersion),
		ObservedGeneration: claw.Generation,
		LastTransitionTime: now,
	})

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
			if time.Since(startedAt) > healthTimeout {
				return r.rollback(ctx, claw, "StatefulSet not found within health timeout")
			}
			return ctrl.Result{RequeueAfter: healthCheckPollInterval}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get StatefulSet: %w", err)
	}

	if sts.Status.ReadyReplicas >= 1 {
		// Health check passed.
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
			Status:    "Healthy",
		})

		SetAutoUpdateCircuit(claw.Namespace, claw.Name, false)
		RecordAutoUpdateResult(claw.Namespace, "success")

		// Clear update annotations.
		delete(claw.Annotations, annotationUpdatePhase)
		delete(claw.Annotations, annotationUpdateStart)

		if err := r.Update(ctx, claw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to clear update annotations: %w", err)
		}
		if err := r.Status().Update(ctx, claw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status after health check: %w", err)
		}

		return r.requeueAtNextCron(spec), nil
	}

	// Check timeout.
	if time.Since(startedAt) > healthTimeout {
		logger.Info("health check timeout", "elapsed", time.Since(startedAt), "timeout", healthTimeout)
		return r.rollback(ctx, claw, "health check timed out")
	}

	logger.Info("waiting for health check", "elapsed", time.Since(startedAt), "timeout", healthTimeout)
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

	// Add to failed versions.
	status.FailedVersions = append(status.FailedVersions, failedVersion)
	status.RollbackCount++
	status.VersionHistory = append(status.VersionHistory, clawv1alpha1.VersionHistoryEntry{
		Version:   failedVersion,
		AppliedAt: metav1.Now(),
		Status:    "RolledBack",
	})

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

	// Remove target-image annotation to revert to default.
	delete(claw.Annotations, annotationTargetImage)
	delete(claw.Annotations, annotationUpdatePhase)
	delete(claw.Annotations, annotationUpdateStart)

	if err := r.Update(ctx, claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to clear annotations on rollback: %w", err)
	}
	if err := r.Status().Update(ctx, claw); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status on rollback: %w", err)
	}

	return r.requeueAtNextCron(spec), nil
}

// isCheckDue returns true if the next cron tick is in the past relative to lastCheck.
func (r *AutoUpdateReconciler) isCheckDue(schedule string, lastCheck time.Time) bool {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(schedule)
	if err != nil {
		return true // invalid schedule: check every reconcile
	}
	next := sched.Next(lastCheck)
	return time.Now().After(next)
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
	next := sched.Next(time.Now())
	delay := time.Until(next)
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

func (r *AutoUpdateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clawv1alpha1.Claw{}).
		Complete(r)
}
```

**Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/controller/ -run TestAutoUpdate`
Expected: PASS

**Step 5: Verify full build**

Run: `go build ./...`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/controller/autoupdate_controller.go internal/controller/autoupdate_controller_test.go
git commit -m "feat: add AutoUpdateReconciler with version check, health verification, and rollback"
```

---

## Task 5: Wire AutoUpdateReconciler + ClawReconciler Integration

**Files:**
- Modify: `cmd/operator/main.go:82-107` (add AutoUpdateReconciler registration)
- Modify: `internal/controller/claw_controller.go:251-283` (read target-image annotation)

**Step 1: Modify `ensureStatefulSet` to read target-image annotation**

In `internal/controller/claw_controller.go`, modify `buildStatefulSet` (around line 347-424). After `podTemplate := adapter.PodTemplate(claw)` (line 354), add logic to override the runtime container image if the `target-image` annotation is set:

```go
	// Auto-update: override runtime image if target-image annotation is set.
	if targetImage := claw.Annotations["claw.prismer.ai/target-image"]; targetImage != "" {
		for i := range podTemplate.Spec.Containers {
			if podTemplate.Spec.Containers[i].Name == "runtime" {
				podTemplate.Spec.Containers[i].Image = targetImage
				break
			}
		}
	}
```

Add this block right after line 354 (`podTemplate := adapter.PodTemplate(claw)`), before the labels are applied (line 357).

**Step 2: Wire AutoUpdateReconciler in main.go**

In `cmd/operator/main.go`, add the import for the registry package:

```go
	"github.com/Prismer-AI/k8s4claw/internal/registry"
```

After the existing controller registrations (after line 107, before line 109 "Register admission webhooks"), add:

```go
	// Register auto-update controller.
	registryClient := registry.NewRegistryClient()
	if err := (&controller.AutoUpdateReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorderFor("autoupdate-controller"),
		TagLister: registryClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AutoUpdate")
		os.Exit(1)
	}
```

**Step 3: Verify build**

Run: `go build ./...`
Expected: PASS

**Step 4: Verify existing tests still pass**

Run: `go test -race ./internal/controller/ -run TestAutoUpdate`
Expected: PASS

Run: `go test -race ./...`
Expected: PASS (may have some tests that require envtest assets)

**Step 5: Commit**

```bash
git add cmd/operator/main.go internal/controller/claw_controller.go
git commit -m "feat: wire AutoUpdateReconciler and integrate target-image annotation in ClawReconciler"
```

---

## Task 6: Webhook Validation for AutoUpdate Fields

**Files:**
- Modify: `internal/webhook/claw_validator.go`
- Modify: `internal/webhook/claw_validator_test.go`

**Step 1: Write failing tests**

Add to `internal/webhook/claw_validator_test.go`:

```go
func TestValidateAutoUpdate_InvalidConstraint(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:           true,
				VersionConstraint: "not-a-semver",
			},
		},
	}
	errs := validateAutoUpdate(claw)
	if len(errs) == 0 {
		t.Error("expected error for invalid version constraint")
	}
}

func TestValidateAutoUpdate_InvalidSchedule(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:  true,
				Schedule: "not-a-cron",
			},
		},
	}
	errs := validateAutoUpdate(claw)
	if len(errs) == 0 {
		t.Error("expected error for invalid schedule")
	}
}

func TestValidateAutoUpdate_HealthTimeoutRange(t *testing.T) {
	tests := []struct {
		name    string
		timeout string
		wantErr bool
	}{
		{"valid 5m", "5m", false},
		{"valid 10m", "10m", false},
		{"too short", "1m", true},
		{"too long", "1h", true},
		{"invalid", "not-a-duration", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claw := &clawv1alpha1.Claw{
				Spec: clawv1alpha1.ClawSpec{
					Runtime: clawv1alpha1.RuntimeOpenClaw,
					AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
						Enabled:       true,
						HealthTimeout: tt.timeout,
					},
				},
			}
			errs := validateAutoUpdate(claw)
			if (len(errs) > 0) != tt.wantErr {
				t.Errorf("wantErr = %v, got %d errors: %v", tt.wantErr, len(errs), errs)
			}
		})
	}
}

func TestValidateAutoUpdate_MaxRollbacksPositive(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:      true,
				MaxRollbacks: -1,
			},
		},
	}
	errs := validateAutoUpdate(claw)
	if len(errs) == 0 {
		t.Error("expected error for negative maxRollbacks")
	}
}

func TestValidateAutoUpdate_Disabled_NoValidation(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			AutoUpdate: &clawv1alpha1.AutoUpdateSpec{
				Enabled:           false,
				VersionConstraint: "invalid",
			},
		},
	}
	errs := validateAutoUpdate(claw)
	if len(errs) != 0 {
		t.Errorf("expected no errors when disabled, got %d: %v", len(errs), errs)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/webhook/ -run TestValidateAutoUpdate`
Expected: FAIL — `validateAutoUpdate` not defined

**Step 3: Implement validation**

In `internal/webhook/claw_validator.go`, add imports:

```go
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/robfig/cron/v3"
```

Add `validateAutoUpdate` function:

```go
// validateAutoUpdate validates auto-update spec fields.
func validateAutoUpdate(obj *clawv1alpha1.Claw) field.ErrorList {
	au := obj.Spec.AutoUpdate
	if au == nil || !au.Enabled {
		return nil
	}

	var allErrs field.ErrorList
	basePath := field.NewPath("spec", "autoUpdate")

	if au.VersionConstraint != "" {
		if _, err := semver.NewConstraint(au.VersionConstraint); err != nil {
			allErrs = append(allErrs, field.Invalid(
				basePath.Child("versionConstraint"),
				au.VersionConstraint,
				"must be a valid semver constraint (e.g., ^1.0.0, ~1.x)",
			))
		}
	}

	if au.Schedule != "" {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(au.Schedule); err != nil {
			allErrs = append(allErrs, field.Invalid(
				basePath.Child("schedule"),
				au.Schedule,
				"must be a valid cron expression",
			))
		}
	}

	if au.HealthTimeout != "" {
		d, err := time.ParseDuration(au.HealthTimeout)
		if err != nil {
			allErrs = append(allErrs, field.Invalid(
				basePath.Child("healthTimeout"),
				au.HealthTimeout,
				"must be a valid duration",
			))
		} else if d < 2*time.Minute || d > 30*time.Minute {
			allErrs = append(allErrs, field.Invalid(
				basePath.Child("healthTimeout"),
				au.HealthTimeout,
				"must be between 2m and 30m",
			))
		}
	}

	if au.MaxRollbacks < 0 {
		allErrs = append(allErrs, field.Invalid(
			basePath.Child("maxRollbacks"),
			au.MaxRollbacks,
			"must be >= 0",
		))
	}

	return allErrs
}
```

Wire it into `ValidateCreate` and `ValidateUpdate` by adding `allErrs = append(allErrs, validateAutoUpdate(obj)...)` in both methods (after the existing validation calls).

In `ValidateCreate` (around line 22-24):
```go
	allErrs = append(allErrs, validateAutoUpdate(obj)...)
```

In `ValidateUpdate` (around line 35-37):
```go
	allErrs = append(allErrs, validateAutoUpdate(newObj)...)
```

**Step 4: Run tests**

Run: `go test -race ./internal/webhook/ -run TestValidateAutoUpdate`
Expected: PASS

Run: `go test -race ./internal/webhook/`
Expected: PASS (all existing tests still pass)

**Step 5: Verify full build**

Run: `go build ./...`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/webhook/claw_validator.go internal/webhook/claw_validator_test.go
git commit -m "feat: add webhook validation for auto-update fields"
```

---

## Verification

```bash
# Full build
go build ./...

# All unit tests
go test -race -cover ./internal/registry/
go test -race -cover ./internal/controller/ -run TestAutoUpdate
go test -race -cover ./internal/webhook/

# Full test suite
go test -race ./...
```
