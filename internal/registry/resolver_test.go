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

	_, err := client.ListTags(context.Background(), image)
	if err != nil {
		t.Fatalf("first ListTags: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("callCount = %d, want 1", callCount)
	}

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
