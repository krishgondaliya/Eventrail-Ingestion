package deliveryworker

import (
	"context"
	"testing"
	"time"
)

func TestWorkerRunExitsAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	worker := New(nil, Config{})
	done := make(chan error, 1)
	go func() {
		done <- worker.Run(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil cancellation error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for worker Run to exit")
	}
}

func TestRetryPumpExitsAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	worker := New(nil, Config{})
	done := make(chan error, 1)
	go func() {
		done <- worker.retryPump(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil cancellation error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retry pump to exit")
	}
}
