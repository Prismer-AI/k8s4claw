package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
)

const maxResponseBytes = 10 << 20 // 10 MB

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
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&result); err != nil {
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
	scope := extractScope(image)

	u, err := url.Parse(c.tokenURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse token URL: %w", err)
	}
	q := u.Query()
	q.Set("scope", scope)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}
	return result.Token, nil
}

// extractScope derives the Docker scope from an image URL.
func extractScope(image string) string {
	u := image
	for _, prefix := range []string{"https://", "http://"} {
		u = strings.TrimPrefix(u, prefix)
	}
	if idx := strings.Index(u, "/"); idx >= 0 {
		u = u[idx+1:]
	}
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
