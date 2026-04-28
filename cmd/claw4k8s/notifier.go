package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// Notifier sends a human-readable summary of an escalation transition to an
// external channel (Slack, Discord, etc.).
type Notifier interface {
	// NotifyAwaitingApproval is called after an escalation reaches the
	// AwaitingApproval phase. Implementations should make the call best-effort
	// and respect ctx for cancellation/timeout.
	NotifyAwaitingApproval(ctx context.Context, esc *v1alpha1.ClawOpsEscalation) error
}

// noopNotifier silently accepts notifications. Used when no channel is configured.
type noopNotifier struct{}

func (noopNotifier) NotifyAwaitingApproval(context.Context, *v1alpha1.ClawOpsEscalation) error {
	return nil
}

// CompositeNotifier fans out to multiple notifiers. Errors are joined; a
// partial failure does not prevent other notifiers from running.
type CompositeNotifier struct {
	Notifiers []Notifier
}

// NotifyAwaitingApproval invokes every child notifier and joins any errors.
func (c *CompositeNotifier) NotifyAwaitingApproval(ctx context.Context, esc *v1alpha1.ClawOpsEscalation) error {
	var errs []error
	for _, n := range c.Notifiers {
		if err := n.NotifyAwaitingApproval(ctx, esc); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SlackNotifier posts a message to a Slack incoming webhook URL.
type SlackNotifier struct {
	WebhookURL string
	httpClient *http.Client
}

type slackPayload struct {
	Text string `json:"text"`
}

// NotifyAwaitingApproval posts a Slack-formatted summary to the configured webhook.
func (s *SlackNotifier) NotifyAwaitingApproval(ctx context.Context, esc *v1alpha1.ClawOpsEscalation) error {
	return postJSON(ctx, s.httpClient, s.WebhookURL, slackPayload{Text: formatSlackText(esc)})
}

// DiscordNotifier posts a message to a Discord webhook URL.
type DiscordNotifier struct {
	WebhookURL string
	httpClient *http.Client
}

type discordPayload struct {
	Content string `json:"content"`
}

// NotifyAwaitingApproval posts a Discord-formatted summary to the configured webhook.
func (d *DiscordNotifier) NotifyAwaitingApproval(ctx context.Context, esc *v1alpha1.ClawOpsEscalation) error {
	return postJSON(ctx, d.httpClient, d.WebhookURL, discordPayload{Content: formatDiscordText(esc)})
}

func formatSlackText(esc *v1alpha1.ClawOpsEscalation) string {
	return formatNotification(esc, ":warning:", "*Claw escalation awaiting approval*", "*", "`")
}

func formatDiscordText(esc *v1alpha1.ClawOpsEscalation) string {
	return formatNotification(esc, ":warning:", "**Claw escalation awaiting approval**", "**", "`")
}

// formatNotification builds the human-readable body for both Slack and Discord.
// The bold/code markers are passed in because Slack uses *bold* while Discord
// uses **bold** (the rest of the formatting is identical).
//
// When ProposedAction is empty (LLM fallback path) the message points the
// on-call to manual inspection instead of `kubectl claw approve`, which the
// CLI would reject with "empty proposedAction — nothing to approve".
func formatNotification(esc *v1alpha1.ClawOpsEscalation, emoji, title, _ /*bold*/, code string) string {
	analysis := esc.Status.Analysis
	if analysis == "" {
		analysis = "(no analysis)"
	}
	var proposedLine, actionLine string
	if proposed := esc.Status.ProposedAction; proposed != "" {
		proposedLine = fmt.Sprintf("Proposed action: %s%s%s\n", code, truncate(proposed, 240), code)
		actionLine = fmt.Sprintf("Approve: %skubectl claw approve %s -n %s%s",
			code, esc.Name, esc.Namespace, code)
	} else {
		actionLine = fmt.Sprintf(
			"No proposed action — inspect manually: %skubectl describe clawopsescalation %s -n %s%s",
			code, esc.Name, esc.Namespace, code)
	}
	return fmt.Sprintf(
		"%s %s\n"+
			"Claw: %s%s%s in %s%s%s\n"+
			"Trigger: %s%s%s — %s (count=%d, severity=%s)\n"+
			"Analysis: %s\n"+
			"%s"+
			"%s",
		emoji, title,
		code, esc.Spec.ClawRef.Name, code, code, esc.Namespace, code,
		code, esc.Spec.Trigger.Type, code, esc.Spec.Trigger.Message,
		esc.Spec.Trigger.Count, esc.Spec.Severity,
		truncate(analysis, 480),
		proposedLine,
		actionLine,
	)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func postJSON(ctx context.Context, c *http.Client, url string, payload any) error {
	if url == "" {
		return errors.New("webhook URL is empty")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req) //nolint:gosec // URL is the operator's own webhook config, not user input
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}
