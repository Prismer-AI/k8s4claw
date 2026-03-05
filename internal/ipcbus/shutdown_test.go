package ipcbus

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func TestShutdownOrchestrator_Execute(t *testing.T) {
	// No connected sidecars -> should complete quickly (< 500ms).
	dir := t.TempDir()
	wal, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("failed to create WAL: %v", err)
	}
	defer wal.Close()

	router := NewRouter(RouterConfig{WAL: wal, Logger: logr.Discard()})
	orchestrator := NewShutdownOrchestrator(router, wal, nil, logr.Discard())

	start := time.Now()
	orchestrator.Execute(context.Background(), 1*time.Second)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("shutdown took too long: %v", elapsed)
	}
}
