package signet

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// ---------------------------------------------------------------------------
// Ed25519Signer
// ---------------------------------------------------------------------------

func TestEd25519Signer_SignAndVerify(t *testing.T) {
	t.Parallel()
	signer, err := NewEd25519Signer()
	require.NoError(t, err)

	req := SignRequest{
		Key:    "rule-engine",
		Tool:   "bump-memory",
		Params: map[string]string{"target": "1Gi"},
		Target: "claw://default/my-claw",
	}

	receiptJSON, err := signer.Sign(req)
	require.NoError(t, err)
	require.NotEmpty(t, receiptJSON)

	// Parse receipt.
	var receipt Receipt
	require.NoError(t, json.Unmarshal([]byte(receiptJSON), &receipt))

	assert.Equal(t, 1, receipt.Version)
	assert.Equal(t, "rule-engine", receipt.KeyID)
	assert.Equal(t, "bump-memory", receipt.Tool)
	assert.Equal(t, "claw://default/my-claw", receipt.Target)
	assert.NotEmpty(t, receipt.Signature)
	assert.NotEmpty(t, receipt.Timestamp)
	assert.NotEmpty(t, receipt.ID)

	// Verify signature.
	ok, err := signer.Verify(receiptJSON)
	require.NoError(t, err)
	assert.True(t, ok, "signature should verify")
}

func TestEd25519Signer_VerifyTampered(t *testing.T) {
	t.Parallel()
	signer, err := NewEd25519Signer()
	require.NoError(t, err)

	req := SignRequest{
		Key:    "rule-engine",
		Tool:   "restart-pod",
		Params: map[string]string{},
		Target: "claw://ns/claw-1",
	}

	receiptJSON, err := signer.Sign(req)
	require.NoError(t, err)

	// Tamper with the receipt.
	var receipt Receipt
	require.NoError(t, json.Unmarshal([]byte(receiptJSON), &receipt))
	receipt.Tool = "delete-namespace"
	tampered, _ := json.Marshal(receipt)

	ok, err := signer.Verify(string(tampered))
	require.NoError(t, err)
	assert.False(t, ok, "tampered receipt should fail verification")
}

func TestEd25519Signer_DeterministicPayload(t *testing.T) {
	t.Parallel()
	signer, err := NewEd25519Signer()
	require.NoError(t, err)

	req := SignRequest{
		Key:    "test-key",
		Tool:   "test-tool",
		Params: map[string]string{"a": "1", "b": "2"},
		Target: "test-target",
	}

	r1, err := signer.Sign(req)
	require.NoError(t, err)

	r2, err := signer.Sign(req)
	require.NoError(t, err)

	// Different IDs and timestamps but same key/tool/target.
	var rec1, rec2 Receipt
	require.NoError(t, json.Unmarshal([]byte(r1), &rec1))
	require.NoError(t, json.Unmarshal([]byte(r2), &rec2))

	assert.NotEqual(t, rec1.ID, rec2.ID, "IDs should be unique")
	assert.Equal(t, rec1.KeyID, rec2.KeyID)
	assert.Equal(t, rec1.Tool, rec2.Tool)
}

func TestEd25519Signer_PublicKeyHex(t *testing.T) {
	t.Parallel()
	signer, err := NewEd25519Signer()
	require.NoError(t, err)

	hex := signer.PublicKeyHex()
	assert.Len(t, hex, 64, "Ed25519 public key hex should be 64 chars")
}

// ---------------------------------------------------------------------------
// CLISigner — binary not found fallback
// ---------------------------------------------------------------------------

func TestCLISigner_BinaryNotFound(t *testing.T) {
	t.Parallel()
	signer := NewCLISigner("/nonexistent/signet-binary")
	_, err := signer.Sign(SignRequest{Key: "k", Tool: "t", Target: "x"})
	assert.Error(t, err, "should error when binary not found")
}

// ---------------------------------------------------------------------------
// NewSigner factory
// ---------------------------------------------------------------------------

func TestNewSigner_FallsBackToEd25519(t *testing.T) {
	t.Parallel()
	// With a nonexistent binary path, should fall back to Ed25519Signer.
	signer := NewSigner("/nonexistent/path")
	receipt, err := signer.Sign(SignRequest{
		Key:    "test",
		Tool:   "test-tool",
		Params: map[string]string{},
		Target: "test-target",
	})
	require.NoError(t, err)
	assert.Contains(t, receipt, `"v":1`)
	assert.Contains(t, receipt, `"sig"`)
}
