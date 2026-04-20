package rules

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultRules_UniqueIDs(t *testing.T) {
	seen := make(map[string]bool)
	for _, rule := range DefaultRules {
		assert.False(t, seen[rule.ID], "duplicate rule ID: %s", rule.ID)
		seen[rule.ID] = true
	}
}

func TestDefaultRules_AllHaveCooldowns(t *testing.T) {
	for _, rule := range DefaultRules {
		assert.Positive(t, rule.Cooldown, "rule %s should have a cooldown", rule.ID)
	}
}

func TestDefaultRules_AllHaveActions(t *testing.T) {
	for _, rule := range DefaultRules {
		assert.NotEmpty(t, rule.Action.Type, "rule %s should have an action type", rule.ID)
	}
}
