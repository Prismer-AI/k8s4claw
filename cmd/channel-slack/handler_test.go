package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-logr/logr"

	channel "github.com/Prismer-AI/k8s4claw/sdk/channel"
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

// mockSocketModeConn is a mock Socket Mode connection for testing.
type mockSocketModeConn struct {
	mu         sync.Mutex
	events     []mockEvent
	acked      []string
	connected  bool
	connectErr error
	readIdx    int
}

type mockEvent struct {
	payload    json.RawMessage
	envelopeID string
	err        error
}

func newMockSocketModeConn(events []mockEvent) *mockSocketModeConn {
	return &mockSocketModeConn{
		events:    events,
		connected: true,
	}
}

func (m *mockSocketModeConn) Connect(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connectErr != nil {
		return m.connectErr
	}
	m.connected = true
	return nil
}

func (m *mockSocketModeConn) ReadEvent(ctx context.Context) (json.RawMessage, string, error) {
	m.mu.Lock()
	idx := m.readIdx
	if idx >= len(m.events) {
		m.mu.Unlock()
		// Block until context is cancelled to simulate waiting.
		<-ctx.Done()
		return nil, "", ctx.Err()
	}
	ev := m.events[idx]
	m.readIdx++
	m.mu.Unlock()
	return ev.payload, ev.envelopeID, ev.err
}

func (m *mockSocketModeConn) Acknowledge(_ context.Context, envelopeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acked = append(m.acked, envelopeID)
	return nil
}

func (m *mockSocketModeConn) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

func (m *mockSocketModeConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = false
	return nil
}

func (m *mockSocketModeConn) getAcked() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.acked...)
}

func TestParseConfig(t *testing.T) {
	raw := `{"appLevelToken":"xapp-test","botToken":"xoxb-test","defaultChannel":"C123","listenPort":9090,"retryAttempts":5}`

	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.AppLevelToken != "xapp-test" {
		t.Errorf("AppLevelToken = %q, want xapp-test", cfg.AppLevelToken)
	}
	if cfg.BotToken != "xoxb-test" {
		t.Errorf("BotToken = %q, want xoxb-test", cfg.BotToken)
	}
	if cfg.DefaultChannel != "C123" {
		t.Errorf("DefaultChannel = %q, want C123", cfg.DefaultChannel)
	}
	if cfg.ListenPort != 9090 {
		t.Errorf("ListenPort = %d, want 9090", cfg.ListenPort)
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
	if cfg.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want 3", cfg.RetryAttempts)
	}
	if cfg.SlackAPIURL != "https://slack.com/api" {
		t.Errorf("SlackAPIURL = %q, want https://slack.com/api", cfg.SlackAPIURL)
	}
}

func TestParseConfig_EnvOverride(t *testing.T) {
	t.Setenv("SLACK_APP_TOKEN", "xapp-env")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-env")

	raw := `{"appLevelToken":"xapp-json","botToken":"xoxb-json"}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	// Env vars should take precedence.
	if cfg.AppLevelToken != "xapp-env" {
		t.Errorf("AppLevelToken = %q, want xapp-env (env override)", cfg.AppLevelToken)
	}
	if cfg.BotToken != "xoxb-env" {
		t.Errorf("BotToken = %q, want xoxb-env (env override)", cfg.BotToken)
	}
}

func TestOutboundPost_Success(t *testing.T) {
	var received []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer xoxb-test" {
			t.Error("missing or wrong Authorization header")
		}
		if r.Header.Get("Content-Type") != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  3,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`{"text":"Hello from the agent!"}`)
	err := poster.post(context.Background(), payload)
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	// Verify the request body sent to Slack API.
	var apiReq map[string]any
	if err := json.Unmarshal(received, &apiReq); err != nil {
		t.Fatalf("unmarshal received: %v", err)
	}
	if apiReq["channel"] != "C999" {
		t.Errorf("channel = %v, want C999", apiReq["channel"])
	}
	if apiReq["text"] != "Hello from the agent!" {
		t.Errorf("text = %v", apiReq["text"])
	}
}

func TestOutboundPost_WithExplicitChannel(t *testing.T) {
	var received []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  3,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`{"channel":"C1234567890","text":"specific channel"}`)
	err := poster.post(context.Background(), payload)
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	var apiReq map[string]any
	if err := json.Unmarshal(received, &apiReq); err != nil {
		t.Fatalf("unmarshal received: %v", err)
	}
	if apiReq["channel"] != "C1234567890" {
		t.Errorf("channel = %v, want C1234567890", apiReq["channel"])
	}
}

func TestOutboundPost_WithBlocks(t *testing.T) {
	var received []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  3,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`{"text":"fallback","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"*Bold*"}}]}`)
	err := poster.post(context.Background(), payload)
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	var apiReq map[string]any
	if err := json.Unmarshal(received, &apiReq); err != nil {
		t.Fatalf("unmarshal received: %v", err)
	}
	if apiReq["blocks"] == nil {
		t.Error("expected blocks in API request")
	}
}

func TestOutboundPost_RateLimited(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  3,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`{"text":"retry me"}`)
	err := poster.post(context.Background(), payload)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
}

func TestOutboundPost_SlackError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  1,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`{"text":"bad"}`)
	err := poster.post(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for Slack API error response")
	}
}

func TestOutboundPost_NoChannel(t *testing.T) {
	cfg := &slackConfig{
		BotToken:      "xoxb-test",
		RetryAttempts: 1,
		SlackAPIURL:   "http://unused",
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`{"text":"no channel"}`)
	err := poster.post(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error when no channel specified")
	}
}

func TestInboundHandler(t *testing.T) {
	eventPayload := json.RawMessage(`{"envelope_id":"env1","type":"events_api","payload":{"event":{"type":"message","text":"hello"}}}`)

	mockConn := newMockSocketModeConn([]mockEvent{
		{payload: eventPayload, envelopeID: "env1"},
	})

	s := &mockSender{}
	logger := logr.Discard()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runInboundLoop(ctx, mockConn, s, logger)
		close(done)
	}()

	// Wait for the event to be processed.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for inbound message")
		default:
		}
		sent := s.getSent()
		if len(sent) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	sent := s.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	if string(sent[0]) != string(eventPayload) {
		t.Errorf("payload = %s, want %s", sent[0], eventPayload)
	}

	acked := mockConn.getAcked()
	if len(acked) != 1 || acked[0] != "env1" {
		t.Errorf("acked = %v, want [env1]", acked)
	}
}

func TestInboundHandler_NoEnvelopeID(t *testing.T) {
	// Events without envelope_id (e.g., hello message) should still forward.
	eventPayload := json.RawMessage(`{"type":"hello"}`)

	mockConn := newMockSocketModeConn([]mockEvent{
		{payload: eventPayload, envelopeID: ""},
	})

	s := &mockSender{}
	logger := logr.Discard()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runInboundLoop(ctx, mockConn, s, logger)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for inbound message")
		default:
		}
		sent := s.getSent()
		if len(sent) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	acked := mockConn.getAcked()
	if len(acked) != 0 {
		t.Errorf("acked = %v, want empty (no envelope_id)", acked)
	}
}

func TestInboundHandler_ReadError_Reconnects(t *testing.T) {
	eventPayload := json.RawMessage(`{"type":"events_api","payload":{}}`)

	mockConn := newMockSocketModeConn([]mockEvent{
		{err: fmt.Errorf("connection lost")},
		{payload: eventPayload, envelopeID: "env2"},
	})

	s := &mockSender{}
	logger := logr.Discard()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runInboundLoop(ctx, mockConn, s, logger)
		close(done)
	}()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for reconnect and message")
		default:
		}
		sent := s.getSent()
		if len(sent) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	<-done

	sent := s.getSent()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1 (after reconnect)", len(sent))
	}
}

func TestHealthHandler_Healthy(t *testing.T) {
	h := newHealthHandler(func() bool { return true })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body = %q, want ok", w.Body.String())
	}
}

func TestHealthHandler_Unhealthy(t *testing.T) {
	h := newHealthHandler(func() bool { return false })

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if w.Body.String() != "unhealthy" {
		t.Errorf("body = %q, want unhealthy", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// mockWSConn implements wsConn for slackSocketMode unit tests.
// ---------------------------------------------------------------------------
type mockWSConn struct {
	mu       sync.Mutex
	messages [][]byte
	readIdx  int
	written  [][]byte
	closed   bool
}

func (m *mockWSConn) ReadMessage(_ context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readIdx >= len(m.messages) {
		return nil, fmt.Errorf("no more messages")
	}
	msg := m.messages[m.readIdx]
	m.readIdx++
	return msg, nil
}

func (m *mockWSConn) WriteMessage(_ context.Context, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.written = append(m.written, data)
	return nil
}

func (m *mockWSConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// ---------------------------------------------------------------------------
// slackSocketMode.Connect() tests
// ---------------------------------------------------------------------------

func TestSlackSocketMode_Connect_APIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apps.connections.open" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer xapp-test" {
			t.Errorf("wrong auth header: %s", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
	}))
	defer ts.Close()

	sm := newSlackSocketMode("xapp-test", ts.URL)
	err := sm.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid_auth")
	}
	if sm.Connected() {
		t.Error("should not be connected after API error")
	}
}

func TestSlackSocketMode_Connect_Success(t *testing.T) {
	fakeWS := &mockWSConn{}

	// Override dialWebSocket for this test.
	origDial := dialWebSocket
	dialWebSocket = func(_ context.Context, _ string) (wsConn, error) {
		return fakeWS, nil
	}
	defer func() { dialWebSocket = origDial }()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true,"url":"wss://fake.slack.test/ws"}`))
	}))
	defer ts.Close()

	sm := newSlackSocketMode("xapp-test", ts.URL)
	err := sm.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !sm.Connected() {
		t.Error("should be connected after successful Connect")
	}
}

func TestSlackSocketMode_Connect_DialError(t *testing.T) {
	origDial := dialWebSocket
	dialWebSocket = func(_ context.Context, _ string) (wsConn, error) {
		return nil, fmt.Errorf("dial failed")
	}
	defer func() { dialWebSocket = origDial }()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true,"url":"wss://fake.slack.test/ws"}`))
	}))
	defer ts.Close()

	sm := newSlackSocketMode("xapp-test", ts.URL)
	err := sm.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error when dial fails")
	}
	if sm.Connected() {
		t.Error("should not be connected after dial error")
	}
}

func TestSlackSocketMode_Connect_HTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer ts.Close()

	sm := newSlackSocketMode("xapp-test", ts.URL)
	err := sm.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed response")
	}
}

// ---------------------------------------------------------------------------
// slackSocketMode.ReadEvent() tests
// ---------------------------------------------------------------------------

func TestSlackSocketMode_ReadEvent(t *testing.T) {
	envelope := `{"envelope_id":"env123","type":"events_api","payload":{"event":{"type":"message"}}}`
	sm := &slackSocketMode{
		wsConn: &mockWSConn{messages: [][]byte{[]byte(envelope)}},
	}
	sm.connected.Store(true)

	payload, envID, err := sm.ReadEvent(context.Background())
	if err != nil {
		t.Fatalf("ReadEvent: %v", err)
	}
	if envID != "env123" {
		t.Errorf("envelopeID = %q, want env123", envID)
	}
	if len(payload) == 0 {
		t.Error("expected non-empty payload")
	}
}

func TestSlackSocketMode_ReadEvent_NotConnected(t *testing.T) {
	sm := &slackSocketMode{}
	_, _, err := sm.ReadEvent(context.Background())
	if err == nil {
		t.Fatal("expected error when not connected")
	}
}

func TestSlackSocketMode_ReadEvent_WSError(t *testing.T) {
	sm := &slackSocketMode{
		wsConn: &mockWSConn{messages: nil}, // no messages → error
	}
	sm.connected.Store(true)

	_, _, err := sm.ReadEvent(context.Background())
	if err == nil {
		t.Fatal("expected error on WebSocket read failure")
	}
	if sm.Connected() {
		t.Error("should mark as disconnected on read error")
	}
}

func TestSlackSocketMode_ReadEvent_MalformedJSON(t *testing.T) {
	sm := &slackSocketMode{
		wsConn: &mockWSConn{messages: [][]byte{[]byte("not json")}},
	}
	sm.connected.Store(true)

	_, _, err := sm.ReadEvent(context.Background())
	if err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

// ---------------------------------------------------------------------------
// slackSocketMode.Acknowledge() tests
// ---------------------------------------------------------------------------

func TestSlackSocketMode_Acknowledge(t *testing.T) {
	ws := &mockWSConn{}
	sm := &slackSocketMode{wsConn: ws}
	sm.connected.Store(true)

	err := sm.Acknowledge(context.Background(), "env456")
	if err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	ws.mu.Lock()
	defer ws.mu.Unlock()
	if len(ws.written) != 1 {
		t.Fatalf("written %d messages, want 1", len(ws.written))
	}

	var ack map[string]string
	if err := json.Unmarshal(ws.written[0], &ack); err != nil {
		t.Fatalf("unmarshal ack: %v", err)
	}
	if ack["envelope_id"] != "env456" {
		t.Errorf("envelope_id = %q, want env456", ack["envelope_id"])
	}
}

func TestSlackSocketMode_Acknowledge_NotConnected(t *testing.T) {
	sm := &slackSocketMode{}
	err := sm.Acknowledge(context.Background(), "env789")
	if err == nil {
		t.Fatal("expected error when not connected")
	}
}

// ---------------------------------------------------------------------------
// slackSocketMode.Connected() / Close() tests
// ---------------------------------------------------------------------------

func TestSlackSocketMode_Connected_Close(t *testing.T) {
	ws := &mockWSConn{}
	sm := &slackSocketMode{wsConn: ws}
	sm.connected.Store(true)

	if !sm.Connected() {
		t.Error("should be connected")
	}

	if err := sm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sm.Connected() {
		t.Error("should not be connected after Close")
	}

	ws.mu.Lock()
	if !ws.closed {
		t.Error("underlying wsConn should be closed")
	}
	ws.mu.Unlock()

	// Close is idempotent — calling again should not panic.
	if err := sm.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestSlackSocketMode_Close_NilConn(t *testing.T) {
	sm := &slackSocketMode{}
	if err := sm.Close(); err != nil {
		t.Fatalf("Close with nil conn: %v", err)
	}
}

// ---------------------------------------------------------------------------
// runOutboundLoop tests
// ---------------------------------------------------------------------------

// mockPoster captures calls to post().
type mockPoster struct {
	mu      sync.Mutex
	posted  []json.RawMessage
	postErr error
}

func (m *mockPoster) post(_ context.Context, payload json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posted = append(m.posted, payload)
	return m.postErr
}

func (m *mockPoster) getPosted() []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]json.RawMessage(nil), m.posted...)
}

// posterAdapter wraps mockPoster to satisfy slackPoster type needed by runOutboundLoop.
// Since runOutboundLoop uses *slackPoster directly, we test via the real slackPoster
// with an httptest server.
func TestRunOutboundLoop(t *testing.T) {
	var received [][]byte
	var mu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  1,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	ch := make(chan *channel.InboundMessage, 2)
	ch <- &channel.InboundMessage{
		ID:      "msg1",
		Payload: json.RawMessage(`{"text":"hello"}`),
	}
	ch <- &channel.InboundMessage{
		ID:      "msg2",
		Payload: json.RawMessage(`{"text":"world"}`),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runOutboundLoop(ctx, ch, poster, logr.Discard())
		close(done)
	}()

	// Wait for both messages to be processed.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for outbound messages")
		default:
		}
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("received %d messages, want 2", len(received))
	}
}

func TestRunOutboundLoop_ContextCancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  1,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)
	ch := make(chan *channel.InboundMessage)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runOutboundLoop(ctx, ch, poster, logr.Discard())
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runOutboundLoop did not exit on context cancel")
	}
}

func TestRunOutboundLoop_ChannelClosed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  1,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)
	ch := make(chan *channel.InboundMessage)
	close(ch)

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		runOutboundLoop(ctx, ch, poster, logr.Discard())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runOutboundLoop did not exit when channel closed")
	}
}

func TestRunOutboundLoop_PostError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  1,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)
	ch := make(chan *channel.InboundMessage, 1)
	ch <- &channel.InboundMessage{
		ID:      "msg1",
		Payload: json.RawMessage(`{"text":"fail"}`),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runOutboundLoop(ctx, ch, poster, logr.Discard())
		close(done)
	}()

	// Give time for the message to be processed (error is logged, not fatal).
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
}

// ---------------------------------------------------------------------------
// retrySleep tests
// ---------------------------------------------------------------------------

func TestRetrySleep_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	err := retrySleep(ctx, 0)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestRetrySleep_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// attempt=0 → 1 second sleep. This is slow but correct.
	// Use a goroutine to verify it completes.
	start := time.Now()
	err := retrySleep(ctx, 0)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("retrySleep: %v", err)
	}
	if elapsed < 900*time.Millisecond {
		t.Errorf("retrySleep returned too quickly: %v", elapsed)
	}
}

// ---------------------------------------------------------------------------
// slackPoster.doPost() error path tests
// ---------------------------------------------------------------------------

func TestDoPost_TransientError_Retries(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			// Close connection abruptly to simulate transient error.
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal error"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  5,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	// doPost should fail for 500 (non-ok response parsed as JSON error).
	// Test the full post() path with retry.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	payload := json.RawMessage(`{"text":"retry me"}`)
	err := poster.post(ctx, payload)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if got := attempts.Load(); got < 3 {
		t.Errorf("attempts = %d, want >= 3", got)
	}
}

func TestDoPost_MalformedResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json at all"))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  1,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`{"text":"bad response"}`)
	err := poster.post(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for malformed JSON response")
	}
}

func TestDoPost_ContextTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Delay to trigger context timeout.
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  2,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	payload := json.RawMessage(`{"text":"timeout"}`)
	err := poster.post(ctx, payload)
	if err == nil {
		t.Fatal("expected error for context timeout")
	}
}

func TestDoPost_RateLimited_ContextCancelled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  3,
		SlackAPIURL:    ts.URL,
	}
	poster := newSlackPoster(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	payload := json.RawMessage(`{"text":"rate limited"}`)
	err := poster.post(ctx, payload)
	if err == nil {
		t.Fatal("expected error for cancelled context during rate limit wait")
	}
}

func TestOutboundPost_InvalidPayload(t *testing.T) {
	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  1,
		SlackAPIURL:    "http://unused",
	}
	poster := newSlackPoster(cfg)

	payload := json.RawMessage(`not valid json`)
	err := poster.post(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for invalid payload JSON")
	}
}

// ---------------------------------------------------------------------------
// parseConfig additional edge cases
// ---------------------------------------------------------------------------

func TestParseConfig_InvalidJSON(t *testing.T) {
	_, err := parseConfig(`{invalid`)
	if err == nil {
		t.Fatal("expected error for invalid JSON config")
	}
}

func TestParseConfig_ZeroValues(t *testing.T) {
	// JSON that explicitly sets zero values — defaults should override.
	raw := `{"listenPort":0,"retryAttempts":0,"slackAPIURL":""}`
	cfg, err := parseConfig(raw)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ListenPort != 8080 {
		t.Errorf("ListenPort = %d, want 8080", cfg.ListenPort)
	}
	if cfg.RetryAttempts != 3 {
		t.Errorf("RetryAttempts = %d, want 3", cfg.RetryAttempts)
	}
	if cfg.SlackAPIURL != "https://slack.com/api" {
		t.Errorf("SlackAPIURL = %q, want default", cfg.SlackAPIURL)
	}
}

func TestSlackSocketMode_Connect_BadResponseBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not json`))
	}))
	defer ts.Close()

	sm := newSlackSocketMode("xapp-test", ts.URL)
	err := sm.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed response JSON")
	}
}

func TestInboundHandler_SendError(t *testing.T) {
	// Test that runInboundLoop continues after a Send error.
	eventPayload1 := json.RawMessage(`{"envelope_id":"e1","type":"events_api","payload":{}}`)
	eventPayload2 := json.RawMessage(`{"envelope_id":"e2","type":"events_api","payload":{}}`)

	mockConn := newMockSocketModeConn([]mockEvent{
		{payload: eventPayload1, envelopeID: "e1"},
		{payload: eventPayload2, envelopeID: "e2"},
	})

	failOnce := &failOnceSender{}
	logger := logr.Discard()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runInboundLoop(ctx, mockConn, failOnce, logger)
		close(done)
	}()

	// Wait for both events.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out")
		default:
		}
		failOnce.mu.Lock()
		n := failOnce.calls
		failOnce.mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	// Both events should have been acknowledged despite the first Send failing.
	acked := mockConn.getAcked()
	if len(acked) != 2 {
		t.Errorf("acked = %v, want 2 envelopes", acked)
	}
}

// failOnceSender fails the first Send, succeeds thereafter.
type failOnceSender struct {
	mu    sync.Mutex
	calls int
	sent  []json.RawMessage
}

func (f *failOnceSender) Send(_ context.Context, payload json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		return fmt.Errorf("simulated send failure")
	}
	f.sent = append(f.sent, payload)
	return nil
}

func TestInboundHandler_ReconnectFailsThenSucceeds(t *testing.T) {
	eventPayload := json.RawMessage(`{"envelope_id":"e1","type":"events_api","payload":{}}`)

	mc := &mockSocketModeConn{
		events: []mockEvent{
			{err: fmt.Errorf("ws broken")},
			{payload: eventPayload, envelopeID: "e1"},
		},
		connected:  true,
		connectErr: nil,
	}
	// Make the first reconnect attempt fail, second succeed.
	reconnectAttempts := atomic.Int32{}
	origConnect := mc.connectErr
	_ = origConnect

	mc2 := &reconnectMockConn{
		inner:           mc,
		failReconnectN:  1,
		reconnectCalled: &reconnectAttempts,
	}

	s := &mockSender{}
	logger := logr.Discard()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runInboundLoop(ctx, mc2, s, logger)
		close(done)
	}()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for message after reconnect")
		default:
		}
		sent := s.getSent()
		if len(sent) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	<-done

	if got := reconnectAttempts.Load(); got < 2 {
		t.Errorf("reconnect attempts = %d, want >= 2", got)
	}
}

// reconnectMockConn wraps mockSocketModeConn but fails the first N reconnect attempts.
type reconnectMockConn struct {
	inner           *mockSocketModeConn
	failReconnectN  int32
	reconnectCalled *atomic.Int32
}

func (r *reconnectMockConn) Connect(ctx context.Context) error {
	n := r.reconnectCalled.Add(1)
	if n <= r.failReconnectN {
		return fmt.Errorf("reconnect attempt %d failed", n)
	}
	return r.inner.Connect(ctx)
}

func (r *reconnectMockConn) ReadEvent(ctx context.Context) (json.RawMessage, string, error) {
	return r.inner.ReadEvent(ctx)
}

func (r *reconnectMockConn) Acknowledge(ctx context.Context, envelopeID string) error {
	return r.inner.Acknowledge(ctx, envelopeID)
}

func (r *reconnectMockConn) Connected() bool {
	return r.inner.Connected()
}

func (r *reconnectMockConn) Close() error {
	return r.inner.Close()
}

// ---------------------------------------------------------------------------
// rateLimitError and slackAPIError string representations
// ---------------------------------------------------------------------------

func TestRateLimitError_Error(t *testing.T) {
	e := &rateLimitError{retryAfter: 5 * time.Second}
	s := e.Error()
	if s == "" {
		t.Error("expected non-empty error string")
	}
}

func TestSlackAPIError_Error(t *testing.T) {
	e := &slackAPIError{code: "channel_not_found"}
	s := e.Error()
	if s == "" {
		t.Error("expected non-empty error string")
	}
}

// ---------------------------------------------------------------------------
// mockChannelClient implements channelClient for testing run().
// ---------------------------------------------------------------------------
type mockChannelClient struct {
	mu            sync.Mutex
	sent          []json.RawMessage
	inbound       chan *channel.InboundMessage
	bufferedCount int
	closed        bool
	receiveErr    error
}

func newMockChannelClient() *mockChannelClient {
	return &mockChannelClient{
		inbound: make(chan *channel.InboundMessage, 64),
	}
}

func (m *mockChannelClient) Send(_ context.Context, payload json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, payload)
	return nil
}

func (m *mockChannelClient) Receive(_ context.Context) (<-chan *channel.InboundMessage, error) {
	if m.receiveErr != nil {
		return nil, m.receiveErr
	}
	return m.inbound, nil
}

func (m *mockChannelClient) BufferedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bufferedCount
}

func (m *mockChannelClient) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// ---------------------------------------------------------------------------
// run() tests
// ---------------------------------------------------------------------------

func TestRun_OutboundMode(t *testing.T) {
	slackAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer slackAPI.Close()

	cfg := &slackConfig{
		BotToken:       "xoxb-test",
		DefaultChannel: "C999",
		RetryAttempts:  1,
		SlackAPIURL:    slackAPI.URL,
		ListenPort:     0, // will bind to random port
	}

	client := newMockChannelClient()
	client.inbound <- &channel.InboundMessage{
		ID:      "msg1",
		Payload: json.RawMessage(`{"text":"hello"}`),
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Use port 0 to get a random free port.
	cfg.ListenPort = 19876

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, cfg, client, "outbound", logr.Discard())
	}()

	// Give the server time to start.
	time.Sleep(200 * time.Millisecond)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit")
	}
}

func TestRun_InboundMode_MissingAppToken(t *testing.T) {
	cfg := &slackConfig{
		BotToken:      "xoxb-test",
		RetryAttempts: 1,
		ListenPort:    19877,
	}

	client := newMockChannelClient()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := run(ctx, cfg, client, "inbound", logr.Discard())
	if err == nil {
		t.Fatal("expected error for missing app token in inbound mode")
	}
}

func TestRun_NoMode(t *testing.T) {
	cfg := &slackConfig{
		BotToken:      "xoxb-test",
		RetryAttempts: 1,
		ListenPort:    19878,
	}

	client := newMockChannelClient()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, cfg, client, "", logr.Discard())
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit")
	}
}

func TestRun_ReceiveError(t *testing.T) {
	cfg := &slackConfig{
		BotToken:      "xoxb-test",
		RetryAttempts: 1,
		ListenPort:    19879,
	}

	client := newMockChannelClient()
	client.receiveErr = fmt.Errorf("receive failed")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := run(ctx, cfg, client, "outbound", logr.Discard())
	if err == nil {
		t.Fatal("expected error when Receive fails")
	}
}

// ---------------------------------------------------------------------------
// ws.go: coderWSConn integration tests with real WebSocket server
// ---------------------------------------------------------------------------

// newTestWSServer creates an httptest server that accepts one WS connection,
// echoes messages, and optionally sends a pre-configured message.
func newTestWSServer(t *testing.T, serverMsg []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Logf("websocket.Accept: %v", err)
			return
		}
		defer conn.CloseNow()

		if serverMsg != nil {
			if err := conn.Write(r.Context(), websocket.MessageText, serverMsg); err != nil {
				t.Logf("server write: %v", err)
				return
			}
		}

		// Echo loop: read one message and echo it back.
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		conn.Write(r.Context(), websocket.MessageText, data)
		conn.Close(websocket.StatusNormalClosure, "done")
	}))
}

func TestCoderWSConn_ReadWriteClose(t *testing.T) {
	serverMsg := []byte(`{"hello":"world"}`)
	ts := newTestWSServer(t, serverMsg)
	defer ts.Close()

	wsURL := "ws" + ts.URL[len("http"):]
	ctx := context.Background()

	ws, err := defaultDialWebSocket(ctx, wsURL)
	if err != nil {
		t.Fatalf("dialWebSocket: %v", err)
	}

	// Read the server-sent message.
	data, err := ws.ReadMessage(ctx)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(data) != string(serverMsg) {
		t.Errorf("got %q, want %q", data, serverMsg)
	}

	// Write a message.
	echoPayload := []byte(`{"echo":"test"}`)
	if err := ws.WriteMessage(ctx, echoPayload); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	// Read echo response.
	data, err = ws.ReadMessage(ctx)
	if err != nil {
		t.Fatalf("ReadMessage echo: %v", err)
	}
	if string(data) != string(echoPayload) {
		t.Errorf("echo got %q, want %q", data, echoPayload)
	}

	// Close.
	if err := ws.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestDialWebSocket_InvalidURL(t *testing.T) {
	_, err := defaultDialWebSocket(context.Background(), "ws://127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

// ---------------------------------------------------------------------------
// Full Connect → ReadEvent → Acknowledge integration via real WS server
// ---------------------------------------------------------------------------

func TestCoderWSConn_ReadError(t *testing.T) {
	// Server closes immediately without sending a message → ReadMessage error.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.Close(websocket.StatusNormalClosure, "bye")
	}))
	defer ts.Close()

	wsURL := "ws" + ts.URL[len("http"):]
	ws, err := defaultDialWebSocket(context.Background(), wsURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	_, err = ws.ReadMessage(context.Background())
	if err == nil {
		t.Fatal("expected error reading from closed server")
	}
}

func TestCoderWSConn_WriteError(t *testing.T) {
	// Server closes immediately → after reading the close frame, writes should fail.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.Close(websocket.StatusNormalClosure, "bye")
	}))
	defer ts.Close()

	wsURL := "ws" + ts.URL[len("http"):]
	ws, err := defaultDialWebSocket(context.Background(), wsURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// Read to consume the close frame so the connection is truly closed.
	_, _ = ws.ReadMessage(context.Background())

	err = ws.WriteMessage(context.Background(), []byte("test"))
	if err == nil {
		t.Fatal("expected error writing to closed connection")
	}
}

func TestSlackSocketMode_FullIntegration(t *testing.T) {
	envelope := `{"envelope_id":"integ1","type":"events_api","payload":{"event":{"type":"message","text":"hi"}}}`

	// WS server that sends an envelope.
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()

		// Send the envelope.
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(envelope)); err != nil {
			return
		}

		// Read the ACK.
		_, _, err = conn.Read(r.Context())
		if err != nil {
			return
		}

		conn.Close(websocket.StatusNormalClosure, "done")
	}))
	defer wsServer.Close()

	wsURL := "ws" + wsServer.URL[len("http"):]

	// Override dialWebSocket.
	origDial := dialWebSocket
	dialWebSocket = defaultDialWebSocket
	defer func() { dialWebSocket = origDial }()

	// Mock the apps.connections.open API to return our WS URL.
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"ok":true,"url":%q}`, wsURL)
	}))
	defer apiServer.Close()

	sm := newSlackSocketMode("xapp-test", apiServer.URL)
	ctx := context.Background()

	if err := sm.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sm.Close()

	if !sm.Connected() {
		t.Fatal("expected Connected() to be true")
	}

	// ReadEvent.
	payload, envID, err := sm.ReadEvent(ctx)
	if err != nil {
		t.Fatalf("ReadEvent: %v", err)
	}
	if envID != "integ1" {
		t.Errorf("envelopeID = %q, want integ1", envID)
	}
	if len(payload) == 0 {
		t.Error("expected non-empty payload")
	}

	// Acknowledge.
	if err := sm.Acknowledge(ctx, envID); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
}
