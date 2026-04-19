package signet

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
)

// SignRequest contains the parameters for signing an action.
type SignRequest struct {
	Key    string
	Tool   string
	Params map[string]string
	Target string
}

// Signer signs ops actions and produces receipt JSON.
type Signer interface {
	Sign(req SignRequest) (receiptJSON string, err error)
}

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
	cmd := exec.Command(s.BinaryPath, "sign",
		"--key", req.Key,
		"--tool", req.Tool,
		"--params", string(paramsJSON),
		"--target", req.Target,
	)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("signet sign failed: %w", err)
	}
	return string(output), nil
}

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
