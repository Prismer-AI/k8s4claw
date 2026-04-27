package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	controller "github.com/Prismer-AI/k8s4claw/internal/controller"
)

// HermesGatewayClient calls an OpenAI-compatible /v1/chat/completions endpoint.
// Works with hermes-agent-rs hermes-gateway, Anthropic API (via gateway model
// prefix), or any OpenAI-compatible provider.
//
// Concurrency: HermesGatewayClient is safe for concurrent use as long as
// httpClient is initialized at construction time (see buildLLMClient in main.go).
type HermesGatewayClient struct {
	BaseURL string // e.g. "http://hermes-gateway.default.svc.cluster.local:8080"
	Model   string // e.g. "anthropic/claude-sonnet-4-20250514"
	APIKey  string // optional; sent as Bearer token if non-empty

	// httpClient must be set by the caller (e.g., buildLLMClient). Never
	// initialized lazily inside Analyze to avoid data races under concurrent use.
	httpClient *http.Client
}

// chat completion wire types (OpenAI-compatible subset).
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type chatCompletionResponse struct {
	Choices []chatChoice `json:"choices"`
}

// Analyze sends the prompt to the chat completions endpoint and parses the
// response into (analysis, action). The action JSON is expected after an
// "ACTION:" marker or inside a ```json fenced block. The action JSON is
// validated against the operator's ops-intent whitelist (allowed actions only)
// before being returned; invalid actions are discarded and ("analysis", "")
// is returned with a nil error so the escalation falls through to
// AwaitingApproval.
func (c *HermesGatewayClient) Analyze(ctx context.Context, prompt string) (string, string, error) {
	if c.httpClient == nil {
		return "", "", fmt.Errorf("failed to call gateway: httpClient is nil (use buildLLMClient)")
	}

	reqBody := chatCompletionRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: companionSystemPrompt},
			{Role: "user", Content: prompt},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal request: %w", err)
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to call gateway: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		// Note: response body is intentionally NOT included to avoid leaking
		// API keys or other sensitive data that some proxies echo back.
		return "", "", fmt.Errorf("failed gateway request: status %d", resp.StatusCode)
	}

	var parsed chatCompletionResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", "", fmt.Errorf("failed to decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", "", fmt.Errorf("failed to parse response: no choices returned")
	}

	content := parsed.Choices[0].Message.Content
	analysis, rawAction := extractAction(content)

	// If the LLM proposed an action, validate it against the ops-intent schema
	// and stamp a server-side generation counter (LLM clocks are unreliable).
	if rawAction != "" {
		validated, err := validateAndStampAction(rawAction)
		if err != nil {
			// Invalid action — discard so the escalation falls through to
			// AwaitingApproval rather than writing garbage to ops-intent.
			return analysis, "", nil
		}
		return analysis, validated, nil
	}
	return analysis, "", nil
}

// validateAndStampAction validates the raw LLM action JSON against the
// ops-intent schema and overwrites the generation field with a server-side
// timestamp (LLMs cannot produce reliable monotonic timestamps).
func validateAndStampAction(raw string) (string, error) {
	intent, err := controller.ValidateIntent(raw)
	if err != nil {
		return "", err
	}
	intent.Generation = time.Now().UnixMilli()
	if intent.Source == "" {
		intent.Source = "companion-claw"
	}
	out, err := json.Marshal(intent)
	if err != nil {
		return "", fmt.Errorf("failed to marshal validated intent: %w", err)
	}
	return string(out), nil
}

// companionSystemPrompt instructs the model to produce an analysis followed by
// a structured action JSON. Action format matches OpsIntent in the operator.
const companionSystemPrompt = `You are an SRE assistant analyzing Kubernetes incidents for an AI agent runtime.

Given an incident (OOMKilled, CrashLoopBackOff, etc.), produce:
1. A brief diagnostic analysis (2-4 sentences).
2. A proposed remediation action as JSON, after an "ACTION:" marker.

Allowed actions: bump-memory, bump-cpu, restart-pod, rollout-restart, scale-replicas.

Action JSON format:
{"action":"<action>","params":{...},"source":"companion-claw"}

Required params:
- bump-memory: {"target": "<size>"}        e.g. "768Mi", "2Gi"
- bump-cpu:    {"target": "<cpu>"}         e.g. "500m", "2"
- scale-replicas: {"replicas": "<count>"}  e.g. "3"
- restart-pod, rollout-restart: no params required (use {})

The "generation" field will be added by the operator — do not include it.

If you cannot determine a safe action, omit the ACTION marker entirely. Never
invent actions outside the allowlist.`

// extractAction splits an LLM response into (analysis, action JSON).
// Looks for "ACTION:" marker first, then ```json fenced block as fallback.
// Returns (content, "") if no action found.
func extractAction(content string) (string, string) {
	// Try ACTION: marker first.
	if before, after, ok := strings.Cut(content, "ACTION:"); ok {
		analysis := strings.TrimSpace(before)
		raw := strings.TrimSpace(after)
		raw = stripFences(raw)
		if isValidJSONObject(raw) {
			return analysis, raw
		}
		return analysis, ""
	}

	// Fallback: look for ```json ... ``` block.
	if before, after, ok := strings.Cut(content, "```json"); ok {
		if inner, _, ok2 := strings.Cut(after, "```"); ok2 {
			raw := strings.TrimSpace(inner)
			if isValidJSONObject(raw) {
				return strings.TrimSpace(before), raw
			}
		}
	}

	return content, ""
}

// stripFences removes ```json / ``` markers from a string.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// isValidJSONObject reports whether s parses as a JSON object.
func isValidJSONObject(s string) bool {
	var m map[string]any
	return json.Unmarshal([]byte(s), &m) == nil
}
