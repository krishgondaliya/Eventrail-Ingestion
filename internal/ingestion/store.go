package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrIdempotencyConflict = errors.New("idempotency key conflict")

type EventInput struct {
	EventType string
	Source    string
	Payload   json.RawMessage
}

type PersistResult struct {
	EventID string
	Created bool
}

const insertEventWithoutIdempotencyKeySQL = `
	INSERT INTO events (
		event_type,
		source,
		payload,
		idempotency_key,
		request_hash
	)
	VALUES ($1, $2, $3, NULL, $4)
	RETURNING id`

const insertEventWithIdempotencyKeySQL = `
	INSERT INTO events (
		event_type,
		source,
		payload,
		idempotency_key,
		request_hash
	)
	VALUES ($1, $2, $3, $4, $5)
	ON CONFLICT (source, idempotency_key)
	WHERE idempotency_key IS NOT NULL
	DO NOTHING
	RETURNING id`

const selectEventByIdempotencyKeySQL = `
	SELECT id, COALESCE(request_hash, ''), request_hash IS NOT NULL
	FROM events
	WHERE source = $1 AND idempotency_key = $2`

const insertOutboxSQL = `
	INSERT INTO outbox (event_id)
	VALUES ($1)`

func PersistEventWithOutbox(
	ctx context.Context,
	pool *pgxpool.Pool,
	req EventInput,
	idempotencyKey string,
) (PersistResult, error) {
	normalizedKey := strings.TrimSpace(idempotencyKey)

	requestHash, err := computeRequestHash(req.EventType, req.Source, req.Payload)
	if err != nil {
		return PersistResult{}, fmt.Errorf("compute request hash: %w", err)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PersistResult{}, fmt.Errorf("begin persist event transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if normalizedKey == "" {
		result, err := insertEventWithoutKeyWithOutbox(ctx, tx, req, requestHash)
		if err != nil {
			return PersistResult{}, err
		}

		if err := tx.Commit(ctx); err != nil {
			return PersistResult{}, fmt.Errorf("commit new event without idempotency key: %w", err)
		}
		return result, nil
	}

	var eventID string
	err = tx.QueryRow(
		ctx,
		insertEventWithIdempotencyKeySQL,
		req.EventType,
		req.Source,
		req.Payload,
		normalizedKey,
		requestHash,
	).Scan(&eventID)

	if err == nil {
		if err := insertOutboxForEvent(ctx, tx, eventID); err != nil {
			return PersistResult{}, err
		}

		if err := tx.Commit(ctx); err != nil {
			return PersistResult{}, fmt.Errorf("commit new event with idempotency key: %w", err)
		}
		return PersistResult{EventID: eventID, Created: true}, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return PersistResult{}, fmt.Errorf("insert event with idempotency key: %w", err)
	}

	eventID, err = existingEventIDForMatchingRequestHash(ctx, tx, req.Source, normalizedKey, requestHash)
	if err != nil {
		return PersistResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return PersistResult{}, fmt.Errorf("commit existing idempotent event lookup: %w", err)
	}
	return PersistResult{EventID: eventID, Created: false}, nil
}

func insertEventWithoutKeyWithOutbox(
	ctx context.Context,
	tx pgx.Tx,
	req EventInput,
	requestHash string,
) (PersistResult, error) {
	var eventID string

	if err := tx.QueryRow(
		ctx,
		insertEventWithoutIdempotencyKeySQL,
		req.EventType,
		req.Source,
		req.Payload,
		requestHash,
	).Scan(&eventID); err != nil {
		return PersistResult{}, fmt.Errorf("insert event without idempotency key: %w", err)
	}

	if err := insertOutboxForEvent(ctx, tx, eventID); err != nil {
		return PersistResult{}, err
	}

	return PersistResult{EventID: eventID, Created: true}, nil
}

func existingEventIDForMatchingRequestHash(
	ctx context.Context,
	tx pgx.Tx,
	source string,
	idempotencyKey string,
	incomingHash string,
) (string, error) {
	var eventID string
	var existingHashValue string
	var existingHashValid bool

	if err := tx.QueryRow(
		ctx,
		selectEventByIdempotencyKeySQL,
		source,
		idempotencyKey,
	).Scan(&eventID, &existingHashValue, &existingHashValid); err != nil {
		return "", fmt.Errorf("lookup existing event for idempotency key: %w", err)
	}

	var existingHash *string
	if existingHashValid {
		existingHash = &existingHashValue
	}

	if err := validateExistingRequestHash(existingHash, incomingHash); err != nil {
		return "", fmt.Errorf("%w for source %q event %s", err, source, eventID)
	}

	return eventID, nil
}

func insertOutboxForEvent(ctx context.Context, tx pgx.Tx, eventID string) error {
	if _, err := tx.Exec(ctx, insertOutboxSQL, eventID); err != nil {
		return fmt.Errorf("insert outbox row for event %s: %w", eventID, err)
	}
	return nil
}

func validateExistingRequestHash(existing *string, incoming string) error {
	if existing == nil {
		return fmt.Errorf("%w: existing request hash is missing", ErrIdempotencyConflict)
	}

	if *existing != incoming {
		return fmt.Errorf("%w: existing request hash differs", ErrIdempotencyConflict)
	}

	return nil
}
