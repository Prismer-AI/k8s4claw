package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func sampleEscalation() *v1alpha1.ClawOpsEscalation {
	return &v1alpha1.ClawOpsEscalation{
		ObjectMeta: metav1.ObjectMeta{Name: "esc-1", Namespace: "ai-agents"},
		Spec: v1alpha1.ClawOpsEscalationSpec{
			ClawRef:  corev1.LocalObjectReference{Name: "my-claw"},
			Severity: v1alpha1.SeverityHigh,
			Trigger: v1alpha1.TriggerInfo{
				Type:    v1alpha1.TriggerOOMKilled,
				Message: "Container OOMKilled",
				Count:   3,
			},
		},
		Status: v1alpha1.ClawOpsEscalationStatus{
			Phase:          v1alpha1.EscalationPhaseAwaitingApproval,
			Analysis:       "Memory leak in handler goroutine",
			ProposedAction: `{"action":"bump-memory","params":{"target":"1Gi"}}`,
		},
	}
}

func TestSlackNotifier_PostsExpectedPayload(t *testing.T) {
	t.Parallel()

	var received atomic.Pointer[slackPayload]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var p slackPayload
		require.NoError(t, json.Unmarshal(body, &p))
		received.Store(&p)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &SlackNotifier{WebhookURL: srv.URL, httpClient: srv.Client()}
	require.NoError(t, n.NotifyAwaitingApproval(context.Background(), sampleEscalation()))

	got := received.Load()
	require.NotNil(t, got, "server received no payload")
	assert.Contains(t, got.Text, "my-claw")
	assert.Contains(t, got.Text, "ai-agents")
	assert.Contains(t, got.Text, string(v1alpha1.TriggerOOMKilled))
	assert.Contains(t, got.Text, "Memory leak in handler goroutine")
	assert.Contains(t, got.Text, "kubectl claw approve esc-1")
}

func TestDiscordNotifier_PostsExpectedPayload(t *testing.T) {
	t.Parallel()

	var received atomic.Pointer[discordPayload]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var p discordPayload
		require.NoError(t, json.Unmarshal(body, &p))
		received.Store(&p)
		w.WriteHeader(http.StatusNoContent) // Discord returns 204 on success
	}))
	defer srv.Close()

	n := &DiscordNotifier{WebhookURL: srv.URL, httpClient: srv.Client()}
	require.NoError(t, n.NotifyAwaitingApproval(context.Background(), sampleEscalation()))

	got := received.Load()
	require.NotNil(t, got, "server received no payload")
	assert.Contains(t, got.Content, "my-claw")
	assert.Contains(t, got.Content, "**Claw escalation awaiting approval**", "Discord uses **bold** markdown")
	assert.Contains(t, got.Content, "kubectl claw approve esc-1")
}

func TestNotifier_NonOKStatusReturnsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	slack := &SlackNotifier{WebhookURL: srv.URL, httpClient: srv.Client()}
	err := slack.NotifyAwaitingApproval(context.Background(), sampleEscalation())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")

	discord := &DiscordNotifier{WebhookURL: srv.URL, httpClient: srv.Client()}
	err = discord.NotifyAwaitingApproval(context.Background(), sampleEscalation())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestNotifier_EmptyURLReturnsError(t *testing.T) {
	t.Parallel()
	slack := &SlackNotifier{}
	err := slack.NotifyAwaitingApproval(context.Background(), sampleEscalation())
	require.Error(t, err)
}

func TestNotifier_RespectsContextCancellation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until client cancels.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	n := &SlackNotifier{WebhookURL: srv.URL, httpClient: srv.Client()}
	err := n.NotifyAwaitingApproval(ctx, sampleEscalation())
	require.Error(t, err, "expected error when context is cancelled")
}

// stubNotifier records calls and can be configured to return an error.
type stubNotifier struct {
	calls atomic.Int64
	err   error
}

func (s *stubNotifier) NotifyAwaitingApproval(_ context.Context, _ *v1alpha1.ClawOpsEscalation) error {
	s.calls.Add(1)
	return s.err
}

func TestCompositeNotifier_FansOutAndJoinsErrors(t *testing.T) {
	t.Parallel()

	a := &stubNotifier{}
	b := &stubNotifier{err: errors.New("b failed")}
	c := &stubNotifier{err: errors.New("c failed")}

	cn := &CompositeNotifier{Notifiers: []Notifier{a, b, c}}
	err := cn.NotifyAwaitingApproval(context.Background(), sampleEscalation())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "b failed")
	assert.Contains(t, err.Error(), "c failed")

	assert.Equal(t, int64(1), a.calls.Load(), "a should be called even though b/c return errors")
	assert.Equal(t, int64(1), b.calls.Load())
	assert.Equal(t, int64(1), c.calls.Load())
}

func TestNoopNotifier(t *testing.T) {
	t.Parallel()
	n := noopNotifier{}
	require.NoError(t, n.NotifyAwaitingApproval(context.Background(), sampleEscalation()))
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "abc", truncate("abc", 5))
	assert.Equal(t, "ab…", truncate("abcd", 2))
	assert.Equal(t, "abcde", truncate("abcde", 5))
}

func TestFormatNotification_HandlesEmptyAnalysis(t *testing.T) {
	t.Parallel()
	esc := sampleEscalation()
	esc.Status.Analysis = ""
	esc.Status.ProposedAction = ""

	text := formatSlackText(esc)
	assert.Contains(t, text, "(no analysis)")
	assert.NotContains(t, text, "Proposed action:", "no proposal line when ProposedAction is empty")
	// kubectl-claw rejects approval when ProposedAction is empty, so the
	// notification must NOT instruct the on-call to run that command.
	assert.NotContains(t, text, "kubectl claw approve",
		"approve hint must be suppressed when ProposedAction is empty")
	assert.Contains(t, text, "kubectl describe clawopsescalation",
		"empty-action notification should point at manual inspection instead")
}

func TestFormatNotification_IncludesApproveWhenProposalPresent(t *testing.T) {
	t.Parallel()
	esc := sampleEscalation() // sample has a non-empty ProposedAction.

	text := formatSlackText(esc)
	assert.Contains(t, text, "kubectl claw approve esc-1 -n ai-agents",
		"approve hint must include the ns-qualified escalation name")
	assert.NotContains(t, text, "kubectl describe clawopsescalation",
		"manual-inspection fallback must not appear when a proposal exists")
}

func TestFormatNotification_TruncatesLongFields(t *testing.T) {
	t.Parallel()
	esc := sampleEscalation()
	esc.Status.Analysis = strings.Repeat("x", 1000)

	text := formatSlackText(esc)
	// Should contain the truncation marker.
	assert.True(t, strings.Contains(text, "…"), "expected ellipsis marker in truncated output")
	// Should be shorter than the raw analysis.
	assert.Less(t, len(text), 1000, "expected truncated output to fit comfortably under raw length")
}

// Sanity check that buildNotifier wiring respects env vars.
func TestBuildNotifier_PicksUpEnv(t *testing.T) {
	// Not parallel — uses os.Setenv.
	t.Setenv("CLAW4K8S_SLACK_WEBHOOK_URL", "https://hooks.slack.com/services/AAA/BBB/CCC")
	t.Setenv("CLAW4K8S_DISCORD_WEBHOOK_URL", "https://discord.com/api/webhooks/123/abc")

	logger := discardLogger()
	n := buildNotifier(logger)

	cn, ok := n.(*CompositeNotifier)
	require.True(t, ok, "expected CompositeNotifier when both env vars set, got %T", n)
	assert.Len(t, cn.Notifiers, 2)
}

func TestBuildNotifier_NoEnvReturnsNoop(t *testing.T) {
	// Not parallel — uses os.Setenv (to clear).
	t.Setenv("CLAW4K8S_SLACK_WEBHOOK_URL", "")
	t.Setenv("CLAW4K8S_DISCORD_WEBHOOK_URL", "")

	logger := discardLogger()
	n := buildNotifier(logger)

	_, ok := n.(noopNotifier)
	require.True(t, ok, "expected noopNotifier when no env vars set, got %T", n)
}

// discardLogger returns a logger that drops all output (tests don't need it).
func discardLogger() logr.Logger {
	return logr.Discard()
}
