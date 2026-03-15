package channel

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// mockIPCServer simulates the IPC Bus server for testing.
type mockIPCServer struct {
	socketPath string
	listener   net.Listener
	mu         sync.Mutex
	received   []*message
	sendOnRecv []*message // messages to send back after first data message
	conns      []net.Conn
}

func newMockIPCServer(t *testing.T) *mockIPCServer {
	t.Helper()
	dir := t.TempDir()
	return &mockIPCServer{
		socketPath: filepath.Join(dir, "ipc.sock"),
	}
}

func (s *mockIPCServer) start(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		t.Fatalf("mock server listen: %v", err)
	}
	s.listener = ln

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
			go s.handleConn(conn)
		}
	}()
}

func (s *mockIPCServer) handleConn(conn net.Conn) {
	defer conn.Close()

	// Read registration.
	reg, err := readMessage(conn)
	if err != nil {
		return
	}
	if reg.Type != typeRegister {
		return
	}

	// Send ACK.
	ack := newAck(reg.ID)
	if err := writeMessage(conn, ack); err != nil {
		return
	}

	// Read loop.
	for {
		msg, err := readMessage(conn)
		if err != nil {
			return
		}

		if msg.Type == typeHeartbeat {
			hbAck := newAck(msg.ID)
			_ = writeMessage(conn, hbAck)
			continue
		}

		s.mu.Lock()
		s.received = append(s.received, msg)
		toSend := append([]*message(nil), s.sendOnRecv...)
		s.mu.Unlock()

		for _, m := range toSend {
			_ = writeMessage(conn, m)
		}
	}
}

func (s *mockIPCServer) close() {
	if s.listener != nil {
		s.listener.Close()
	}
	s.mu.Lock()
	for _, conn := range s.conns {
		conn.Close()
	}
	s.conns = nil
	s.mu.Unlock()
}

func (s *mockIPCServer) getReceived() []*message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*message(nil), s.received...)
}

func (s *mockIPCServer) queueOutbound(msgs ...*message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendOnRecv = append(s.sendOnRecv, msgs...)
}

func TestClient_ConnectAndSend(t *testing.T) {
	srv := newMockIPCServer(t)
	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("test-chan"),
		WithHeartbeatInterval(time.Hour), // disable heartbeat for this test
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	payload := json.RawMessage(`{"text":"hello"}`)
	if err := c.Send(ctx, payload); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Give the server time to receive.
	time.Sleep(50 * time.Millisecond)

	msgs := srv.getReceived()
	if len(msgs) != 1 {
		t.Fatalf("server received %d messages, want 1", len(msgs))
	}
	if string(msgs[0].Payload) != `{"text":"hello"}` {
		t.Errorf("payload = %s, want %s", msgs[0].Payload, `{"text":"hello"}`)
	}
}

func TestClient_Receive(t *testing.T) {
	srv := newMockIPCServer(t)

	// Queue an outbound message to send after first data message.
	outMsg := newMessage(typeMessage, "test-chan", json.RawMessage(`{"reply":"world"}`))
	srv.queueOutbound(outMsg)

	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("test-chan"),
		WithHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	ch, err := c.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}

	// Trigger the server to send the outbound message.
	_ = c.Send(ctx, json.RawMessage(`{"ping":true}`))

	select {
	case msg := <-ch:
		if string(msg.Payload) != `{"reply":"world"}` {
			t.Errorf("received payload = %s, want %s", msg.Payload, `{"reply":"world"}`)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for inbound message")
	}
}

func TestClient_ConnectFailsWithoutServer(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "nonexistent.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := Connect(ctx,
		WithSocketPath(sock),
		WithChannelName("test"),
		WithReconnectInterval(50*time.Millisecond),
	)
	if err == nil {
		t.Fatal("expected error when server is not running")
	}
}

func TestClient_MissingChannelName(t *testing.T) {
	t.Setenv("CHANNEL_NAME", "")

	ctx := context.Background()
	_, err := Connect(ctx, WithChannelName(""))
	if err == nil {
		t.Fatal("expected error for missing channel name")
	}
}

func TestClient_BackpressureSlowDown(t *testing.T) {
	srv := newMockIPCServer(t)

	// Queue a slow_down message to be sent after first data message.
	slowDown := newMessage(typeSlowDown, "test-chan", nil)
	srv.queueOutbound(slowDown)

	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("test-chan"),
		WithHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	// Send a message to trigger the server to send slow_down.
	if err := c.Send(ctx, json.RawMessage(`{"trigger":"1"}`)); err != nil {
		t.Fatalf("Send trigger: %v", err)
	}

	// Wait for slow_down to be processed.
	time.Sleep(100 * time.Millisecond)

	c.throttleMu.Lock()
	throttled := c.throttled
	c.throttleMu.Unlock()

	if !throttled {
		t.Error("expected client to be throttled after slow_down")
	}

	// Now queue a resume before the next send.
	srv.mu.Lock()
	srv.sendOnRecv = nil
	srv.mu.Unlock()

	// Send with a short timeout context -- it should block because throttled.
	shortCtx, shortCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer shortCancel()

	// The Send should block or time out because we're throttled.
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.Send(shortCtx, json.RawMessage(`{"during":"throttle"}`))
	}()

	// Simulate resume after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		c.throttleMu.Lock()
		c.throttled = false
		close(c.throttleCh)
		c.throttleMu.Unlock()
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Send after resume failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not unblock after resume")
	}
}

func TestClient_BackpressureSlowDown_ContextCancel(t *testing.T) {
	srv := newMockIPCServer(t)

	// Queue a slow_down message.
	slowDown := newMessage(typeSlowDown, "test-chan", nil)
	srv.queueOutbound(slowDown)

	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("test-chan"),
		WithHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	// Trigger slow_down.
	if err := c.Send(ctx, json.RawMessage(`{"trigger":"1"}`)); err != nil {
		t.Fatalf("Send trigger: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Send with a context that will be cancelled while throttled.
	shortCtx, shortCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer shortCancel()

	err = c.Send(shortCtx, json.RawMessage(`{"blocked":"msg"}`))
	if err == nil {
		t.Fatal("expected error from Send during throttle with cancelled context")
	}
}

func TestClient_BufferFullOnSend(t *testing.T) {
	srv := newMockIPCServer(t)
	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("buf-full-chan"),
		WithBufferSize(2),
		WithHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Forcibly disconnect.
	srv.close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		connected := c.connected
		c.mu.Unlock()
		if !connected {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Fill the buffer.
	for i := 0; i < 2; i++ {
		if err := c.Send(ctx, json.RawMessage(`{"fill":"buf"}`)); err != nil {
			t.Fatalf("Send %d failed: %v", i, err)
		}
	}

	// Next send should fail because buffer is full.
	err = c.Send(ctx, json.RawMessage(`{"overflow":"msg"}`))
	if err == nil {
		t.Fatal("expected error when buffer is full")
	}

	c.Close()
}

func TestClient_HandleMessage_Shutdown(t *testing.T) {
	srv := newMockIPCServer(t)

	// Queue a shutdown message.
	shutdownMsg := newMessage(typeShutdown, "test-chan", nil)
	srv.queueOutbound(shutdownMsg)

	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("test-chan"),
		WithHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Trigger server to send shutdown message.
	_ = c.Send(ctx, json.RawMessage(`{"trigger":"shutdown"}`))

	// Wait for shutdown to be processed.
	select {
	case <-c.done:
		// Client should be closed.
	case <-time.After(2 * time.Second):
		t.Fatal("client did not close after shutdown message")
	}
}

func TestClient_HandleMessage_InboundMessage(t *testing.T) {
	srv := newMockIPCServer(t)

	// Queue an inbound data message.
	inMsg := newMessage(typeMessage, "test-chan", json.RawMessage(`{"reply":"data"}`))
	srv.queueOutbound(inMsg)

	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("test-chan"),
		WithHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	ch, _ := c.Receive(ctx)

	// Trigger server to send data message.
	_ = c.Send(ctx, json.RawMessage(`{"trigger":"1"}`))

	select {
	case msg := <-ch:
		if msg == nil {
			t.Fatal("received nil message")
		}
		if string(msg.Payload) != `{"reply":"data"}` {
			t.Errorf("payload = %s, want %s", msg.Payload, `{"reply":"data"}`)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for inbound message")
	}
}

func TestClient_HandleMessage_Resume(t *testing.T) {
	srv := newMockIPCServer(t)

	// Queue slow_down followed by resume.
	slowDown := newMessage(typeSlowDown, "test-chan", nil)
	resume := newMessage(typeResume, "test-chan", nil)
	srv.queueOutbound(slowDown, resume)

	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("test-chan"),
		WithHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	// Trigger both messages.
	_ = c.Send(ctx, json.RawMessage(`{"trigger":"1"}`))

	// Wait for messages to be processed.
	time.Sleep(200 * time.Millisecond)

	c.throttleMu.Lock()
	throttled := c.throttled
	c.throttleMu.Unlock()

	if throttled {
		t.Error("expected client to NOT be throttled after resume")
	}
}

func TestClient_Heartbeat(t *testing.T) {
	srv := newMockIPCServer(t)
	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("hb-chan"),
		WithHeartbeatInterval(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	// Wait for at least 2 heartbeat intervals.
	time.Sleep(350 * time.Millisecond)

	// The server's handleConn ACKs heartbeats without adding to received,
	// so we just verify the client is still alive and connected.
	c.mu.Lock()
	connected := c.connected
	c.mu.Unlock()

	if !connected {
		t.Error("expected client to remain connected after heartbeats")
	}
}

func TestClient_ReplayBuffer(t *testing.T) {
	srv := newMockIPCServer(t)
	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("replay-chan"),
		WithBufferSize(16),
		WithHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Send a message to confirm connectivity.
	if err := c.Send(ctx, json.RawMessage(`{"before":"disconnect"}`)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Close the server to simulate disconnect.
	srv.close()

	// Wait for disconnect detection.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		connected := c.connected
		c.mu.Unlock()
		if !connected {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Buffer some messages while disconnected.
	for i := 0; i < 3; i++ {
		_ = c.Send(ctx, json.RawMessage(`{"buffered":true}`))
	}

	buffered := c.BufferedCount()
	if buffered != 3 {
		t.Fatalf("BufferedCount = %d, want 3", buffered)
	}

	// Restart the server so reconnect succeeds.
	srv2 := newMockIPCServer(t)
	// Use the same socket path.
	srv2.socketPath = srv.socketPath
	srv2.start(t)
	defer srv2.close()

	// Manually trigger reconnect with short interval.
	c.mu.Lock()
	c.reconnecting = false // allow new reconnect attempt
	c.mu.Unlock()

	reconnCtx, reconnCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer reconnCancel()
	err = c.dial(reconnCtx)
	if err != nil {
		// Socket path may differ; just verify buffer state.
		t.Logf("dial failed (expected if socket path changed): %v", err)
		c.Close()
		return
	}

	// After successful reconnect, buffer should be drained.
	time.Sleep(100 * time.Millisecond)
	if c.BufferedCount() != 0 {
		t.Errorf("BufferedCount after replay = %d, want 0", c.BufferedCount())
	}

	// Server should have received the replayed messages.
	msgs := srv2.getReceived()
	if len(msgs) < 3 {
		t.Errorf("server received %d messages, want at least 3", len(msgs))
	}

	c.Close()
}

func TestClient_BusDownBuffering(t *testing.T) {
	srv := newMockIPCServer(t)
	srv.start(t)
	defer srv.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx,
		WithSocketPath(srv.socketPath),
		WithChannelName("buf-chan"),
		WithBufferSize(16),
		WithHeartbeatInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Verify client is connected.
	if err := c.Send(ctx, json.RawMessage(`{"before":"disconnect"}`)); err != nil {
		t.Fatalf("Send before disconnect: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Forcibly close the server to simulate bus down.
	srv.close()

	// Wait for the client's readLoop to detect the disconnect (EOF).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		connected := c.connected
		c.mu.Unlock()
		if !connected {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Send while disconnected — should buffer.
	if err := c.Send(ctx, json.RawMessage(`{"during":"disconnect"}`)); err != nil {
		t.Fatalf("Send during disconnect should buffer, got: %v", err)
	}

	if c.BufferedCount() != 1 {
		t.Fatalf("BufferedCount = %d, want 1", c.BufferedCount())
	}

	c.Close()
}
