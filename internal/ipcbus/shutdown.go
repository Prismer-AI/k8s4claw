package ipcbus

import (
	"context"
	"time"

	"github.com/go-logr/logr"
)

// ShutdownOrchestrator coordinates graceful shutdown of the IPC Bus,
// ensuring all sidecars are notified, the WAL is flushed, and the
// bridge is closed.
type ShutdownOrchestrator struct {
	router *Router
	wal    *WAL
	bridge RuntimeBridge
	logger logr.Logger
}

// NewShutdownOrchestrator creates a ShutdownOrchestrator with the given
// dependencies.
func NewShutdownOrchestrator(router *Router, wal *WAL, bridge RuntimeBridge, logger logr.Logger) *ShutdownOrchestrator {
	return &ShutdownOrchestrator{
		router: router,
		wal:    wal,
		bridge: bridge,
		logger: logger,
	}
}

// Execute runs the graceful shutdown sequence:
//  1. Send shutdown to all sidecars
//  2. Wait for drain (up to drainTimeout) — poll ConnectedCount every 100ms, exit early if 0
//  3. Flush WAL
//  4. Close bridge
func (s *ShutdownOrchestrator) Execute(ctx context.Context, drainTimeout time.Duration) {
	s.logger.Info("shutdown: sending shutdown to all sidecars")
	s.router.SendShutdown()

	// Wait for sidecars to drain.
	s.logger.Info("shutdown: waiting for sidecar drain", "timeout", drainTimeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	timer := time.NewTimer(drainTimeout)
	defer timer.Stop()

drain:
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("shutdown: context cancelled during drain")
			break drain
		case <-timer.C:
			s.logger.Info("shutdown: drain timeout reached", "connected", s.router.ConnectedCount())
			break drain
		case <-ticker.C:
			if s.router.ConnectedCount() == 0 {
				s.logger.Info("shutdown: all sidecars disconnected")
				break drain
			}
		}
	}

	// Flush WAL.
	if s.wal != nil {
		s.logger.Info("shutdown: flushing WAL")
		if err := s.wal.Flush(); err != nil {
			s.logger.Error(err, "shutdown: failed to flush WAL")
		}
	}

	// Close bridge.
	if s.bridge != nil {
		s.logger.Info("shutdown: closing bridge")
		if err := s.bridge.Close(); err != nil {
			s.logger.Error(err, "shutdown: failed to close bridge")
		}
	}

	s.logger.Info("shutdown: complete")
}
