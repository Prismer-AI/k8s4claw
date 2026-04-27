package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeChatHandler returns a canned OpenAI-compatible response.
type fakeChatHandler struct {
	statusCode  int
	respContent string
	failBody    bool
	gotPrompt   string
	gotModel    string
	gotAuth     string
}

// newTestClient returns a HermesGatewayClient with an eagerly-initialized
// httpClient — mirrors how buildLLMClient constructs it in production.
func newTestClient(baseURL, model, apiKey string, timeout time.Duration) *HermesGatewayClient {
	return &HermesGatewayClient{
		BaseURL:    baseURL,
		Model:      model,
		APIKey:     apiKey,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (h *fakeChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.gotAuth = r.Header.Get("Authorization")

	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}

	var req chatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.gotModel = req.Model
	if len(req.Messages) > 0 {
		h.gotPrompt = req.Messages[len(req.Messages)-1].Content
	}

	w.Header().Set("Content-Type", "application/json")
	if h.statusCode != 0 {
		w.WriteHeader(h.statusCode)
	}
	if h.failBody {
		_, _ = w.Write([]byte("not json"))
		return
	}
	resp := chatCompletionResponse{
		Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: h.respContent}}},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// ---------------------------------------------------------------------------
// Analyze — happy path
// ---------------------------------------------------------------------------

func TestHermesGatewayClient_Analyze_ParsesAnalysisAndAction(t *testing.T) {
	t.Parallel()

	handler := &fakeChatHandler{
		respContent: `Pod OOMKilled — memory limit too low for the workload.

ACTION: {"action":"bump-memory","params":{"target":"768Mi"},"generation":1,"source":"companion-claw"}`,
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := newTestClient(srv.URL, "anthropic/claude-sonnet-4", "sk-test", 5*time.Second)

	analysis, action, err := client.Analyze(context.Background(), "trigger: OOMKilled")
	require.NoError(t, err)
	assert.Contains(t, analysis, "OOMKilled")
	assert.Contains(t, action, "bump-memory")
	assert.Contains(t, action, "768Mi")

	// Verify request shape.
	assert.Equal(t, "anthropic/claude-sonnet-4", handler.gotModel)
	assert.Equal(t, "Bearer sk-test", handler.gotAuth)
	assert.Contains(t, handler.gotPrompt, "OOMKilled")
}

func TestHermesGatewayClient_Analyze_NoActionInResponse(t *testing.T) {
	t.Parallel()

	handler := &fakeChatHandler{
		respContent: "I cannot determine a safe remediation for this issue. Manual review recommended.",
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := newTestClient(srv.URL, "test", "x", 5*time.Second)
	analysis, action, err := client.Analyze(context.Background(), "prompt")
	require.NoError(t, err)
	assert.Contains(t, analysis, "Manual review")
	assert.Empty(t, action, "no ACTION marker → empty action")
}

func TestHermesGatewayClient_Analyze_NoAPIKey(t *testing.T) {
	t.Parallel()
	// Some local hermes-agent-rs deployments may not require an API key.
	handler := &fakeChatHandler{
		respContent: "ok",
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := newTestClient(srv.URL, "test", "", 5*time.Second)
	_, _, err := client.Analyze(context.Background(), "prompt")
	require.NoError(t, err)
	assert.Empty(t, handler.gotAuth, "no API key → no Authorization header")
}

// ---------------------------------------------------------------------------
// Analyze — error paths
// ---------------------------------------------------------------------------

func TestHermesGatewayClient_Analyze_HTTPError(t *testing.T) {
	t.Parallel()

	handler := &fakeChatHandler{statusCode: http.StatusInternalServerError, respContent: "boom"}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := newTestClient(srv.URL, "test", "x", 5*time.Second)
	_, _, err := client.Analyze(context.Background(), "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestHermesGatewayClient_Analyze_MalformedJSON(t *testing.T) {
	t.Parallel()

	handler := &fakeChatHandler{failBody: true}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := newTestClient(srv.URL, "test", "x", 5*time.Second)
	_, _, err := client.Analyze(context.Background(), "prompt")
	require.Error(t, err)
}

func TestHermesGatewayClient_Analyze_EmptyChoices(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "test", "x", 5*time.Second)
	_, _, err := client.Analyze(context.Background(), "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no choices")
}

func TestHermesGatewayClient_Analyze_ContextCancel(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hang until client cancels
	}))
	defer srv.Close()

	client := newTestClient(srv.URL, "test", "x", 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before sending

	_, _, err := client.Analyze(ctx, "prompt")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// extractAction — parsing helpers
// ---------------------------------------------------------------------------

func TestExtractAction_StandardMarker(t *testing.T) {
	t.Parallel()
	content := `analysis text here

ACTION: {"action":"bump-memory","params":{"target":"512Mi"},"generation":1,"source":"companion-claw"}`
	analysis, action := extractAction(content)
	assert.Contains(t, analysis, "analysis text")
	assert.NotContains(t, analysis, "ACTION:")
	assert.Contains(t, action, "bump-memory")
}

func TestExtractAction_FencedJSON(t *testing.T) {
	t.Parallel()
	content := "diagnosis here\n\n```json\n{\"action\":\"restart-pod\",\"params\":{},\"generation\":2,\"source\":\"companion-claw\"}\n```\n"
	analysis, action := extractAction(content)
	assert.Contains(t, analysis, "diagnosis")
	assert.Contains(t, action, "restart-pod")
	assert.False(t, strings.Contains(action, "```"), "fences should be stripped")
}

func TestExtractAction_NoAction(t *testing.T) {
	t.Parallel()
	content := "Just a description, no action proposed."
	analysis, action := extractAction(content)
	assert.Equal(t, content, analysis)
	assert.Empty(t, action)
}

func TestExtractAction_InvalidJSON(t *testing.T) {
	t.Parallel()
	// Marker present but the payload isn't valid JSON — return raw analysis, no action.
	content := "ACTION: not a json object at all"
	_, action := extractAction(content)
	assert.Empty(t, action, "non-JSON action must be discarded")
}

// ---------------------------------------------------------------------------
// validateAndStampAction — schema validation + server-stamped generation
// ---------------------------------------------------------------------------

func TestAnalyze_ValidatesActionAgainstAllowlist(t *testing.T) {
	t.Parallel()
	// LLM proposes an unknown action — must be discarded, escalation falls
	// through to AwaitingApproval.
	handler := &fakeChatHandler{
		respContent: `analysis here

ACTION: {"action":"delete-namespace","params":{},"source":"companion-claw"}`,
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := newTestClient(srv.URL, "test", "x", 5*time.Second)
	analysis, action, err := client.Analyze(context.Background(), "prompt")
	require.NoError(t, err)
	assert.Contains(t, analysis, "analysis here")
	assert.Empty(t, action, "unknown action must be discarded")
}

func TestAnalyze_StampsServerGeneration(t *testing.T) {
	t.Parallel()
	// LLM provides generation=1 (low/hallucinated). Server overwrites with
	// time.Now().UnixMilli() so replay protection works correctly.
	handler := &fakeChatHandler{
		respContent: `analysis

ACTION: {"action":"bump-memory","params":{"target":"768Mi"},"generation":1,"source":"companion-claw"}`,
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	before := time.Now().UnixMilli()
	client := newTestClient(srv.URL, "test", "x", 5*time.Second)
	_, action, err := client.Analyze(context.Background(), "prompt")
	after := time.Now().UnixMilli()
	require.NoError(t, err)
	require.NotEmpty(t, action)

	// Re-parse and check generation is server-stamped (within [before, after]).
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(action), &parsed))
	gen, ok := parsed["generation"].(float64)
	require.True(t, ok, "generation must be present")
	assert.GreaterOrEqual(t, int64(gen), before, "generation should be >= before-call timestamp")
	assert.LessOrEqual(t, int64(gen), after, "generation should be <= after-call timestamp")
	assert.NotEqual(t, int64(1), int64(gen), "LLM-provided generation=1 must be overwritten")
}

func TestAnalyze_NilHTTPClient(t *testing.T) {
	t.Parallel()
	// Direct construction without buildLLMClient — should fail with clear error
	// rather than racing on lazy init.
	client := &HermesGatewayClient{BaseURL: "http://x", Model: "y"}
	_, _, err := client.Analyze(context.Background(), "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "httpClient is nil")
}
