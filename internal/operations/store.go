package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	StatusStored             = "STORED"
	StatusPendingPublication = "PENDING_PUBLICATION"
	StatusPublished          = "PUBLISHED"
	StatusProcessing         = "PROCESSING"
	StatusRetrying           = "RETRYING"
	StatusDelivered          = "DELIVERED"
	StatusDeadLettered       = "DEAD_LETTERED"
	StatusRedriven           = "REDRIVEN"

	DeliveryOutcomeSucceeded = "SUCCESS"
	DeliveryOutcomeFailed    = "FAILED"

	DLQStatusOpen     = "OPEN"
	DLQStatusRedriven = "REDRIVEN"
)

var (
	ErrEventNotFound = errors.New("event not found")
	ErrDLQNotFound   = errors.New("DLQ record not found")
	ErrDLQNotOpen    = errors.New("DLQ record is not open")
)

type Store struct {
	pool *pgxpool.Pool
}

type EventMetadata struct {
	EventID   string
	EventType string
	Source    string
	Payload   json.RawMessage
	CreatedAt time.Time
}

type StatusHistoryEntry struct {
	Status    string
	Details   json.RawMessage
	CreatedAt time.Time
}

type DeliveryAttempt struct {
	AttemptNumber int
	Outcome       string
	ResponseCode  *int
	Error         *string
	StartedAt     time.Time
	CompletedAt   time.Time
}

type EventStatus struct {
	Event            EventMetadata
	CurrentStatus    string
	History          []StatusHistoryEntry
	DeliveryAttempts []DeliveryAttempt
}

type DLQRecord struct {
	EventID          string
	EventType        string
	Source           string
	AttemptCount     int
	LastError        *string
	Status           string
	DeadLetteredAt   time.Time
	RedrivenAt       *time.Time
	OriginalStreamID string
}

type DLQDetail struct {
	Record           DLQRecord
	History          []StatusHistoryEntry
	DeliveryAttempts []DeliveryAttempt
}

type MetricsSummary struct {
	TotalEvents        int
	PendingPublication int
	Delivered          int
	Retrying           int
	OpenDLQ            int
	Redriven           int
}

type RedrivePublisher func(context.Context, map[string]interface{}) (string, error)

type RedriveResult struct {
	EventID  string
	Status   string
	StreamID string
}

type ProcessingRecord struct {
	EventID  string
	StreamID string
	Retry    int
}

type DeliveryAttemptRecord struct {
	EventID       string
	StreamID      string
	AttemptNumber int
	ResponseCode  *int
	Error         error
	StartedAt     time.Time
	CompletedAt   time.Time
}

type RetryingRecord struct {
	DeliveryAttemptRecord
	NextRetry   int
	ScheduledAt time.Time
}

type DeadLetterRecord struct {
	DeliveryAttemptRecord
	OriginalStreamID string
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) EventStatus(ctx context.Context, eventID string) (EventStatus, error) {
	if s == nil || s.pool == nil {
		return EventStatus{}, errors.New("operations store pool is required")
	}

	event, err := s.fetchEvent(ctx, s.pool, eventID)
	if err != nil {
		return EventStatus{}, err
	}
	history, err := s.fetchStatusHistory(ctx, s.pool, eventID)
	if err != nil {
		return EventStatus{}, err
	}
	attempts, err := s.fetchDeliveryAttempts(ctx, s.pool, eventID)
	if err != nil {
		return EventStatus{}, err
	}

	currentStatus := ""
	if len(history) > 0 {
		currentStatus = history[len(history)-1].Status
	}
	return EventStatus{
		Event:            event,
		CurrentStatus:    currentStatus,
		History:          history,
		DeliveryAttempts: attempts,
	}, nil
}

func (s *Store) ListDLQ(ctx context.Context, status string, limit int) ([]DLQRecord, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("operations store pool is required")
	}
	if status != DLQStatusOpen && status != DLQStatusRedriven {
		return nil, fmt.Errorf("unsupported DLQ status %q", status)
	}
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("DLQ limit must be between 1 and 200")
	}

	rows, err := s.pool.Query(ctx, `
		SELECT d.event_id::text, e.event_type, e.source, d.attempt_count, d.last_error,
		       d.status, d.dead_lettered_at, d.redriven_at, d.original_stream_id
		FROM dlq_records d
		JOIN events e ON e.id = d.event_id
		WHERE d.status = $1
		ORDER BY d.dead_lettered_at DESC, d.event_id DESC
		LIMIT $2`,
		status,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list DLQ records: %w", err)
	}
	defer rows.Close()

	var records []DLQRecord
	for rows.Next() {
		record, err := scanDLQRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read DLQ records: %w", err)
	}
	return records, nil
}

func (s *Store) DLQDetail(ctx context.Context, eventID string) (DLQDetail, error) {
	if s == nil || s.pool == nil {
		return DLQDetail{}, errors.New("operations store pool is required")
	}

	row := s.pool.QueryRow(ctx, `
		SELECT d.event_id::text, e.event_type, e.source, d.attempt_count, d.last_error,
		       d.status, d.dead_lettered_at, d.redriven_at, d.original_stream_id
		FROM dlq_records d
		JOIN events e ON e.id = d.event_id
		WHERE d.event_id = $1::uuid`,
		eventID,
	)
	record, err := scanDLQRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return DLQDetail{}, ErrDLQNotFound
	}
	if err != nil {
		return DLQDetail{}, err
	}

	history, err := s.fetchStatusHistory(ctx, s.pool, eventID)
	if err != nil {
		return DLQDetail{}, err
	}
	attempts, err := s.fetchDeliveryAttempts(ctx, s.pool, eventID)
	if err != nil {
		return DLQDetail{}, err
	}
	return DLQDetail{
		Record:           record,
		History:          history,
		DeliveryAttempts: attempts,
	}, nil
}

func (s *Store) MetricsSummary(ctx context.Context) (MetricsSummary, error) {
	if s == nil || s.pool == nil {
		return MetricsSummary{}, errors.New("operations store pool is required")
	}

	var summary MetricsSummary
	if err := s.pool.QueryRow(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (event_id) event_id, status
			FROM event_status_history
			ORDER BY event_id, created_at DESC, id DESC
		)
		SELECT
			(SELECT count(*) FROM events),
			(SELECT count(*) FROM outbox WHERE status = 'pending'),
			(SELECT count(*) FROM latest WHERE status = $1),
			(SELECT count(*) FROM latest WHERE status = $2),
			(SELECT count(*) FROM dlq_records WHERE status = $3),
			(SELECT count(*) FROM dlq_records WHERE status = $4)`,
		StatusDelivered,
		StatusRetrying,
		DLQStatusOpen,
		DLQStatusRedriven,
	).Scan(
		&summary.TotalEvents,
		&summary.PendingPublication,
		&summary.Delivered,
		&summary.Retrying,
		&summary.OpenDLQ,
		&summary.Redriven,
	); err != nil {
		return MetricsSummary{}, fmt.Errorf("read metrics summary: %w", err)
	}
	return summary, nil
}

func (s *Store) RedriveDLQ(ctx context.Context, eventID string, publish RedrivePublisher) (RedriveResult, error) {
	if s == nil || s.pool == nil {
		return RedriveResult{}, errors.New("operations store pool is required")
	}
	if publish == nil {
		return RedriveResult{}, errors.New("redrive publisher is required")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RedriveResult{}, fmt.Errorf("begin redrive transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var dlqStatus string
	if err := tx.QueryRow(ctx, `
		SELECT status
		FROM dlq_records
		WHERE event_id = $1::uuid
		FOR UPDATE`,
		eventID,
	).Scan(&dlqStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RedriveResult{}, ErrDLQNotFound
		}
		return RedriveResult{}, fmt.Errorf("lock DLQ record for event %s: %w", eventID, err)
	}
	if dlqStatus != DLQStatusOpen {
		return RedriveResult{}, ErrDLQNotOpen
	}

	event, err := s.fetchEvent(ctx, tx, eventID)
	if err != nil {
		return RedriveResult{}, err
	}
	streamID, err := publish(ctx, map[string]interface{}{
		"event_id":   event.EventID,
		"event_type": event.EventType,
		"source":     event.Source,
		"payload":    string(event.Payload),
		"retry":      "0",
		"created_at": event.CreatedAt.UTC().Format(time.RFC3339),
		"redrive":    "1",
	})
	if err != nil {
		return RedriveResult{}, fmt.Errorf("publish redrive event %s: %w", eventID, err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE dlq_records
		SET status = $2,
		    redriven_at = now(),
		    updated_at = now()
		WHERE event_id = $1::uuid`,
		eventID,
		DLQStatusRedriven,
	)
	if err != nil {
		return RedriveResult{}, fmt.Errorf("mark DLQ record redriven for event %s: %w", eventID, err)
	}
	if tag.RowsAffected() != 1 {
		return RedriveResult{}, fmt.Errorf("mark DLQ record redriven for event %s affected %d rows", eventID, tag.RowsAffected())
	}
	if err := InsertStatusHistory(ctx, tx, eventID, StatusRedriven, map[string]any{"stream_id": streamID}); err != nil {
		return RedriveResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RedriveResult{}, fmt.Errorf("commit redrive event %s: %w", eventID, err)
	}

	return RedriveResult{
		EventID:  eventID,
		Status:   DLQStatusRedriven,
		StreamID: streamID,
	}, nil
}

func InsertStatusHistory(ctx context.Context, tx pgx.Tx, eventID string, status string, details map[string]any) error {
	detailsJSON, err := marshalDetails(details)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(
		ctx,
		`INSERT INTO event_status_history (event_id, status, details)
		 VALUES ($1::uuid, $2, $3::jsonb)`,
		eventID,
		status,
		detailsJSON,
	); err != nil {
		return fmt.Errorf("insert status %s for event %s: %w", status, eventID, err)
	}
	return nil
}

func (s *Store) RecordProcessing(ctx context.Context, record ProcessingRecord) error {
	if s == nil || s.pool == nil {
		return errors.New("operations store pool is required")
	}

	return s.inTransaction(ctx, func(tx pgx.Tx) error {
		return InsertStatusHistory(ctx, tx, record.EventID, StatusProcessing, map[string]any{
			"stream_id": record.StreamID,
			"retry":     record.Retry,
		})
	})
}

func (s *Store) RecordDelivered(ctx context.Context, record DeliveryAttemptRecord) error {
	if s == nil || s.pool == nil {
		return errors.New("operations store pool is required")
	}

	return s.inTransaction(ctx, func(tx pgx.Tx) error {
		if err := insertDeliveryAttempt(ctx, tx, record, DeliveryOutcomeSucceeded); err != nil {
			return err
		}
		return InsertStatusHistory(ctx, tx, record.EventID, StatusDelivered, map[string]any{
			"stream_id": record.StreamID,
			"retry":     record.AttemptNumber - 1,
		})
	})
}

func (s *Store) RecordRetrying(ctx context.Context, record RetryingRecord) error {
	if s == nil || s.pool == nil {
		return errors.New("operations store pool is required")
	}

	return s.inTransaction(ctx, func(tx pgx.Tx) error {
		if err := insertDeliveryAttempt(ctx, tx, record.DeliveryAttemptRecord, DeliveryOutcomeFailed); err != nil {
			return err
		}
		return InsertStatusHistory(ctx, tx, record.EventID, StatusRetrying, map[string]any{
			"stream_id":    record.StreamID,
			"retry":        record.AttemptNumber - 1,
			"next_retry":   record.NextRetry,
			"scheduled_at": record.ScheduledAt.UTC().Format(time.RFC3339Nano),
		})
	})
}

func (s *Store) RecordDeadLettered(ctx context.Context, record DeadLetterRecord) error {
	if s == nil || s.pool == nil {
		return errors.New("operations store pool is required")
	}

	originalStreamID := strings.TrimSpace(record.OriginalStreamID)
	if originalStreamID == "" {
		originalStreamID = record.StreamID
	}

	return s.inTransaction(ctx, func(tx pgx.Tx) error {
		if err := insertDeliveryAttempt(ctx, tx, record.DeliveryAttemptRecord, DeliveryOutcomeFailed); err != nil {
			return err
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO dlq_records (
				event_id,
				original_stream_id,
				attempt_count,
				last_error,
				status,
				dead_lettered_at,
				redriven_at,
				updated_at
			)
			VALUES ($1::uuid, $2, $3, $4, $5, now(), NULL, now())
			ON CONFLICT (event_id)
			DO UPDATE SET
				original_stream_id = EXCLUDED.original_stream_id,
				attempt_count = EXCLUDED.attempt_count,
				last_error = EXCLUDED.last_error,
				status = EXCLUDED.status,
				dead_lettered_at = now(),
				redriven_at = NULL,
				updated_at = now()`,
			record.EventID,
			originalStreamID,
			record.AttemptNumber,
			errorString(record.Error),
			DLQStatusOpen,
		); err != nil {
			return fmt.Errorf("upsert DLQ record for event %s: %w", record.EventID, err)
		}
		return InsertStatusHistory(ctx, tx, record.EventID, StatusDeadLettered, map[string]any{
			"stream_id": record.StreamID,
			"retry":     record.AttemptNumber - 1,
		})
	})
}

func (s *Store) inTransaction(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin operations transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit operations transaction: %w", err)
	}
	return nil
}

func insertDeliveryAttempt(ctx context.Context, tx pgx.Tx, record DeliveryAttemptRecord, outcome string) error {
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO delivery_attempts (
			event_id,
			attempt_number,
			outcome,
			response_code,
			error,
			started_at,
			completed_at
		)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)`,
		record.EventID,
		record.AttemptNumber,
		outcome,
		record.ResponseCode,
		errorString(record.Error),
		record.StartedAt,
		record.CompletedAt,
	); err != nil {
		return fmt.Errorf("insert delivery attempt for event %s: %w", record.EventID, err)
	}
	return nil
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type rowScanner interface {
	Scan(...any) error
}

func (s *Store) fetchEvent(ctx context.Context, q queryer, eventID string) (EventMetadata, error) {
	var event EventMetadata
	var payload []byte
	if err := q.QueryRow(ctx, `
		SELECT id::text, event_type, source, payload, created_at
		FROM events
		WHERE id = $1::uuid`,
		eventID,
	).Scan(&event.EventID, &event.EventType, &event.Source, &payload, &event.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return EventMetadata{}, ErrEventNotFound
		}
		return EventMetadata{}, fmt.Errorf("fetch event %s: %w", eventID, err)
	}
	event.Payload = append(json.RawMessage(nil), payload...)
	return event, nil
}

func (s *Store) fetchStatusHistory(ctx context.Context, q queryer, eventID string) ([]StatusHistoryEntry, error) {
	rows, err := q.Query(ctx, `
		SELECT status, details, created_at
		FROM event_status_history
		WHERE event_id = $1::uuid
		ORDER BY created_at ASC, id ASC`,
		eventID,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch status history for event %s: %w", eventID, err)
	}
	defer rows.Close()

	var history []StatusHistoryEntry
	for rows.Next() {
		var entry StatusHistoryEntry
		var details []byte
		if err := rows.Scan(&entry.Status, &details, &entry.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan status history for event %s: %w", eventID, err)
		}
		entry.Details = append(json.RawMessage(nil), details...)
		history = append(history, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read status history for event %s: %w", eventID, err)
	}
	return history, nil
}

func (s *Store) fetchDeliveryAttempts(ctx context.Context, q queryer, eventID string) ([]DeliveryAttempt, error) {
	rows, err := q.Query(ctx, `
		SELECT attempt_number, outcome, response_code, error, started_at, completed_at
		FROM delivery_attempts
		WHERE event_id = $1::uuid
		ORDER BY started_at ASC, id ASC`,
		eventID,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch delivery attempts for event %s: %w", eventID, err)
	}
	defer rows.Close()

	var attempts []DeliveryAttempt
	for rows.Next() {
		var attempt DeliveryAttempt
		var responseCode sql.NullInt32
		var message sql.NullString
		if err := rows.Scan(
			&attempt.AttemptNumber,
			&attempt.Outcome,
			&responseCode,
			&message,
			&attempt.StartedAt,
			&attempt.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan delivery attempt for event %s: %w", eventID, err)
		}
		if responseCode.Valid {
			code := int(responseCode.Int32)
			attempt.ResponseCode = &code
		}
		if message.Valid {
			attempt.Error = &message.String
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read delivery attempts for event %s: %w", eventID, err)
	}
	return attempts, nil
}

func scanDLQRecord(row rowScanner) (DLQRecord, error) {
	var record DLQRecord
	var lastError sql.NullString
	var redrivenAt sql.NullTime
	if err := row.Scan(
		&record.EventID,
		&record.EventType,
		&record.Source,
		&record.AttemptCount,
		&lastError,
		&record.Status,
		&record.DeadLetteredAt,
		&redrivenAt,
		&record.OriginalStreamID,
	); err != nil {
		return DLQRecord{}, err
	}
	if lastError.Valid {
		record.LastError = &lastError.String
	}
	if redrivenAt.Valid {
		record.RedrivenAt = &redrivenAt.Time
	}
	return record, nil
}

func marshalDetails(details map[string]any) (string, error) {
	if details == nil {
		details = map[string]any{}
	}

	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return "", fmt.Errorf("marshal status details: %w", err)
	}
	return string(detailsJSON), nil
}

func errorString(err error) *string {
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return nil
	}
	return &message
}
