package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func TestWaitForRuntimeRetriesBeforeUse(t *testing.T) {
	attempts := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := waitForRuntime(ctx, func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("runtime not listening")
		}
		return nil
	}, logr.Discard(), time.Millisecond)
	if err != nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}
func TestWaitForRuntimeCancelsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := waitForRuntime(ctx, func(context.Context) error { cancel(); return errors.New("not ready") }, logr.Discard(), time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
func TestWaitForRuntimeCancelsDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := waitForRuntime(ctx, func(attempt context.Context) error { cancel(); <-attempt.Done(); return attempt.Err() }, logr.Discard(), time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
