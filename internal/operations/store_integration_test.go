package operations_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krishgondaliya/eventrail-ingestion/internal/ingestion"
	"github.com/krishgondaliya/eventrail-ingestion/internal/operations"
	"github.com/krishgondaliya/eventrail-ingestion/internal/testutil"
)

func TestStoreIntegrationRecordsDeliveryTransitions(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	store := operations.NewStore(pool)
	ctx := context.Background()
	eventID := persistOperationalEvent(t, pool)
	responseCode := 200
	startedAt := time.Now().UTC()
	completedAt := startedAt.Add(25 * time.Millisecond)

	if err := store.RecordProcessing(ctx, operations.ProcessingRecord{
		EventID:  eventID,
		StreamID: "1-0",
		Retry:    0,
	}); err != nil {
		t.Fatalf("RecordProcessing returned error: %v", err)
	}
	if err := store.RecordDelivered(ctx, operations.DeliveryAttemptRecord{
		EventID:       eventID,
		StreamID:      "1-0",
		AttemptNumber: 1,
		ResponseCode:  &responseCode,
		StartedAt:     startedAt,
		CompletedAt:   completedAt,
	}); err != nil {
		t.Fatalf("RecordDelivered returned error: %v", err)
	}

	assertOperationStatusCount(t, pool, eventID, operations.StatusProcessing, 1)
	assertOperationStatusCount(t, pool, eventID, operations.StatusDelivered, 1)
	assertDeliveryAttempt(t, pool, eventID, 1, operations.DeliveryOutcomeSucceeded, sql.NullInt32{Int32: 200, Valid: true})
}

func TestStoreIntegrationRecordsRetrying(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	store := operations.NewStore(pool)
	ctx := context.Background()
	eventID := persistOperationalEvent(t, pool)
	responseCode := 503
	startedAt := time.Now().UTC()

	if err := store.RecordRetrying(ctx, operations.RetryingRecord{
		DeliveryAttemptRecord: operations.DeliveryAttemptRecord{
			EventID:       eventID,
			StreamID:      "2-0",
			AttemptNumber: 2,
			ResponseCode:  &responseCode,
			Error:         assertError("503 Service Unavailable"),
			StartedAt:     startedAt,
			CompletedAt:   startedAt.Add(10 * time.Millisecond),
		},
		NextRetry:   3,
		ScheduledAt: startedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("RecordRetrying returned error: %v", err)
	}

	assertOperationStatusCount(t, pool, eventID, operations.StatusRetrying, 1)
	assertDeliveryAttempt(t, pool, eventID, 2, operations.DeliveryOutcomeFailed, sql.NullInt32{Int32: 503, Valid: true})
}

func TestStoreIntegrationRepeatedDLQRecordingUpsertsCurrentRecord(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	store := operations.NewStore(pool)
	ctx := context.Background()
	eventID := persistOperationalEvent(t, pool)
	startedAt := time.Now().UTC()

	first := operations.DeadLetterRecord{
		DeliveryAttemptRecord: operations.DeliveryAttemptRecord{
			EventID:       eventID,
			StreamID:      "3-0",
			AttemptNumber: 1,
			Error:         assertError("bad request"),
			StartedAt:     startedAt,
			CompletedAt:   startedAt.Add(10 * time.Millisecond),
		},
		OriginalStreamID: "3-0",
	}
	if err := store.RecordDeadLettered(ctx, first); err != nil {
		t.Fatalf("first RecordDeadLettered returned error: %v", err)
	}

	second := operations.DeadLetterRecord{
		DeliveryAttemptRecord: operations.DeliveryAttemptRecord{
			EventID:       eventID,
			StreamID:      "4-0",
			AttemptNumber: 2,
			Error:         assertError("still bad"),
			StartedAt:     startedAt.Add(time.Second),
			CompletedAt:   startedAt.Add(time.Second + 10*time.Millisecond),
		},
		OriginalStreamID: "4-0",
	}
	if err := store.RecordDeadLettered(ctx, second); err != nil {
		t.Fatalf("second RecordDeadLettered returned error: %v", err)
	}

	assertOperationStatusCount(t, pool, eventID, operations.StatusDeadLettered, 2)
	assertDeliveryAttemptCount(t, pool, eventID, 2)

	var originalStreamID string
	var attemptCount int
	var status string
	var redrivenAt sql.NullTime
	if err := pool.QueryRow(
		ctx,
		`SELECT original_stream_id, attempt_count, status, redriven_at
		 FROM dlq_records
		 WHERE event_id = $1::uuid`,
		eventID,
	).Scan(&originalStreamID, &attemptCount, &status, &redrivenAt); err != nil {
		t.Fatalf("fetch DLQ record: %v", err)
	}
	if originalStreamID != "4-0" || attemptCount != 2 || status != operations.DLQStatusOpen || redrivenAt.Valid {
		t.Fatalf("unexpected DLQ record: stream=%q attempt=%d status=%q redriven=%v", originalStreamID, attemptCount, status, redrivenAt)
	}

	var dlqRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dlq_records WHERE event_id = $1::uuid`, eventID).Scan(&dlqRows); err != nil {
		t.Fatalf("count DLQ rows: %v", err)
	}
	if dlqRows != 1 {
		t.Fatalf("expected one current DLQ row, got %d", dlqRows)
	}
}

func TestStoreIntegrationEventStatusReturnsOrderedHistoryAndAttempts(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	store := operations.NewStore(pool)
	ctx := context.Background()
	eventID := persistOperationalEvent(t, pool)
	startedAt := time.Now().UTC()
	responseCode := 200

	if _, err := pool.Exec(ctx, `
		INSERT INTO event_status_history (event_id, status, details, created_at)
		VALUES
			($1::uuid, $2, '{}'::jsonb, $4),
			($1::uuid, $3, '{}'::jsonb, $5)`,
		eventID,
		operations.StatusProcessing,
		operations.StatusDelivered,
		startedAt,
		startedAt.Add(time.Millisecond),
	); err != nil {
		t.Fatalf("insert status history: %v", err)
	}
	if err := store.RecordDelivered(ctx, operations.DeliveryAttemptRecord{
		EventID:       eventID,
		StreamID:      "1-0",
		AttemptNumber: 1,
		ResponseCode:  &responseCode,
		StartedAt:     startedAt.Add(2 * time.Millisecond),
		CompletedAt:   startedAt.Add(3 * time.Millisecond),
	}); err != nil {
		t.Fatalf("RecordDelivered returned error: %v", err)
	}

	status, err := store.EventStatus(ctx, eventID)
	if err != nil {
		t.Fatalf("EventStatus returned error: %v", err)
	}
	if status.Event.EventID != eventID {
		t.Fatalf("expected event metadata for %s, got %#v", eventID, status.Event)
	}
	if status.CurrentStatus != operations.StatusDelivered {
		t.Fatalf("expected current status DELIVERED, got %q", status.CurrentStatus)
	}
	if len(status.History) < 4 {
		t.Fatalf("expected at least four status entries, got %d", len(status.History))
	}
	if status.History[len(status.History)-1].Status != operations.StatusDelivered {
		t.Fatalf("expected final history status DELIVERED, got %q", status.History[len(status.History)-1].Status)
	}
	if len(status.DeliveryAttempts) != 1 || status.DeliveryAttempts[0].AttemptNumber != 1 {
		t.Fatalf("unexpected delivery attempts: %#v", status.DeliveryAttempts)
	}
}

func TestStoreIntegrationMissingEventStatusReturnsNotFound(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	store := operations.NewStore(pool)

	_, err := store.EventStatus(context.Background(), "00000000-0000-0000-0000-000000000001")
	if !errors.Is(err, operations.ErrEventNotFound) {
		t.Fatalf("expected ErrEventNotFound, got %v", err)
	}
}

func TestStoreIntegrationDLQListAndDetail(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	store := operations.NewStore(pool)
	ctx := context.Background()
	eventID := persistOperationalEvent(t, pool)
	startedAt := time.Now().UTC()
	if err := store.RecordDeadLettered(ctx, operations.DeadLetterRecord{
		DeliveryAttemptRecord: operations.DeliveryAttemptRecord{
			EventID:       eventID,
			StreamID:      "5-0",
			AttemptNumber: 1,
			Error:         assertError("bad request"),
			StartedAt:     startedAt,
			CompletedAt:   startedAt.Add(time.Millisecond),
		},
		OriginalStreamID: "5-0",
	}); err != nil {
		t.Fatalf("RecordDeadLettered returned error: %v", err)
	}

	records, err := store.ListDLQ(ctx, operations.DLQStatusOpen, 50)
	if err != nil {
		t.Fatalf("ListDLQ returned error: %v", err)
	}
	if len(records) != 1 || records[0].EventID != eventID || records[0].Status != operations.DLQStatusOpen {
		t.Fatalf("unexpected DLQ records: %#v", records)
	}

	detail, err := store.DLQDetail(ctx, eventID)
	if err != nil {
		t.Fatalf("DLQDetail returned error: %v", err)
	}
	if detail.Record.EventID != eventID || len(detail.History) == 0 || len(detail.DeliveryAttempts) != 1 {
		t.Fatalf("unexpected DLQ detail: %#v", detail)
	}
}

func TestStoreIntegrationRedriveSuccessAndConflict(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	store := operations.NewStore(pool)
	ctx := context.Background()
	eventID := persistOperationalEvent(t, pool)
	startedAt := time.Now().UTC()
	if err := store.RecordDeadLettered(ctx, operations.DeadLetterRecord{
		DeliveryAttemptRecord: operations.DeliveryAttemptRecord{
			EventID:       eventID,
			StreamID:      "6-0",
			AttemptNumber: 1,
			Error:         assertError("bad request"),
			StartedAt:     startedAt,
			CompletedAt:   startedAt.Add(time.Millisecond),
		},
		OriginalStreamID: "6-0",
	}); err != nil {
		t.Fatalf("RecordDeadLettered returned error: %v", err)
	}

	published := false
	result, err := store.RedriveDLQ(ctx, eventID, func(ctx context.Context, values map[string]interface{}) (string, error) {
		published = true
		if values["event_id"] != eventID || values["retry"] != "0" || values["redrive"] != "1" {
			t.Fatalf("unexpected redrive values: %#v", values)
		}
		return "7-0", nil
	})
	if err != nil {
		t.Fatalf("RedriveDLQ returned error: %v", err)
	}
	if !published || result.Status != operations.DLQStatusRedriven || result.StreamID != "7-0" {
		t.Fatalf("unexpected redrive result published=%v result=%#v", published, result)
	}
	assertOperationStatusCount(t, pool, eventID, operations.StatusRedriven, 1)

	_, err = store.RedriveDLQ(ctx, eventID, func(ctx context.Context, values map[string]interface{}) (string, error) {
		return "8-0", nil
	})
	if !errors.Is(err, operations.ErrDLQNotOpen) {
		t.Fatalf("expected ErrDLQNotOpen, got %v", err)
	}
}

func TestStoreIntegrationRedrivePublishFailureLeavesRecordOpen(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	store := operations.NewStore(pool)
	ctx := context.Background()
	eventID := persistOperationalEvent(t, pool)
	startedAt := time.Now().UTC()
	if err := store.RecordDeadLettered(ctx, operations.DeadLetterRecord{
		DeliveryAttemptRecord: operations.DeliveryAttemptRecord{
			EventID:       eventID,
			StreamID:      "8-0",
			AttemptNumber: 1,
			Error:         assertError("bad request"),
			StartedAt:     startedAt,
			CompletedAt:   startedAt.Add(time.Millisecond),
		},
		OriginalStreamID: "8-0",
	}); err != nil {
		t.Fatalf("RecordDeadLettered returned error: %v", err)
	}

	_, err := store.RedriveDLQ(ctx, eventID, func(ctx context.Context, values map[string]interface{}) (string, error) {
		return "", assertError("redis unavailable")
	})
	if err == nil {
		t.Fatal("expected redrive publish error")
	}

	detail, err := store.DLQDetail(ctx, eventID)
	if err != nil {
		t.Fatalf("DLQDetail returned error: %v", err)
	}
	if detail.Record.Status != operations.DLQStatusOpen || detail.Record.RedrivenAt != nil {
		t.Fatalf("expected DLQ record to remain open, got %#v", detail.Record)
	}
	assertOperationStatusCount(t, pool, eventID, operations.StatusRedriven, 0)
}

func TestStoreIntegrationMetricsSummaryUsesCurrentState(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	store := operations.NewStore(pool)
	ctx := context.Background()
	deliveredEventID := persistOperationalEvent(t, pool)
	retryingEventID := persistOperationalEvent(t, pool)
	dlqEventID := persistOperationalEvent(t, pool)
	startedAt := time.Now().UTC()

	if err := store.RecordDelivered(ctx, operations.DeliveryAttemptRecord{
		EventID:       deliveredEventID,
		StreamID:      "9-0",
		AttemptNumber: 1,
		StartedAt:     startedAt,
		CompletedAt:   startedAt.Add(time.Millisecond),
	}); err != nil {
		t.Fatalf("RecordDelivered returned error: %v", err)
	}
	if err := store.RecordRetrying(ctx, operations.RetryingRecord{
		DeliveryAttemptRecord: operations.DeliveryAttemptRecord{
			EventID:       retryingEventID,
			StreamID:      "10-0",
			AttemptNumber: 1,
			Error:         assertError("timeout"),
			StartedAt:     startedAt,
			CompletedAt:   startedAt.Add(time.Millisecond),
		},
		NextRetry:   2,
		ScheduledAt: startedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("RecordRetrying returned error: %v", err)
	}
	if err := store.RecordDeadLettered(ctx, operations.DeadLetterRecord{
		DeliveryAttemptRecord: operations.DeliveryAttemptRecord{
			EventID:       dlqEventID,
			StreamID:      "11-0",
			AttemptNumber: 1,
			Error:         assertError("bad request"),
			StartedAt:     startedAt,
			CompletedAt:   startedAt.Add(time.Millisecond),
		},
		OriginalStreamID: "11-0",
	}); err != nil {
		t.Fatalf("RecordDeadLettered returned error: %v", err)
	}

	summary, err := store.MetricsSummary(ctx)
	if err != nil {
		t.Fatalf("MetricsSummary returned error: %v", err)
	}
	if summary.TotalEvents != 3 || summary.Delivered != 1 || summary.Retrying != 1 || summary.OpenDLQ != 1 {
		t.Fatalf("unexpected metrics summary: %#v", summary)
	}
}

func persistOperationalEvent(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()

	result, err := ingestion.PersistEventWithOutbox(context.Background(), pool, ingestion.EventInput{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}, "")
	if err != nil {
		t.Fatalf("persist operational event: %v", err)
	}
	if !result.Created {
		t.Fatalf("expected new event, got %#v", result)
	}
	return result.EventID
}

func assertOperationStatusCount(t *testing.T, pool *pgxpool.Pool, eventID string, status string, want int) {
	t.Helper()

	var got int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM event_status_history WHERE event_id = $1::uuid AND status = $2`,
		eventID,
		status,
	).Scan(&got); err != nil {
		t.Fatalf("count status %s for event %s: %v", status, eventID, err)
	}
	if got != want {
		t.Fatalf("expected %d %s statuses for event %s, got %d", want, status, eventID, got)
	}
}

func assertDeliveryAttempt(
	t *testing.T,
	pool *pgxpool.Pool,
	eventID string,
	attemptNumber int,
	outcome string,
	wantCode sql.NullInt32,
) {
	t.Helper()

	var gotOutcome string
	var gotCode sql.NullInt32
	if err := pool.QueryRow(
		context.Background(),
		`SELECT outcome, response_code
		 FROM delivery_attempts
		 WHERE event_id = $1::uuid AND attempt_number = $2
		 ORDER BY id DESC
		 LIMIT 1`,
		eventID,
		attemptNumber,
	).Scan(&gotOutcome, &gotCode); err != nil {
		t.Fatalf("fetch delivery attempt %d for event %s: %v", attemptNumber, eventID, err)
	}
	if gotOutcome != outcome {
		t.Fatalf("expected outcome %q, got %q", outcome, gotOutcome)
	}
	if gotCode != wantCode {
		t.Fatalf("expected response code %#v, got %#v", wantCode, gotCode)
	}
}

func assertDeliveryAttemptCount(t *testing.T, pool *pgxpool.Pool, eventID string, want int) {
	t.Helper()

	var got int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM delivery_attempts WHERE event_id = $1::uuid`,
		eventID,
	).Scan(&got); err != nil {
		t.Fatalf("count delivery attempts for event %s: %v", eventID, err)
	}
	if got != want {
		t.Fatalf("expected %d delivery attempts for event %s, got %d", want, eventID, got)
	}
}

func assertError(message string) error {
	return errors.New(message)
}
