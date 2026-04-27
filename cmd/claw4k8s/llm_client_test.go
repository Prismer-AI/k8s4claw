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

	client := &HermesGatewayClient{
		BaseURL: srv.URL,
		Model:   "anthropic/claude-sonnet-4",
		APIKey:  "sk-test",
		Timeout: 5 * time.Second,
	}

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

	client := &HermesGatewayClient{BaseURL: srv.URL, Model: "test", APIKey: "x", Timeout: 5 * time.Second}
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

	client := &HermesGatewayClient{BaseURL: srv.URL, Model: "test", Timeout: 5 * time.Second}
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

	client := &HermesGatewayClient{BaseURL: srv.URL, Model: "test", APIKey: "x", Timeout: 5 * time.Second}
	_, _, err := client.Analyze(context.Background(), "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestHermesGatewayClient_Analyze_MalformedJSON(t *testing.T) {
	t.Parallel()

	handler := &fakeChatHandler{failBody: true}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := &HermesGatewayClient{BaseURL: srv.URL, Model: "test", APIKey: "x", Timeout: 5 * time.Second}
	_, _, err := client.Analyze(context.Background(), "prompt")
	require.Error(t, err)
}

func TestHermesGatewayClient_Analyze_EmptyChoices(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	client := &HermesGatewayClient{BaseURL: srv.URL, Model: "test", APIKey: "x", Timeout: 5 * time.Second}
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

	client := &HermesGatewayClient{BaseURL: srv.URL, Model: "test", APIKey: "x", Timeout: 30 * time.Second}
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
