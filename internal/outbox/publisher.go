package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krishgondaliya/eventrail-ingestion/internal/operations"
)

const maxStoredOutboxErrorLength = 1000

var ErrOutboxPublishFailed = errors.New("outbox publication failed")

type OutboxEvent struct {
	OutboxID     string
	EventID      string
	EventType    string
	Source       string
	Payload      json.RawMessage
	CreatedAt    time.Time
	AttemptCount int
}

type PublishOutboxEventFunc func(
	ctx context.Context,
	event OutboxEvent,
) error

type PublishNextOutboxResult struct {
	Found     bool
	Published bool
	OutboxID  string
	EventID   string
}

const selectNextPendingOutboxSQL = `
	SELECT
		o.id::text,
		e.id::text,
		e.event_type,
		e.source,
		e.payload,
		e.created_at,
		o.attempt_count
	FROM outbox o
	JOIN events e ON e.id = o.event_id
	WHERE o.status = 'pending'
		AND o.next_attempt_at <= now()
	ORDER BY o.next_attempt_at ASC, o.created_at ASC
	FOR UPDATE OF o SKIP LOCKED
	LIMIT 1`

const markOutboxPublishedSQL = `
	UPDATE outbox
	SET status = 'published',
		published_at = now(),
		last_error = NULL,
		updated_at = now()
	WHERE id = $1::uuid`

const rescheduleOutboxAfterFailureSQL = `
	UPDATE outbox
	SET status = 'pending',
		attempt_count = attempt_count + 1,
		next_attempt_at = now() + ($2::bigint * interval '1 microsecond'),
		last_error = $3,
		updated_at = now(),
		published_at = NULL
	WHERE id = $1::uuid`

func PublishNextOutboxEvent(
	ctx context.Context,
	pool *pgxpool.Pool,
	publish PublishOutboxEventFunc,
	baseBackoff time.Duration,
	maxBackoff time.Duration,
) (PublishNextOutboxResult, error) {
	if err := validatePublishNextOutboxConfig(pool, publish, baseBackoff, maxBackoff); err != nil {
		return PublishNextOutboxResult{}, err
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PublishNextOutboxResult{}, fmt.Errorf("begin publish outbox transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	event, err := claimNextOutboxEvent(ctx, tx)
	if errors.Is(err, pgx.ErrNoRows) {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
			return PublishNextOutboxResult{}, fmt.Errorf("rollback empty outbox transaction: %w", rollbackErr)
		}
		return PublishNextOutboxResult{Found: false}, nil
	}
	if err != nil {
		return PublishNextOutboxResult{}, err
	}

	result := PublishNextOutboxResult{
		Found:    true,
		OutboxID: event.OutboxID,
		EventID:  event.EventID,
	}

	if err := publish(ctx, event); err != nil {
		attemptNumber := event.AttemptCount + 1
		delay := outboxBackoff(baseBackoff, maxBackoff, attemptNumber)
		if err := rescheduleOutboxAfterFailure(ctx, tx, event.OutboxID, delay, sanitizeOutboxError(err)); err != nil {
			return PublishNextOutboxResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return PublishNextOutboxResult{}, fmt.Errorf("commit outbox failure state for outbox %s event %s: %w", event.OutboxID, event.EventID, err)
		}
		return result, fmt.Errorf("%w: outbox %s event %s attempt %d", ErrOutboxPublishFailed, event.OutboxID, event.EventID, attemptNumber)
	}

	if err := markOutboxPublished(ctx, tx, event.OutboxID); err != nil {
		return PublishNextOutboxResult{}, err
	}
	if err := operations.InsertStatusHistory(ctx, tx, event.EventID, operations.StatusPublished, nil); err != nil {
		return PublishNextOutboxResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PublishNextOutboxResult{}, fmt.Errorf("commit published outbox %s event %s: %w", event.OutboxID, event.EventID, err)
	}

	result.Published = true
	return result, nil
}

func validatePublishNextOutboxConfig(
	pool *pgxpool.Pool,
	publish PublishOutboxEventFunc,
	baseBackoff time.Duration,
	maxBackoff time.Duration,
) error {
	if pool == nil {
		return errors.New("outbox publisher pool is required")
	}
	if publish == nil {
		return errors.New("outbox publisher function is required")
	}
	if baseBackoff <= 0 {
		return errors.New("outbox publisher base backoff must be positive")
	}
	if maxBackoff < baseBackoff {
		return errors.New("outbox publisher max backoff must be at least base backoff")
	}
	return nil
}

func claimNextOutboxEvent(ctx context.Context, tx pgx.Tx) (OutboxEvent, error) {
	var event OutboxEvent
	var payload []byte

	if err := tx.QueryRow(ctx, selectNextPendingOutboxSQL).Scan(
		&event.OutboxID,
		&event.EventID,
		&event.EventType,
		&event.Source,
		&payload,
		&event.CreatedAt,
		&event.AttemptCount,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OutboxEvent{}, err
		}
		return OutboxEvent{}, fmt.Errorf("claim next pending outbox event: %w", err)
	}

	event.Payload = append(json.RawMessage(nil), payload...)
	return event, nil
}

func markOutboxPublished(ctx context.Context, tx pgx.Tx, outboxID string) error {
	tag, err := tx.Exec(ctx, markOutboxPublishedSQL, outboxID)
	if err != nil {
		return fmt.Errorf("mark outbox %s published: %w", outboxID, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("mark outbox %s published affected %d rows", outboxID, tag.RowsAffected())
	}
	return nil
}

func rescheduleOutboxAfterFailure(
	ctx context.Context,
	tx pgx.Tx,
	outboxID string,
	delay time.Duration,
	storedError string,
) error {
	tag, err := tx.Exec(ctx, rescheduleOutboxAfterFailureSQL, outboxID, delay.Microseconds(), storedError)
	if err != nil {
		return fmt.Errorf("reschedule outbox %s after publication failure: %w", outboxID, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("reschedule outbox %s affected %d rows", outboxID, tag.RowsAffected())
	}
	return nil
}

func sanitizeOutboxError(err error) string {
	message := ""
	if err != nil {
		message = strings.TrimSpace(err.Error())
	}
	if message == "" {
		message = "publication failed"
	}

	runes := []rune(message)
	if len(runes) > maxStoredOutboxErrorLength {
		return string(runes[:maxStoredOutboxErrorLength])
	}
	return message
}

func outboxBackoff(base time.Duration, max time.Duration, attempt int) time.Duration {
	if attempt <= 1 {
		return base
	}

	delay := base
	for i := 1; i < attempt; i++ {
		if delay >= max {
			return max
		}
		if delay > max/2 {
			return max
		}
		delay *= 2
	}
	if delay > max {
		return max
	}
	return delay
}
