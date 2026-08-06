package ingestion

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krishgondaliya/eventrail-ingestion/internal/testutil"
)

type storedEvent struct {
	ID             string
	EventType      string
	Source         string
	Payload        string
	IdempotencyKey sql.NullString
	RequestHash    sql.NullString
}

type storedOutbox struct {
	EventID       string
	Status        string
	AttemptCount  int
	NextAttemptAt time.Time
	PublishedAt   sql.NullTime
}

func TestPersistEventWithOutboxIntegrationNewKeyedEvent(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()

	req := EventInput{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}
	result, err := PersistEventWithOutbox(ctx, pool, req, "payment-INV-2048")
	if err != nil {
		t.Fatalf("PersistEventWithOutbox returned error: %v", err)
	}
	if !result.Created {
		t.Fatal("expected Created true")
	}
	if result.EventID == "" {
		t.Fatal("expected nonempty event ID")
	}

	if got := countRows(t, pool, "events"); got != 1 {
		t.Fatalf("expected one event row, got %d", got)
	}
	if got := countRows(t, pool, "outbox"); got != 1 {
		t.Fatalf("expected one outbox row, got %d", got)
	}

	event := fetchStoredEvent(t, pool, result.EventID)
	if event.EventType != req.EventType {
		t.Fatalf("expected event_type %q, got %q", req.EventType, event.EventType)
	}
	if event.Source != req.Source {
		t.Fatalf("expected source %q, got %q", req.Source, event.Source)
	}
	assertJSONEqual(t, event.Payload, string(req.Payload))
	if !event.IdempotencyKey.Valid || event.IdempotencyKey.String != "payment-INV-2048" {
		t.Fatalf("expected stored idempotency key payment-INV-2048, got %#v", event.IdempotencyKey)
	}

	wantHash, err := computeRequestHash(req.EventType, req.Source, req.Payload)
	if err != nil {
		t.Fatalf("computeRequestHash returned error: %v", err)
	}
	if !event.RequestHash.Valid || event.RequestHash.String != wantHash {
		t.Fatalf("expected request hash %q, got %#v", wantHash, event.RequestHash)
	}

	outbox := fetchStoredOutbox(t, pool, result.EventID)
	if outbox.EventID != result.EventID {
		t.Fatalf("expected outbox event_id %q, got %q", result.EventID, outbox.EventID)
	}
	if outbox.Status != "pending" {
		t.Fatalf("expected pending outbox status, got %q", outbox.Status)
	}
	if outbox.AttemptCount != 0 {
		t.Fatalf("expected attempt_count 0, got %d", outbox.AttemptCount)
	}
	if outbox.NextAttemptAt.IsZero() {
		t.Fatal("expected next_attempt_at to be populated")
	}
	if outbox.PublishedAt.Valid {
		t.Fatalf("expected published_at null, got %v", outbox.PublishedAt.Time)
	}
}

func TestPersistEventWithOutboxIntegrationIdenticalRetry(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()

	firstReq := EventInput{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}
	secondReq := EventInput{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload: json.RawMessage(`{
			"currency": "USD",
			"amount": 500,
			"invoice_id": "INV-2048"
		}`),
	}

	first, err := PersistEventWithOutbox(ctx, pool, firstReq, "payment-INV-2048")
	if err != nil {
		t.Fatalf("first persist returned error: %v", err)
	}
	if !first.Created {
		t.Fatal("expected first request to create event")
	}

	second, err := PersistEventWithOutbox(ctx, pool, secondReq, "payment-INV-2048")
	if err != nil {
		t.Fatalf("second persist returned error: %v", err)
	}
	if second.Created {
		t.Fatal("expected second request to reuse existing event")
	}
	if second.EventID != first.EventID {
		t.Fatalf("expected same event ID %q, got %q", first.EventID, second.EventID)
	}

	if got := countRows(t, pool, "events"); got != 1 {
		t.Fatalf("expected one event row, got %d", got)
	}
	if got := countRows(t, pool, "outbox"); got != 1 {
		t.Fatalf("expected one outbox row, got %d", got)
	}
	event := fetchStoredEvent(t, pool, first.EventID)
	assertJSONEqual(t, event.Payload, string(firstReq.Payload))
}

func TestPersistEventWithOutboxIntegrationConflictingRetry(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()

	firstReq := EventInput{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}
	secondReq := EventInput{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":600,"currency":"USD"}`),
	}

	first, err := PersistEventWithOutbox(ctx, pool, firstReq, "payment-INV-2048")
	if err != nil {
		t.Fatalf("first persist returned error: %v", err)
	}

	_, err = PersistEventWithOutbox(ctx, pool, secondReq, "payment-INV-2048")
	if err == nil {
		t.Fatal("expected idempotency conflict error")
	}
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
	for _, forbidden := range []string{"INV-2048", "600", "USD"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("conflict error leaked payload content %q: %v", forbidden, err)
		}
	}

	if got := countRows(t, pool, "events"); got != 1 {
		t.Fatalf("expected one event row, got %d", got)
	}
	if got := countRows(t, pool, "outbox"); got != 1 {
		t.Fatalf("expected one outbox row, got %d", got)
	}
	event := fetchStoredEvent(t, pool, first.EventID)
	assertJSONEqual(t, event.Payload, string(firstReq.Payload))
}

func TestPersistEventWithOutboxIntegrationNoKeyCreatesIndependentEvents(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()

	req := EventInput{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}

	first, err := PersistEventWithOutbox(ctx, pool, req, "")
	if err != nil {
		t.Fatalf("first persist returned error: %v", err)
	}
	second, err := PersistEventWithOutbox(ctx, pool, req, "   ")
	if err != nil {
		t.Fatalf("second persist returned error: %v", err)
	}
	if !first.Created || !second.Created {
		t.Fatalf("expected both requests to create events, got first=%v second=%v", first.Created, second.Created)
	}
	if first.EventID == second.EventID {
		t.Fatalf("expected independent event IDs, got %q twice", first.EventID)
	}

	if got := countRows(t, pool, "events"); got != 2 {
		t.Fatalf("expected two event rows, got %d", got)
	}
	if got := countRows(t, pool, "outbox"); got != 2 {
		t.Fatalf("expected two outbox rows, got %d", got)
	}
	if got := countWhere(t, pool, "events", "idempotency_key IS NULL"); got != 2 {
		t.Fatalf("expected two null idempotency keys, got %d", got)
	}
}

func TestPersistEventWithOutboxIntegrationConcurrentIdenticalRequests(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()

	const goroutines = 10
	req := EventInput{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}

	start := make(chan struct{})
	results := make([]PersistResult, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errs[index] = PersistEventWithOutbox(ctx, pool, req, "payment-INV-2048")
		}(i)
	}

	close(start)
	wg.Wait()

	createdCount := 0
	var eventID string
	for i := 0; i < goroutines; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d returned unexpected error: %v", i, errs[i])
		}
		if results[i].Created {
			createdCount++
		}
		if results[i].EventID == "" {
			t.Fatalf("goroutine %d returned empty event ID", i)
		}
		if eventID == "" {
			eventID = results[i].EventID
		}
		if results[i].EventID != eventID {
			t.Fatalf("goroutine %d returned event ID %q, expected %q", i, results[i].EventID, eventID)
		}
	}
	if createdCount != 1 {
		t.Fatalf("expected exactly one created result, got %d", createdCount)
	}
	if got := countRows(t, pool, "events"); got != 1 {
		t.Fatalf("expected one event row, got %d", got)
	}
	if got := countRows(t, pool, "outbox"); got != 1 {
		t.Fatalf("expected one outbox row, got %d", got)
	}
}

func TestPersistEventWithOutboxIntegrationLegacyNullHashConflicts(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()

	legacyPayload := json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`)
	var legacyID string
	if err := pool.QueryRow(
		ctx,
		`INSERT INTO events (event_type, source, payload, idempotency_key, request_hash)
		 VALUES ($1, $2, $3, $4, NULL)
		 RETURNING id::text`,
		"invoice.paid",
		"payments-service",
		legacyPayload,
		"payment-INV-2048",
	).Scan(&legacyID); err != nil {
		t.Fatalf("insert legacy event: %v", err)
	}

	req := EventInput{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   legacyPayload,
	}
	_, err := PersistEventWithOutbox(ctx, pool, req, "payment-INV-2048")
	if err == nil {
		t.Fatal("expected idempotency conflict")
	}
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}

	if got := countRows(t, pool, "events"); got != 1 {
		t.Fatalf("expected one event row, got %d", got)
	}
	if got := countRows(t, pool, "outbox"); got != 0 {
		t.Fatalf("expected no outbox rows, got %d", got)
	}
	event := fetchStoredEvent(t, pool, legacyID)
	if event.RequestHash.Valid {
		t.Fatalf("expected legacy request_hash to remain null, got %q", event.RequestHash.String)
	}
	assertJSONEqual(t, event.Payload, string(legacyPayload))
}

func TestPersistEventWithOutboxIntegrationOutboxFailureRollsBackEvent(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `ALTER TABLE outbox ADD CONSTRAINT outbox_reject_test CHECK (false)`); err != nil {
		t.Fatalf("add rejecting outbox constraint: %v", err)
	}

	req := EventInput{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}
	_, err := PersistEventWithOutbox(ctx, pool, req, "payment-INV-2048")
	if err == nil {
		t.Fatal("expected outbox insertion failure")
	}

	if got := countRows(t, pool, "events"); got != 0 {
		t.Fatalf("expected event insert to roll back, got %d event rows", got)
	}
	if got := countRows(t, pool, "outbox"); got != 0 {
		t.Fatalf("expected no outbox rows, got %d", got)
	}
}

func TestPersistEventWithOutboxIntegrationCancelledContextDoesNotPersist(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := EventInput{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}
	_, err := PersistEventWithOutbox(ctx, pool, req, "payment-INV-2048")
	if err == nil {
		t.Fatal("expected cancelled context error")
	}

	if got := countRows(t, pool, "events"); got != 0 {
		t.Fatalf("expected no event rows, got %d", got)
	}
	if got := countRows(t, pool, "outbox"); got != 0 {
		t.Fatalf("expected no outbox rows, got %d", got)
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	return countWhere(t, pool, table, "true")
}

func countWhere(t *testing.T, pool *pgxpool.Pool, table string, where string) int {
	t.Helper()

	var count int
	query := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", quotePostgresIdentifier(table), where)
	if err := pool.QueryRow(context.Background(), query).Scan(&count); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	return count
}

func quotePostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func fetchStoredEvent(t *testing.T, pool *pgxpool.Pool, eventID string) storedEvent {
	t.Helper()

	var event storedEvent
	if err := pool.QueryRow(
		context.Background(),
		`SELECT id::text, event_type, source, payload::text, idempotency_key, request_hash
		 FROM events
		 WHERE id = $1::uuid`,
		eventID,
	).Scan(
		&event.ID,
		&event.EventType,
		&event.Source,
		&event.Payload,
		&event.IdempotencyKey,
		&event.RequestHash,
	); err != nil {
		t.Fatalf("fetch stored event %s: %v", eventID, err)
	}
	return event
}

func fetchStoredOutbox(t *testing.T, pool *pgxpool.Pool, eventID string) storedOutbox {
	t.Helper()

	var outbox storedOutbox
	if err := pool.QueryRow(
		context.Background(),
		`SELECT event_id::text, status, attempt_count, next_attempt_at, published_at
		 FROM outbox
		 WHERE event_id = $1::uuid`,
		eventID,
	).Scan(
		&outbox.EventID,
		&outbox.Status,
		&outbox.AttemptCount,
		&outbox.NextAttemptAt,
		&outbox.PublishedAt,
	); err != nil {
		t.Fatalf("fetch outbox row for event %s: %v", eventID, err)
	}
	return outbox
}

func assertJSONEqual(t *testing.T, got string, want string) {
	t.Helper()

	gotValue := decodeJSONValue(t, []byte(got))
	wantValue := decodeJSONValue(t, []byte(want))
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func decodeJSONValue(t *testing.T, raw []byte) any {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode JSON value %q: %v", string(raw), err)
	}
	return value
}
