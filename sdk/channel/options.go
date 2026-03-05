package channel

import (
	"os"
	"time"

	"github.com/go-logr/logr"
)

const (
	defaultSocketPath        = "/var/run/claw/ipc.sock"
	defaultBufferSize        = 256
	defaultReconnectInterval = 2 * time.Second
	defaultHeartbeatInterval = 30 * time.Second
	maxReconnectInterval     = 60 * time.Second
)

type clientConfig struct {
	socketPath        string
	channelName       string
	channelMode       string
	bufferSize        int
	reconnectInterval time.Duration
	heartbeatInterval time.Duration
	logger            logr.Logger
}

func defaultConfig() *clientConfig {
	socketPath := os.Getenv("IPC_SOCKET_PATH")
	if socketPath == "" {
		socketPath = defaultSocketPath
	}
	return &clientConfig{
		socketPath:        socketPath,
		channelName:       os.Getenv("CHANNEL_NAME"),
		channelMode:       os.Getenv("CHANNEL_MODE"),
		bufferSize:        defaultBufferSize,
		reconnectInterval: defaultReconnectInterval,
		heartbeatInterval: defaultHeartbeatInterval,
		logger:            logr.Discard(),
	}
}

// Option configures a channel Client.
type Option func(*clientConfig)

// WithSocketPath sets the IPC Bus UDS path.
func WithSocketPath(path string) Option {
	return func(c *clientConfig) { c.socketPath = path }
}

// WithChannelName sets the channel name for registration.
func WithChannelName(name string) Option {
	return func(c *clientConfig) { c.channelName = name }
}

// WithChannelMode sets the channel mode (inbound/outbound/bidirectional).
func WithChannelMode(mode string) Option {
	return func(c *clientConfig) { c.channelMode = mode }
}

// WithBufferSize sets the bus-down buffer capacity.
func WithBufferSize(size int) Option {
	return func(c *clientConfig) { c.bufferSize = size }
}

// WithReconnectInterval sets the base reconnect interval.
func WithReconnectInterval(d time.Duration) Option {
	return func(c *clientConfig) { c.reconnectInterval = d }
}

// WithHeartbeatInterval sets the heartbeat period.
func WithHeartbeatInterval(d time.Duration) Option {
	return func(c *clientConfig) { c.heartbeatInterval = d }
}

// WithLogger sets the logger.
func WithLogger(l logr.Logger) Option {
	return func(c *clientConfig) { c.logger = l }
}
