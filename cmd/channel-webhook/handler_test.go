package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// mockSender captures payloads sent via the channel SDK.
type mockSender struct {
	mu   sync.Mutex
	sent []json.RawMessage
}

func (m *mockSender) Send(_ context.Context, payload json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, payload)
	return nil
}

func (m *mockSender) getSent() []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]json.RawMessage(nil), m.sent...)
}

func TestInboundHandler_Success(t *testing.T) {
	sender := &mockSender{}
	h := newInboundHandler(sender, "", "/webhook")

	body := `{"event":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}

	sent := sender.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	if string(sent[0]) != body {
		t.Errorf("payload = %s, want %s", sent[0], body)
	}
}

func TestInboundHandler_HMACVerification(t *testing.T) {
	secret := "test-secret"
	sender := &mockSender{}
	h := newInboundHandler(sender, secret, "/webhook")

	body := `{"event":"signed"}`
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature-256", sig)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
}

func TestInboundHandler_HMACRejection(t *testing.T) {
	secret := "test-secret"
	sender := &mockSender{}
	h := newInboundHandler(sender, secret, "/webhook")

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(`{"bad":"sig"}`))
	req.Header.Set("X-Signature-256", "sha256=invalid")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestInboundHandler_WrongMethod(t *testing.T) {
	sender := &mockSender{}
	h := newInboundHandler(sender, "", "/webhook")

	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHealthHandler_Connected(t *testing.T) {
	h := newHealthHandler(func() bool { return true })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHealthHandler_Disconnected(t *testing.T) {
	h := newHealthHandler(func() bool { return false })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestOutboundPoster(t *testing.T) {
	var received []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing Authorization header")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := &webhookConfig{
		TargetURL:     ts.URL,
		Headers:       map[string]string{"Authorization": "Bearer token"},
		RetryAttempts: 3,
	}
	poster := newOutboundPoster(cfg)

	err := poster.post(context.Background(), json.RawMessage(`{"msg":"hello"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	if string(received) != `{"msg":"hello"}` {
		t.Errorf("received = %s, want %s", received, `{"msg":"hello"}`)
	}
}

func TestParseConfig(t *testing.T) {
	raw := `{"listenPort":9090,"path":"/hook","targetURL":"https://example.com","secret":"s","headers":{"X-Key":"val"},"retryAttempts":5}`

	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ListenPort != 9090 {
		t.Errorf("ListenPort = %d, want 9090", cfg.ListenPort)
	}
	if cfg.Path != "/hook" {
		t.Errorf("Path = %q, want /hook", cfg.Path)
	}
	if cfg.TargetURL != "https://example.com" {
		t.Errorf("TargetURL = %q", cfg.TargetURL)
	}
	if cfg.Secret != "s" {
		t.Errorf("Secret = %q", cfg.Secret)
	}
	if cfg.Headers["X-Key"] != "val" {
		t.Errorf("Headers[X-Key] = %q", cfg.Headers["X-Key"])
	}
	if cfg.RetryAttempts != 5 {
		t.Errorf("RetryAttempts = %d, want 5", cfg.RetryAttempts)
	}
}

func TestParseConfig_Defaults(t *testing.T) {
	cfg, err := parseConfig("")
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ListenPort != 8080 {
		t.Errorf("ListenPort = %d, want 8080", cfg.ListenPort)
	}
	if cfg.Path != "/webhook" {
		t.Errorf("Path = %q, want /webhook", cfg.Path)
	}
	if cfg.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want 3", cfg.RetryAttempts)
	}
}
