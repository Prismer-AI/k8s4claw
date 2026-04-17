package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/go-logr/logr"

	channel "github.com/Prismer-AI/k8s4claw/sdk/channel"
)

type mockChannelClient struct {
	mu            sync.Mutex
	sent          []json.RawMessage
	inbound       chan *channel.InboundMessage
	sendErr       error
	receiveErr    error
	bufferedCount int
}

func newMockChannelClient() *mockChannelClient {
	return &mockChannelClient{
		inbound: make(chan *channel.InboundMessage, 64),
	}
}

func (m *mockChannelClient) Send(_ context.Context, payload json.RawMessage) error {
	if m.sendErr != nil {
		return m.sendErr
	}
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
	return nil
}

func (m *mockChannelClient) Sent() []json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]json.RawMessage(nil), m.sent...)
}

type sentDiscordMessage struct {
	channelID string
	content   string
}

type mockDiscordSession struct {
	mu         sync.Mutex
	handler    func(*discordgo.Session, *discordgo.MessageCreate)
	sent       []sentDiscordMessage
	openErr    error
	sendErr    error
	openCount  int
	closeCount int
}

func (m *mockDiscordSession) AddHandler(handler interface{}) func() {
	if h, ok := handler.(func(*discordgo.Session, *discordgo.MessageCreate)); ok {
		m.mu.Lock()
		m.handler = h
		m.mu.Unlock()
	}
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.handler = nil
	}
}

func (m *mockDiscordSession) Open() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.openCount++
	return m.openErr
}

func (m *mockDiscordSession) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeCount++
	return nil
}

func (m *mockDiscordSession) ChannelMessageSend(channelID, content string, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	if m.sendErr != nil {
		return nil, m.sendErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentDiscordMessage{channelID: channelID, content: content})
	return &discordgo.Message{ChannelID: channelID, Content: content}, nil
}

func (m *mockDiscordSession) Emit(msg *discordgo.MessageCreate) {
	m.mu.Lock()
	handler := m.handler
	m.mu.Unlock()
	if handler != nil {
		handler(nil, msg)
	}
}

func (m *mockDiscordSession) Sent() []sentDiscordMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sentDiscordMessage(nil), m.sent...)
}

func TestParseConfig(t *testing.T) {
	cfg, err := parseConfig(`{"channelId":"12345","listenPort":9090}`)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ChannelID != "12345" {
		t.Errorf("ChannelID = %q, want %q", cfg.ChannelID, "12345")
	}
	if cfg.ListenPort != 9090 {
		t.Errorf("ListenPort = %d, want %d", cfg.ListenPort, 9090)
	}
}

func TestParseConfig_Defaults(t *testing.T) {
	cfg, err := parseConfig("")
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ListenPort != defaultListenPort {
		t.Errorf("ListenPort = %d, want %d", cfg.ListenPort, defaultListenPort)
	}
}

func TestParseConfig_InvalidJSON(t *testing.T) {
	if _, err := parseConfig(`{`); err == nil {
		t.Fatal("expected parseConfig to fail for invalid JSON")
	}
}

func TestNewLogger(t *testing.T) {
	logger, err := newLogger()
	if err != nil {
		t.Fatalf("newLogger: %v", err)
	}
	logger.Info("logger initialized for test")
}

func TestNewDiscordSession(t *testing.T) {
	session, err := newDiscordSession("test-token")
	if err != nil {
		t.Fatalf("newDiscordSession: %v", err)
	}
	if session == nil {
		t.Fatal("expected session to be created")
	}
	_ = session.Close()
}

func TestMainRun_MissingToken(t *testing.T) {
	t.Setenv("CHANNEL_CONFIG", "")
	t.Setenv("DISCORD_TOKEN", "")

	if err := mainRun(); err == nil {
		t.Fatal("expected mainRun to fail when DISCORD_TOKEN is missing")
	}
}

func TestMainRun_ParseConfigError(t *testing.T) {
	t.Setenv("CHANNEL_CONFIG", `{`)
	t.Setenv("DISCORD_TOKEN", "test-token")

	if err := mainRun(); err == nil {
		t.Fatal("expected mainRun to fail for invalid config")
	}
}

func TestMainRun_ConnectError(t *testing.T) {
	restore := stubMainRunHooks()
	defer restore()

	t.Setenv("CHANNEL_CONFIG", "")
	t.Setenv("DISCORD_TOKEN", "test-token")

	connectBusFn = func(context.Context, logr.Logger) (channelClient, error) {
		return nil, errors.New("connect failed")
	}

	if err := mainRun(); err == nil {
		t.Fatal("expected mainRun to fail when IPC bus connection fails")
	}
}

func TestMainRun_NewSessionError(t *testing.T) {
	restore := stubMainRunHooks()
	defer restore()

	t.Setenv("CHANNEL_CONFIG", "")
	t.Setenv("DISCORD_TOKEN", "test-token")

	connectBusFn = func(context.Context, logr.Logger) (channelClient, error) {
		return newMockChannelClient(), nil
	}
	newDiscordSessionFn = func(string) (discordSession, error) {
		return nil, errors.New("session failed")
	}

	if err := mainRun(); err == nil {
		t.Fatal("expected mainRun to fail when Discord session creation fails")
	}
}

func TestMainRun_Success(t *testing.T) {
	restore := stubMainRunHooks()
	defer restore()

	t.Setenv("CHANNEL_CONFIG", `{"channelId":"chan-1","listenPort":9090}`)
	t.Setenv("CHANNEL_MODE", "bidirectional")
	t.Setenv("DISCORD_TOKEN", "test-token")

	client := newMockChannelClient()
	session := &mockDiscordSession{}

	connectBusFn = func(context.Context, logr.Logger) (channelClient, error) {
		return client, nil
	}
	newDiscordSessionFn = func(token string) (discordSession, error) {
		if token != "test-token" {
			t.Fatalf("token = %q, want %q", token, "test-token")
		}
		return session, nil
	}
	runFn = func(_ context.Context, cfg *discordConfig, gotClient channelClient, gotSession discordSession, mode string, _ logr.Logger, _ ...runOpts) error {
		if cfg.ChannelID != "chan-1" {
			t.Fatalf("cfg.ChannelID = %q, want %q", cfg.ChannelID, "chan-1")
		}
		if cfg.ListenPort != 9090 {
			t.Fatalf("cfg.ListenPort = %d, want %d", cfg.ListenPort, 9090)
		}
		if gotClient != client {
			t.Fatal("run received unexpected client")
		}
		if gotSession != session {
			t.Fatal("run received unexpected session")
		}
		if mode != "bidirectional" {
			t.Fatalf("mode = %q, want %q", mode, "bidirectional")
		}
		return nil
	}

	if err := mainRun(); err != nil {
		t.Fatalf("mainRun: %v", err)
	}
}

func TestDiscordMessageHandler_ForwardsMessage(t *testing.T) {
	client := newMockChannelClient()
	tracker := newChannelState("")
	handler := newDiscordMessageHandler(context.Background(), client, &discordConfig{ChannelID: "chan-1"}, tracker, logr.Discard())

	handler(nil, newDiscordMessage("chan-1", "hello from discord", "alice", false))

	sent := client.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}

	var payload inboundRuntimeMessage
	if err := json.Unmarshal(sent[0], &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if payload.Text != "hello from discord" {
		t.Errorf("Text = %q, want %q", payload.Text, "hello from discord")
	}
	if payload.User != "alice" {
		t.Errorf("User = %q, want %q", payload.User, "alice")
	}
	if payload.Thread != "chan-1" {
		t.Errorf("Thread = %q, want %q", payload.Thread, "chan-1")
	}
	if tracker.Get() != "chan-1" {
		t.Errorf("tracker.Get() = %q, want %q", tracker.Get(), "chan-1")
	}
}

func TestDiscordMessageHandler_FiltersMessages(t *testing.T) {
	tests := []struct {
		name string
		cfg  *discordConfig
		msg  *discordgo.MessageCreate
	}{
		{
			name: "bot message",
			cfg:  &discordConfig{ChannelID: "chan-1"},
			msg:  newDiscordMessage("chan-1", "hello", "bot", true),
		},
		{
			name: "wrong channel",
			cfg:  &discordConfig{ChannelID: "chan-1"},
			msg:  newDiscordMessage("chan-2", "hello", "alice", false),
		},
		{
			name: "empty text",
			cfg:  &discordConfig{ChannelID: "chan-1"},
			msg:  newDiscordMessage("chan-1", "   ", "alice", false),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newMockChannelClient()
			handler := newDiscordMessageHandler(context.Background(), client, tt.cfg, newChannelState(""), logr.Discard())
			handler(nil, tt.msg)

			if sent := client.Sent(); len(sent) != 0 {
				t.Fatalf("sent %d messages, want 0", len(sent))
			}
		})
	}
}

func TestDiscordDisplayName_PrefersNick(t *testing.T) {
	msg := newDiscordMessage("chan-1", "hello", "alice", false)
	msg.Member = &discordgo.Member{Nick: "team-alice"}

	if got := discordDisplayName(msg); got != "team-alice" {
		t.Errorf("discordDisplayName() = %q, want %q", got, "team-alice")
	}
}

func TestDiscordPoster_Post(t *testing.T) {
	session := &mockDiscordSession{}
	poster := newDiscordPoster(session, &discordConfig{ChannelID: "chan-1"}, newChannelState(""))

	if err := poster.post(context.Background(), json.RawMessage(`{"text":"hello runtime"}`)); err != nil {
		t.Fatalf("post: %v", err)
	}

	sent := session.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	if sent[0].channelID != "chan-1" {
		t.Errorf("channelID = %q, want %q", sent[0].channelID, "chan-1")
	}
	if sent[0].content != "hello runtime" {
		t.Errorf("content = %q, want %q", sent[0].content, "hello runtime")
	}
}

func TestDiscordPoster_Post_PrefersThreadAndTracker(t *testing.T) {
	session := &mockDiscordSession{}
	tracker := newChannelState("tracked-chan")
	poster := newDiscordPoster(session, &discordConfig{}, tracker)

	if err := poster.post(context.Background(), json.RawMessage(`{"thread":"thread-1","channel":"chan-1","text":"reply"}`)); err != nil {
		t.Fatalf("post with thread: %v", err)
	}
	if err := poster.post(context.Background(), json.RawMessage(`{"text":"fallback"}`)); err != nil {
		t.Fatalf("post with tracker fallback: %v", err)
	}

	sent := session.Sent()
	if len(sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(sent))
	}
	if sent[0].channelID != "thread-1" {
		t.Errorf("first channelID = %q, want %q", sent[0].channelID, "thread-1")
	}
	if sent[1].channelID != "tracked-chan" {
		t.Errorf("second channelID = %q, want %q", sent[1].channelID, "tracked-chan")
	}
}

func TestDiscordPoster_Post_Errors(t *testing.T) {
	session := &mockDiscordSession{}
	poster := newDiscordPoster(session, &discordConfig{}, newChannelState(""))

	if err := poster.post(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Fatal("expected invalid JSON error")
	}
	if err := poster.post(context.Background(), json.RawMessage(`{"text":" "}`)); err == nil {
		t.Fatal("expected empty text error")
	}
	if err := poster.post(context.Background(), json.RawMessage(`{"text":"hello"}`)); err == nil {
		t.Fatal("expected missing destination error")
	}
}

func TestRunOutboundLoop(t *testing.T) {
	session := &mockDiscordSession{}
	poster := newDiscordPoster(session, &discordConfig{ChannelID: "chan-1"}, newChannelState(""))

	ch := make(chan *channel.InboundMessage, 2)
	ch <- &channel.InboundMessage{ID: "msg-1", Payload: json.RawMessage(`{"text":"one"}`)}
	ch <- &channel.InboundMessage{ID: "msg-2", Payload: json.RawMessage(`{"text":"two"}`)}
	close(ch)

	runOutboundLoop(context.Background(), ch, poster, logr.Discard())

	sent := session.Sent()
	if len(sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(sent))
	}
}

func TestHealthHandler(t *testing.T) {
	ok := newHealthHandler(func() bool { return true })
	req := mustRequest(t)
	resp := mustServe(t, ok, req)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthy status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	unhealthy := newHealthHandler(func() bool { return false })
	resp = mustServe(t, unhealthy, req)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("unhealthy status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
}

func TestRun_InboundMode(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	client := newMockChannelClient()
	session := &mockDiscordSession{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, &discordConfig{ChannelID: "chan-1"}, client, session, "inbound", logr.Discard(), runOpts{listener: ln})
	}()

	waitForCondition(t, 2*time.Second, func() bool {
		resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == http.StatusOK
	})

	session.Emit(newDiscordMessage("chan-1", "hello", "alice", false))

	waitForCondition(t, time.Second, func() bool {
		return len(client.Sent()) == 1
	})

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit")
	}

	if session.openCount != 1 {
		t.Errorf("Open called %d times, want 1", session.openCount)
	}
	if session.closeCount == 0 {
		t.Fatal("expected Close to be called")
	}
}

func TestRun_InboundMode_OpenError(t *testing.T) {
	client := newMockChannelClient()
	session := &mockDiscordSession{openErr: errors.New("open failed")}

	err := run(context.Background(), &discordConfig{}, client, session, "inbound", logr.Discard())
	if err == nil {
		t.Fatal("expected run to fail when Discord session open fails")
	}
}

func TestRun_OutboundMode_ReceiveError(t *testing.T) {
	client := newMockChannelClient()
	client.receiveErr = errors.New("receive failed")

	err := run(context.Background(), &discordConfig{}, client, &mockDiscordSession{}, "outbound", logr.Discard())
	if err == nil {
		t.Fatal("expected run to fail when Receive fails")
	}
}

func newDiscordMessage(channelID, text, username string, bot bool) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "msg-" + channelID,
			ChannelID: channelID,
			Content:   text,
			Author: &discordgo.User{
				ID:       "user-" + username,
				Username: username,
				Bot:      bot,
			},
		},
	}
}

func mustRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "/healthz", nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	return req
}

func mustServe(t *testing.T, h http.Handler, req *http.Request) *http.Response {
	t.Helper()
	rr := &responseRecorder{header: make(http.Header)}
	h.ServeHTTP(rr, req)
	return rr.Result()
}

type responseRecorder struct {
	code   int
	header http.Header
	body   []byte
}

func (r *responseRecorder) Header() http.Header {
	return r.header
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	r.body = append(r.body, data...)
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return len(data), nil
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	r.code = statusCode
}

func (r *responseRecorder) Result() *http.Response {
	statusCode := r.code
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     r.header.Clone(),
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func stubMainRunHooks() func() {
	oldNewLoggerFn := newLoggerFn
	oldConnectBusFn := connectBusFn
	oldNewDiscordSessionFn := newDiscordSessionFn
	oldRunFn := runFn

	newLoggerFn = func() (logr.Logger, error) {
		return logr.Discard(), nil
	}
	connectBusFn = func(context.Context, logr.Logger) (channelClient, error) {
		return newMockChannelClient(), nil
	}
	newDiscordSessionFn = func(string) (discordSession, error) {
		return &mockDiscordSession{}, nil
	}
	runFn = func(context.Context, *discordConfig, channelClient, discordSession, string, logr.Logger, ...runOpts) error {
		return nil
	}

	return func() {
		newLoggerFn = oldNewLoggerFn
		connectBusFn = oldConnectBusFn
		newDiscordSessionFn = oldNewDiscordSessionFn
		runFn = oldRunFn
	}
}
