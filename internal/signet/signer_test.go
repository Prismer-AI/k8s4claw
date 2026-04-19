package signet

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMockSigner_Sign(t *testing.T) {
	signer := NewMockSigner()
	receipt, err := signer.Sign(SignRequest{
		Key:    "rule-engine",
		Tool:   "PatchResource",
		Params: map[string]string{"field": "memory-limit"},
		Target: "claw://default/my-claw",
	})
	assert.NoError(t, err)
	assert.Contains(t, receipt, `"v":1`)
	assert.Contains(t, receipt, `"id":"rec_mock"`)
}

func TestMockSigner_RecordsHistory(t *testing.T) {
	signer := NewMockSigner()
	_, _ = signer.Sign(SignRequest{Key: "k1", Tool: "t1"})
	_, _ = signer.Sign(SignRequest{Key: "k2", Tool: "t2"})
	assert.Len(t, signer.History(), 2)
	assert.Equal(t, "k1", signer.History()[0].Key)
	assert.Equal(t, "t2", signer.History()[1].Tool)
}

func TestMockSigner_FailMode(t *testing.T) {
	signer := NewMockSigner()
	signer.SetError(assert.AnError)
	receipt, err := signer.Sign(SignRequest{})
	assert.Error(t, err)
	assert.Empty(t, receipt)
}
