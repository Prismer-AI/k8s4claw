package signet

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// SignRequest contains the parameters for signing an action.
type SignRequest struct {
	Key    string
	Tool   string
	Params map[string]string
	Target string
}

// Receipt is the signed audit record produced by a Signer.
type Receipt struct {
	Version   int               `json:"v"`
	ID        string            `json:"id"`
	KeyID     string            `json:"key"`
	Tool      string            `json:"tool"`
	Params    map[string]string `json:"params,omitempty"`
	Target    string            `json:"target"`
	Timestamp string            `json:"ts"`
	Signature string            `json:"sig"`
}

// Signer signs ops actions and produces receipt JSON.
type Signer interface {
	Sign(req SignRequest) (receiptJSON string, err error)
}

// ---------------------------------------------------------------------------
// Ed25519Signer — pure Go implementation
// ---------------------------------------------------------------------------

// Ed25519Signer signs actions using an in-memory Ed25519 key pair.
type Ed25519Signer struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
}

// NewEd25519Signer generates a new Ed25519 key pair and returns a signer.
func NewEd25519Signer() (*Ed25519Signer, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate Ed25519 key: %w", err)
	}
	return &Ed25519Signer{privateKey: priv, publicKey: pub}, nil
}

// Sign creates a receipt with an Ed25519 signature over the canonical payload.
func (s *Ed25519Signer) Sign(req SignRequest) (string, error) {
	id, err := randomID()
	if err != nil {
		return "", fmt.Errorf("failed to generate receipt ID: %w", err)
	}

	receipt := Receipt{
		Version:   1,
		ID:        id,
		KeyID:     req.Key,
		Tool:      req.Tool,
		Params:    req.Params,
		Target:    req.Target,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	payload := canonicalPayload(&receipt)
	sig := ed25519.Sign(s.privateKey, payload)
	receipt.Signature = hex.EncodeToString(sig)

	out, err := json.Marshal(receipt)
	if err != nil {
		return "", fmt.Errorf("failed to marshal receipt: %w", err)
	}
	return string(out), nil
}

// Verify checks the Ed25519 signature on a receipt JSON string.
func (s *Ed25519Signer) Verify(receiptJSON string) (bool, error) {
	var receipt Receipt
	if err := json.Unmarshal([]byte(receiptJSON), &receipt); err != nil {
		return false, fmt.Errorf("failed to parse receipt: %w", err)
	}

	sig, err := hex.DecodeString(receipt.Signature)
	if err != nil {
		return false, fmt.Errorf("failed to decode signature: %w", err)
	}

	payload := canonicalPayload(&receipt)
	return ed25519.Verify(s.publicKey, payload, sig), nil
}

// PublicKeyHex returns the hex-encoded public key for audit/verification.
func (s *Ed25519Signer) PublicKeyHex() string {
	return hex.EncodeToString(s.publicKey)
}

// canonicalPayload builds the deterministic byte string to sign.
// Format: "v=1|id=<id>|key=<key>|tool=<tool>|target=<target>|ts=<ts>|params=<sorted-json>"
func canonicalPayload(r *Receipt) []byte {
	paramsJSON, _ := json.Marshal(r.Params)
	return []byte(fmt.Sprintf("v=%d|id=%s|key=%s|tool=%s|target=%s|ts=%s|params=%s",
		r.Version, r.ID, r.KeyID, r.Tool, r.Target, r.Timestamp, string(paramsJSON)))
}

// randomID generates a hex-encoded random ID (16 bytes → 32 hex chars).
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "rec_" + hex.EncodeToString(b), nil
}

// ---------------------------------------------------------------------------
// CLISigner — calls external signet binary
// ---------------------------------------------------------------------------

// CLISigner signs actions by calling the signet CLI binary.
type CLISigner struct {
	BinaryPath string
}

// NewCLISigner creates a Signer that calls the signet binary.
func NewCLISigner(binaryPath string) *CLISigner {
	return &CLISigner{BinaryPath: binaryPath}
}

// Sign calls `signet sign` and returns the receipt JSON.
func (s *CLISigner) Sign(req SignRequest) (string, error) {
	paramsJSON, err := json.Marshal(req.Params)
	if err != nil {
		return "", fmt.Errorf("failed to marshal params: %w", err)
	}
	args := []string{
		"sign",
		"--key", req.Key,
		"--tool", req.Tool,
		"--params", string(paramsJSON),
		"--target", req.Target,
	}
	cmd := exec.Command(s.BinaryPath, args...) //nolint:gosec // G204: binary path is operator-configured, not user input
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("signet sign failed: %w", err)
	}
	return string(output), nil
}

// ---------------------------------------------------------------------------
// NewSigner — factory with graceful fallback
// ---------------------------------------------------------------------------

// NewSigner creates the best available Signer:
//   - If binaryPath points to an executable signet binary, uses CLISigner
//   - Otherwise, falls back to Ed25519Signer (pure Go, in-memory key)
func NewSigner(binaryPath string) Signer {
	if binaryPath != "" {
		if _, err := exec.LookPath(binaryPath); err == nil {
			return NewCLISigner(binaryPath)
		}
	}
	signer, err := NewEd25519Signer()
	if err != nil {
		// Ed25519 key generation should never fail; if it does, return mock.
		return NewMockSigner()
	}
	return signer
}

// ---------------------------------------------------------------------------
// MockSigner — test double
// ---------------------------------------------------------------------------

// MockSigner is a test double that records sign requests.
type MockSigner struct {
	mu      sync.Mutex
	history []SignRequest
	err     error
}

// NewMockSigner creates a MockSigner.
func NewMockSigner() *MockSigner {
	return &MockSigner{}
}

// SetError causes all subsequent Sign calls to return this error.
func (m *MockSigner) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// Sign records the request and returns a mock receipt.
func (m *MockSigner) Sign(req SignRequest) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return "", m.err
	}
	m.history = append(m.history, req)
	return `{"v":1,"id":"rec_mock","sig":"mock"}`, nil
}

// History returns all recorded sign requests.
func (m *MockSigner) History() []SignRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]SignRequest, len(m.history))
	copy(result, m.history)
	return result
}
