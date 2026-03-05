package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type webhookConfig struct {
	ListenPort    int               `json:"listenPort"`
	Path          string            `json:"path"`
	TargetURL     string            `json:"targetURL"`
	Secret        string            `json:"secret"`
	Headers       map[string]string `json:"headers"`
	RetryAttempts int               `json:"retryAttempts"`
}

func parseConfig(raw string) (*webhookConfig, error) {
	cfg := &webhookConfig{
		ListenPort:    8080,
		Path:          "/webhook",
		RetryAttempts: 3,
	}
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("failed to parse CHANNEL_CONFIG: %w", err)
	}
	if cfg.ListenPort == 0 {
		cfg.ListenPort = 8080
	}
	if cfg.Path == "" {
		cfg.Path = "/webhook"
	}
	if cfg.RetryAttempts == 0 {
		cfg.RetryAttempts = 3
	}
	return cfg, nil
}

// sender abstracts the channel SDK Send method for testing.
type sender interface {
	Send(ctx context.Context, payload json.RawMessage) error
}

// inboundHandler handles incoming HTTP webhook requests.
type inboundHandler struct {
	sender sender
	secret string
}

func newInboundHandler(s sender, secret, _ string) *inboundHandler {
	return &inboundHandler{sender: s, secret: secret}
}

func (h *inboundHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	if h.secret != "" {
		sig := r.Header.Get("X-Signature-256")
		if !verifyHMAC(body, sig, h.secret) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	if err := h.sender.Send(r.Context(), json.RawMessage(body)); err != nil {
		http.Error(w, "failed to forward message", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func verifyHMAC(body []byte, signature, secret string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sigBytes, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), sigBytes)
}

// healthHandler returns 200 if connected, 503 otherwise.
type healthHandler struct {
	isConnected func() bool
}

func newHealthHandler(isConnected func() bool) *healthHandler {
	return &healthHandler{isConnected: isConnected}
}

func (h *healthHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	if h.isConnected() {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("disconnected"))
	}
}

// outboundPoster posts messages to a target URL.
type outboundPoster struct {
	cfg    *webhookConfig
	client *http.Client
}

func newOutboundPoster(cfg *webhookConfig) *outboundPoster {
	return &outboundPoster{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (p *outboundPoster) post(ctx context.Context, payload json.RawMessage) error {
	var lastErr error
	for attempt := range p.cfg.RetryAttempts {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.TargetURL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range p.cfg.Headers {
			req.Header.Set(k, v)
		}

		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("target returned status %d", resp.StatusCode)
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	return fmt.Errorf("outbound post failed after %d attempts: %w", p.cfg.RetryAttempts, lastErr)
}
