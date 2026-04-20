package main

import (
	"context"
	"fmt"
	"time"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// LLMClient is the interface for LLM analysis.
type LLMClient interface {
	Analyze(ctx context.Context, prompt string) (analysis, action string, err error)
}

// Pipeline processes ClawOpsEscalation CRs.
type Pipeline struct {
	LLM        LLMClient
	MaxRetries int
}

// analyze runs LLM analysis on an escalation with retry and fallback.
func (p *Pipeline) analyze(ctx context.Context, esc *v1alpha1.ClawOpsEscalation) (analysis, action string, err error) {
	prompt := buildPrompt(esc)

	retries := p.MaxRetries
	if retries <= 0 {
		retries = 3
	}

	delays := []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}
	for i := 0; i < retries; i++ {
		analysis, action, err = p.LLM.Analyze(ctx, prompt)
		if err == nil {
			return analysis, action, nil
		}
		if i < len(delays) && i < retries-1 {
			select {
			case <-time.After(delays[i]):
			case <-ctx.Done():
				return "", "", ctx.Err()
			}
		}
	}

	// Fallback: return raw context for human review.
	fallbackAnalysis := fmt.Sprintf(
		"LLM analysis unavailable after %d retries. Trigger: %s (%s). Count: %d. Severity: %s.",
		retries, esc.Spec.Trigger.Type, esc.Spec.Trigger.Message,
		esc.Spec.Trigger.Count, esc.Spec.Severity,
	)
	return fallbackAnalysis, "", nil
}

func buildPrompt(esc *v1alpha1.ClawOpsEscalation) string {
	return fmt.Sprintf(
		"Analyze this Kubernetes issue and propose a remediation.\n"+
			"Trigger: %s\nMessage: %s\nCount: %d\nSeverity: %s\n",
		esc.Spec.Trigger.Type, esc.Spec.Trigger.Message,
		esc.Spec.Trigger.Count, esc.Spec.Severity,
	)
}
