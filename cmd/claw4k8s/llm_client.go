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
)

// HermesGatewayClient calls an OpenAI-compatible /v1/chat/completions endpoint.
// Works with hermes-agent-rs hermes-gateway, Anthropic API (via gateway model
// prefix), or any OpenAI-compatible provider.
type HermesGatewayClient struct {
	BaseURL string        // e.g. "http://hermes-gateway.default.svc.cluster.local:8080"
	Model   string        // e.g. "anthropic/claude-sonnet-4-20250514"
	APIKey  string        // optional; sent as Bearer token if non-empty
	Timeout time.Duration // per-request timeout (default 60s)

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
// "ACTION:" marker or inside a ```json fenced block.
func (c *HermesGatewayClient) Analyze(ctx context.Context, prompt string) (string, string, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: timeout}
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
		return "", "", fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("call gateway: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("gateway returned %d: %s", resp.StatusCode, truncate(string(respBytes), 256))
	}

	var parsed chatCompletionResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", "", fmt.Errorf("decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", "", fmt.Errorf("gateway returned no choices")
	}

	content := parsed.Choices[0].Message.Content
	analysis, action := extractAction(content)
	return analysis, action, nil
}

// companionSystemPrompt instructs the model to produce an analysis followed by
// a structured action JSON. Action format matches OpsIntent in the operator.
const companionSystemPrompt = `You are an SRE assistant analyzing Kubernetes incidents for an AI agent runtime.

Given an incident (OOMKilled, CrashLoopBackOff, etc.), produce:
1. A brief diagnostic analysis (2-4 sentences).
2. A proposed remediation action as JSON, after an "ACTION:" marker.

Allowed actions: bump-memory, bump-cpu, restart-pod, rollout-restart, scale-replicas.

Action JSON format:
{"action":"<action>","params":{...},"generation":<unix-millis>,"source":"companion-claw"}

If you cannot determine a safe action, omit the ACTION marker entirely. Never invent
actions outside the allowlist.`

// extractAction splits an LLM response into (analysis, action JSON).
// Looks for "ACTION:" marker first, then ```json fenced block as fallback.
// Returns ("", "") if no action found, with full content as analysis.
func extractAction(content string) (string, string) {
	// Try ACTION: marker first.
	if idx := strings.Index(content, "ACTION:"); idx >= 0 {
		analysis := strings.TrimSpace(content[:idx])
		raw := strings.TrimSpace(content[idx+len("ACTION:"):])
		raw = stripFences(raw)
		if isValidJSONObject(raw) {
			return analysis, raw
		}
		return analysis, ""
	}

	// Fallback: look for ```json ... ``` block.
	if start := strings.Index(content, "```json"); start >= 0 {
		rest := content[start+len("```json"):]
		if end := strings.Index(rest, "```"); end >= 0 {
			raw := strings.TrimSpace(rest[:end])
			if isValidJSONObject(raw) {
				analysis := strings.TrimSpace(content[:start])
				return analysis, raw
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

// truncate clips a string to maxLen characters, adding an ellipsis.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
