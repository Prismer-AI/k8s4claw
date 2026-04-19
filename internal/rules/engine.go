package rules

import (
	"sync"
	"time"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// Signal represents an anomaly detected in a Claw's environment.
type Signal struct {
	Type     v1alpha1.TriggerType
	Severity v1alpha1.Severity
	Count    int32
	Message  string
	Source   string            // "pod-status" or "prometheus"
	Raw      map[string]string // original data for LLM context
}

// ActionType identifies what kind of remediation to perform.
type ActionType string

const (
	ActionPatchResource ActionType = "PatchResource"
	ActionRestartPod    ActionType = "RestartPod"
	ActionScaleReplicas ActionType = "ScaleReplicas"
)

// ActionSpec describes a remediation action.
type ActionSpec struct {
	Type   ActionType
	Params map[string]string
}

// MatchCriteria defines when a rule should fire.
type MatchCriteria struct {
	SignalType  v1alpha1.TriggerType
	MinSeverity v1alpha1.Severity
	MinCount    int32
}

// Rule defines a single auto-remediation rule.
type Rule struct {
	ID       string
	Match    MatchCriteria
	Action   ActionSpec
	Cooldown time.Duration
}

// Engine evaluates signals against rules with cooldown tracking.
type Engine struct {
	rules []Rule
	mu    sync.RWMutex
	// cooldowns tracks last execution time per claw per rule.
	cooldowns map[string]map[string]time.Time // clawName -> ruleID -> lastExec
}

// NewEngine creates a rule engine with the given rules.
func NewEngine(rules []Rule) *Engine {
	return &Engine{
		rules:     rules,
		cooldowns: make(map[string]map[string]time.Time),
	}
}

// SetRules replaces the rule set (for hot-reload from ConfigMap).
func (e *Engine) SetRules(rules []Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = rules
}

// Match returns whether any rule matches the signal for the given claw.
func (e *Engine) Match(clawName string, sig Signal) (bool, Rule) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, rule := range e.rules {
		if e.ruleMatches(clawName, rule, sig) {
			return true, rule
		}
	}
	return false, Rule{}
}

// MatchHighestPriority returns the highest-severity matching signal and its rule.
func (e *Engine) MatchHighestPriority(clawName string, signals []Signal) (Signal, Rule, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var bestSig Signal
	var bestRule Rule
	bestRank := -1

	for _, sig := range signals {
		for _, rule := range e.rules {
			if e.ruleMatches(clawName, rule, sig) {
				rank := v1alpha1.SeverityRank(sig.Severity)
				if rank > bestRank {
					bestRank = rank
					bestSig = sig
					bestRule = rule
				}
				break // first matching rule per signal
			}
		}
	}
	return bestSig, bestRule, bestRank >= 0
}

// RecordExecution records that a rule was executed for a claw (starts cooldown).
func (e *Engine) RecordExecution(clawName, ruleID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.cooldowns[clawName] == nil {
		e.cooldowns[clawName] = make(map[string]time.Time)
	}
	e.cooldowns[clawName][ruleID] = time.Now()
}

func (e *Engine) ruleMatches(clawName string, rule Rule, sig Signal) bool {
	if rule.Match.SignalType != sig.Type {
		return false
	}
	if v1alpha1.SeverityRank(sig.Severity) < v1alpha1.SeverityRank(rule.Match.MinSeverity) {
		return false
	}
	if sig.Count < rule.Match.MinCount {
		return false
	}
	if rule.Cooldown > 0 {
		if clawCooldowns, ok := e.cooldowns[clawName]; ok {
			if lastExec, ok := clawCooldowns[rule.ID]; ok {
				if time.Since(lastExec) < rule.Cooldown {
					return false
				}
			}
		}
	}
	return true
}
