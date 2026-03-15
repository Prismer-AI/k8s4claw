package main

import (
	"context"
	"fmt"

	"github.com/coder/websocket"
)

// coderWSConn wraps a coder/websocket.Conn to implement wsConn.
type coderWSConn struct {
	conn *websocket.Conn
}

// dialWebSocket is a package-level var to allow test injection.
var dialWebSocket = defaultDialWebSocket

func defaultDialWebSocket(ctx context.Context, url string) (wsConn, error) {
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to dial WebSocket at %s: %w", url, err)
	}
	return &coderWSConn{conn: conn}, nil
}

func (c *coderWSConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, data, err := c.conn.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read WebSocket message: %w", err)
	}
	return data, nil
}

func (c *coderWSConn) WriteMessage(ctx context.Context, data []byte) error {
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("failed to write WebSocket message: %w", err)
	}
	return nil
}

func (c *coderWSConn) Close() error {
	return c.conn.Close(websocket.StatusNormalClosure, "closing")
}
