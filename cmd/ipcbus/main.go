package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"

	"github.com/Prismer-AI/k8s4claw/internal/ipcbus"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "shutdown" {
		if err := runShutdown(); err != nil {
			fmt.Fprintf(os.Stderr, "shutdown failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := runServe(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func runServe() error {
	socketPath := flag.String("socket-path", envOr("IPC_SOCKET_PATH", "/var/run/claw/bus.sock"), "UDS socket path")
	walDir := flag.String("wal-dir", envOr("WAL_DIR", "/var/run/claw/wal"), "WAL directory")
	runtime := flag.String("runtime", envOr("CLAW_RUNTIME", "openclaw"), "claw runtime type")
	gatewayPort := flag.Int("gateway-port", envOrInt("CLAW_GATEWAY_PORT", 18900), "gateway port")
	runtimeSocket := flag.String("runtime-socket", envOr("CLAW_RUNTIME_SOCKET", "/var/run/claw/runtime.sock"), "runtime UDS socket path (NanoClaw)")
	bufferSize := flag.Int("buffer-size", envOrInt("BUFFER_SIZE", 1024), "per-channel ring buffer size")
	highWatermark := flag.Float64("high-watermark", 0.8, "ring buffer high watermark")
	lowWatermark := flag.Float64("low-watermark", 0.3, "ring buffer low watermark")
	flag.Parse()

	// Setup zap logger -> logr via zapr.
	zapLog, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("failed to create zap logger: %w", err)
	}
	defer func() { _ = zapLog.Sync() }()
	logger := zapr.NewLogger(zapLog)

	logger.Info("starting IPC Bus",
		"socketPath", *socketPath,
		"walDir", *walDir,
		"runtime", *runtime,
		"gatewayPort", *gatewayPort,
		"bufferSize", *bufferSize,
	)

	// Init WAL.
	wal, err := ipcbus.NewWAL(*walDir)
	if err != nil {
		return fmt.Errorf("failed to init WAL: %w", err)
	}

	// Init DLQ.
	dlqPath := filepath.Join(*walDir, "dlq.db")
	dlq, err := ipcbus.NewDLQ(dlqPath, 10000, 24*time.Hour)
	if err != nil {
		return fmt.Errorf("failed to init DLQ: %w", err)
	}

	// Init bridge.
	bridge, err := ipcbus.NewBridge(ipcbus.RuntimeType(*runtime), ipcbus.BridgeConfig{
		GatewayPort: *gatewayPort,
		SocketPath:  *runtimeSocket,
	})
	if err != nil {
		return fmt.Errorf("failed to create bridge: %w", err)
	}

	// Create router + server.
	router := ipcbus.NewRouter(ipcbus.RouterConfig{
		Bridge:        bridge,
		WAL:           wal,
		DLQ:           dlq,
		Logger:        logger.WithName("router"),
		BufferSize:    *bufferSize,
		HighWatermark: *highWatermark,
		LowWatermark:  *lowWatermark,
	})

	server := ipcbus.NewServer(*socketPath, router, logger.WithName("server"))

	// Context with signal handling.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig)
		shutdown(logger, router, wal, bridge, cancel)
	}()

	// Native sidecars can run before the runtime. Do not use a bridge whose
	// initial connection failed: some transports have no connection to read yet.
	if err := waitForRuntime(ctx, bridge.Connect, logger, time.Second); err != nil {
		return err
	}

	// Start outbound loop.
	go router.StartOutboundLoop(ctx)

	// Replay WAL.
	router.ReplayWAL(ctx)

	// Start compaction ticker (60s).
	go runCompactionTicker(ctx, logger.WithName("compaction"), wal)

	// Start DLQ purge ticker (1h).
	go runDLQPurgeTicker(ctx, logger.WithName("dlq-purge"), dlq)

	// Start UDS server (blocks).
	logger.Info("starting UDS server")
	return server.Start(ctx)
}

// Wait for the runtime without blocking Kubernetes from starting the next
// container. The native sidecar process is already Running at this point.
func waitForRuntime(ctx context.Context, connect func(context.Context) error, logger logr.Logger, interval time.Duration) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := connect(attempt)
		cancel()
		if err == nil {
			return nil
		}
		logger.Info("waiting for runtime bridge", "error", err.Error())
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func shutdown(logger logr.Logger, router *ipcbus.Router, wal *ipcbus.WAL, bridge ipcbus.RuntimeBridge, cancel context.CancelFunc) {
	// Send shutdown to sidecars.
	router.SendShutdown()

	// Wait 5s for sidecars to drain.
	logger.Info("waiting 5s for sidecars to drain")
	time.Sleep(5 * time.Second)

	// Flush WAL.
	if err := wal.Flush(); err != nil {
		logger.Error(err, "failed to flush WAL during shutdown")
	}

	// Close bridge.
	if err := bridge.Close(); err != nil {
		logger.Error(err, "failed to close bridge during shutdown")
	}

	// Cancel context to stop server + loops.
	cancel()
}

func runCompactionTicker(ctx context.Context, logger logr.Logger, wal *ipcbus.WAL) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if wal.NeedsCompaction() {
				logger.Info("running WAL compaction")
				if err := wal.Compact(); err != nil {
					logger.Error(err, "WAL compaction failed")
				}
			}
		}
	}
}

func runDLQPurgeTicker(ctx context.Context, logger logr.Logger, dlq *ipcbus.DLQ) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			purged, err := dlq.PurgeExpired()
			if err != nil {
				logger.Error(err, "DLQ purge failed")
			} else if purged > 0 {
				logger.Info("purged expired DLQ entries", "count", purged)
			}
		}
	}
}

func runShutdown() error {
	socketPath := envOr("IPC_SOCKET_PATH", "/var/run/claw/bus.sock")

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to connect to bus socket: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Register as __shutdown__.
	regPayload, _ := json.Marshal(map[string]string{"role": "shutdown"})
	regMsg := ipcbus.NewMessage(ipcbus.TypeRegister, "__shutdown__", regPayload)
	if err := ipcbus.WriteMessage(conn, regMsg); err != nil {
		return fmt.Errorf("failed to send register: %w", err)
	}

	// Read ACK.
	ack, err := ipcbus.ReadMessage(conn)
	if err != nil {
		return fmt.Errorf("failed to read ACK: %w", err)
	}
	if ack.Type != ipcbus.TypeAck {
		return fmt.Errorf("expected ACK, got %s", ack.Type)
	}

	// Send shutdown.
	shutdownMsg := ipcbus.NewMessage(ipcbus.TypeShutdown, "__shutdown__", nil)
	if err := ipcbus.WriteMessage(conn, shutdownMsg); err != nil {
		return fmt.Errorf("failed to send shutdown: %w", err)
	}

	fmt.Println("shutdown signal sent")
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
