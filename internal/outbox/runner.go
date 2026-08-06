package outbox

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type PublishNextFunc func(ctx context.Context) (PublishNextOutboxResult, error)

type Runner struct {
	publishNext  PublishNextFunc
	pollInterval time.Duration
	onError      func(error)
}

func NewRunner(
	publishNext PublishNextFunc,
	pollInterval time.Duration,
	onError func(error),
) (*Runner, error) {
	if publishNext == nil {
		return nil, errors.New("outbox runner publish function is required")
	}
	if pollInterval <= 0 {
		return nil, fmt.Errorf("outbox runner poll interval must be positive")
	}

	return &Runner{
		publishNext:  publishNext,
		pollInterval: pollInterval,
		onError:      onError,
	}, nil
}

func (r *Runner) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		result, err := r.publishNext(ctx)
		if ctx.Err() != nil {
			return nil
		}

		if errors.Is(err, ErrOutboxPublishFailed) {
			r.reportError(err)
			continue
		}
		if err != nil {
			r.reportError(err)
			if !waitForContext(ctx, r.pollInterval) {
				return nil
			}
			continue
		}
		if result.Found {
			continue
		}
		if !waitForContext(ctx, r.pollInterval) {
			return nil
		}
	}
}

func (r *Runner) reportError(err error) {
	if r.onError != nil {
		r.onError(err)
	}
}

func waitForContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
