package outbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewRunnerValidation(t *testing.T) {
	validPublishNext := func(ctx context.Context) (PublishNextOutboxResult, error) {
		return PublishNextOutboxResult{}, nil
	}

	tests := []struct {
		name          string
		publishNext   PublishNextFunc
		pollInterval  time.Duration
		onError       func(error)
		wantErr       bool
		wantErrSubstr string
	}{
		{
			name:          "nil publish function",
			publishNext:   nil,
			pollInterval:  time.Millisecond,
			wantErr:       true,
			wantErrSubstr: "publish function",
		},
		{
			name:          "zero interval",
			publishNext:   validPublishNext,
			pollInterval:  0,
			wantErr:       true,
			wantErrSubstr: "poll interval",
		},
		{
			name:          "negative interval",
			publishNext:   validPublishNext,
			pollInterval:  -time.Millisecond,
			wantErr:       true,
			wantErrSubstr: "poll interval",
		},
		{
			name:         "nil error callback is allowed",
			publishNext:  validPublishNext,
			pollInterval: time.Millisecond,
			onError:      nil,
		},
		{
			name:         "valid arguments",
			publishNext:  validPublishNext,
			pollInterval: time.Millisecond,
			onError:      func(error) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, err := NewRunner(tt.publishNext, tt.pollInterval, tt.onError)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrSubstr != "" && !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Fatalf("expected error containing %q, got %q", tt.wantErrSubstr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
			if runner == nil {
				t.Fatal("expected runner")
			}
		})
	}
}

func TestRunnerDrainsAvailableWorkImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results := []PublishNextOutboxResult{
		{Found: true, Published: true},
		{Found: true, Published: true},
		{Found: true, Published: true},
		{Found: false},
	}

	var calls int
	publishNext := func(ctx context.Context) (PublishNextOutboxResult, error) {
		defer func() { calls++ }()
		if calls >= len(results) {
			t.Fatalf("unexpected publish call %d", calls+1)
		}
		if calls == len(results)-1 {
			cancel()
		}
		return results[calls], nil
	}

	runner := mustNewRunner(t, publishNext, 100*time.Millisecond, nil)
	start := time.Now()
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if calls != len(results) {
		t.Fatalf("expected %d calls, got %d", len(results), calls)
	}
	if elapsed := time.Since(start); elapsed >= 100*time.Millisecond {
		t.Fatalf("expected available work to drain before poll interval, elapsed %s", elapsed)
	}
}

func TestRunnerNoWorkWaitsRatherThanHotLoops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const pollInterval = 30 * time.Millisecond
	start := time.Now()
	var calls int

	publishNext := func(ctx context.Context) (PublishNextOutboxResult, error) {
		calls++
		if calls == 2 {
			cancel()
		}
		return PublishNextOutboxResult{Found: false}, nil
	}

	runner := mustNewRunner(t, publishNext, pollInterval, nil)
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if calls != 2 {
		t.Fatalf("expected two calls, got %d", calls)
	}
	if elapsed := time.Since(start); elapsed < pollInterval {
		t.Fatalf("expected poll interval wait before second call, elapsed %s", elapsed)
	}
}

func TestRunnerRedisPublicationFailureContinues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	publicationErr := fmt.Errorf("redis unavailable: %w", ErrOutboxPublishFailed)
	results := []struct {
		result PublishNextOutboxResult
		err    error
	}{
		{
			result: PublishNextOutboxResult{Found: true, Published: false},
			err:    publicationErr,
		},
		{
			result: PublishNextOutboxResult{Found: true, Published: true},
			err:    nil,
		},
	}

	var calls int
	var gotErrors []error
	publishNext := func(ctx context.Context) (PublishNextOutboxResult, error) {
		defer func() { calls++ }()
		if calls >= len(results) {
			t.Fatalf("unexpected publish call %d", calls+1)
		}
		if calls == len(results)-1 {
			cancel()
		}
		return results[calls].result, results[calls].err
	}

	runner := mustNewRunner(t, publishNext, time.Second, func(err error) {
		gotErrors = append(gotErrors, err)
	})
	start := time.Now()
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if calls != 2 {
		t.Fatalf("expected runner to continue to one later row, got %d calls", calls)
	}
	if len(gotErrors) != 1 || !errors.Is(gotErrors[0], ErrOutboxPublishFailed) {
		t.Fatalf("expected one publication failure callback, got %#v", gotErrors)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("expected durable publication failure to continue without polling delay, elapsed %s", elapsed)
	}
}

func TestRunnerUnexpectedErrorWaitsAndContinues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const pollInterval = 30 * time.Millisecond
	databaseErr := errors.New("database temporarily unavailable")
	var calls int
	var gotErrors []error
	start := time.Now()

	publishNext := func(ctx context.Context) (PublishNextOutboxResult, error) {
		calls++
		if calls == 1 {
			return PublishNextOutboxResult{}, databaseErr
		}
		cancel()
		return PublishNextOutboxResult{Found: false}, nil
	}

	runner := mustNewRunner(t, publishNext, pollInterval, func(err error) {
		gotErrors = append(gotErrors, err)
	})
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if calls != 2 {
		t.Fatalf("expected runner to continue after error, got %d calls", calls)
	}
	if len(gotErrors) != 1 || !errors.Is(gotErrors[0], databaseErr) {
		t.Fatalf("expected database error callback, got %#v", gotErrors)
	}
	if elapsed := time.Since(start); elapsed < pollInterval {
		t.Fatalf("expected error path to wait before continuing, elapsed %s", elapsed)
	}
}

func TestRunnerCancelledBeforeRunDoesNotPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var calls int
	runner := mustNewRunner(t, func(ctx context.Context) (PublishNextOutboxResult, error) {
		calls++
		return PublishNextOutboxResult{Found: true, Published: true}, nil
	}, time.Millisecond, nil)

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected zero publish calls, got %d", calls)
	}
}

func TestRunnerCancellationWhileWaitingReturnsNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startedWait := make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once

	runner := mustNewRunner(t, func(ctx context.Context) (PublishNextOutboxResult, error) {
		once.Do(func() { close(startedWait) })
		return PublishNextOutboxResult{Found: false}, nil
	}, time.Hour, nil)

	go func() {
		done <- runner.Run(ctx)
	}()

	<-startedWait
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil cancellation result, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runner cancellation")
	}
}

func mustNewRunner(t *testing.T, publishNext PublishNextFunc, pollInterval time.Duration, onError func(error)) *Runner {
	t.Helper()

	runner, err := NewRunner(publishNext, pollInterval, onError)
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	return runner
}
