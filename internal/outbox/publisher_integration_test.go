package outbox

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krishgondaliya/eventrail-ingestion/internal/ingestion"
	"github.com/krishgondaliya/eventrail-ingestion/internal/testutil"
)

type outboxPublisherRow struct {
	ID            string
	EventID       string
	Status        string
	AttemptCount  int
	NextAttemptAt time.Time
	PublishedAt   sql.NullTime
	LastError     sql.NullString
}

type recordingOutboxPublisher struct {
	mu     sync.Mutex
	events []OutboxEvent
	err    error
}

func (p *recordingOutboxPublisher) publish(ctx context.Context, event OutboxEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.events = append(p.events, event)
	return p.err
}

func (p *recordingOutboxPublisher) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

func (p *recordingOutboxPublisher) event(index int) OutboxEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.events[index]
}

func TestPublishNextOutboxEventIntegrationSuccessfulPublication(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()
	req := sampleOutboxEventRequest()
	persisted := persistSampleOutboxEvent(t, pool, req)

	publisher := &recordingOutboxPublisher{}
	result, err := PublishNextOutboxEvent(ctx, pool, publisher.publish, 100*time.Millisecond, 10*time.Second)
	if err != nil {
		t.Fatalf("PublishNextOutboxEvent returned error: %v", err)
	}

	if !result.Found || !result.Published {
		t.Fatalf("expected found published result, got %#v", result)
	}
	if result.EventID != persisted.EventID || result.OutboxID == "" {
		t.Fatalf("unexpected result IDs: %#v", result)
	}
	if publisher.calls() != 1 {
		t.Fatalf("expected publisher to be called once, got %d", publisher.calls())
	}

	publishedEvent := publisher.event(0)
	if publishedEvent.OutboxID != result.OutboxID || publishedEvent.EventID != persisted.EventID {
		t.Fatalf("publisher received wrong IDs: %#v", publishedEvent)
	}
	if publishedEvent.EventType != req.EventType || publishedEvent.Source != req.Source {
		t.Fatalf("publisher received wrong event fields: %#v", publishedEvent)
	}
	assertJSONEqual(t, string(publishedEvent.Payload), string(req.Payload))
	if publishedEvent.CreatedAt.IsZero() {
		t.Fatal("expected publisher event CreatedAt to be populated")
	}
	if publishedEvent.AttemptCount != 0 {
		t.Fatalf("expected attempt count 0, got %d", publishedEvent.AttemptCount)
	}

	row := fetchOutboxPublisherRow(t, pool, result.OutboxID)
	if row.Status != "published" {
		t.Fatalf("expected published status, got %q", row.Status)
	}
	if !row.PublishedAt.Valid {
		t.Fatal("expected published_at to be populated")
	}
	if row.AttemptCount != 0 {
		t.Fatalf("expected attempt_count to remain 0, got %d", row.AttemptCount)
	}
	if row.LastError.Valid {
		t.Fatalf("expected last_error null, got %q", row.LastError.String)
	}
}

func TestPublishNextOutboxEventIntegrationNoEligibleWork(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	publisher := &recordingOutboxPublisher{}

	result, err := PublishNextOutboxEvent(context.Background(), pool, publisher.publish, 100*time.Millisecond, 10*time.Second)
	if err != nil {
		t.Fatalf("PublishNextOutboxEvent returned error: %v", err)
	}
	if result.Found {
		t.Fatalf("expected no work, got %#v", result)
	}
	if publisher.calls() != 0 {
		t.Fatalf("expected publisher not to be called, got %d", publisher.calls())
	}
}

func TestPublishNextOutboxEventIntegrationFutureScheduledRowSkipped(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()
	persisted := persistSampleOutboxEvent(t, pool, sampleOutboxEventRequest())

	if _, err := pool.Exec(ctx, `UPDATE outbox SET next_attempt_at = now() + interval '1 hour' WHERE event_id = $1::uuid`, persisted.EventID); err != nil {
		t.Fatalf("schedule outbox row into future: %v", err)
	}

	publisher := &recordingOutboxPublisher{}
	result, err := PublishNextOutboxEvent(ctx, pool, publisher.publish, 100*time.Millisecond, 10*time.Second)
	if err != nil {
		t.Fatalf("PublishNextOutboxEvent returned error: %v", err)
	}
	if result.Found {
		t.Fatalf("expected future row to be skipped, got %#v", result)
	}
	if publisher.calls() != 0 {
		t.Fatalf("expected publisher not to be called, got %d", publisher.calls())
	}
}

func TestPublishNextOutboxEventIntegrationFailedPublicationIsRescheduled(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()
	persisted := persistSampleOutboxEvent(t, pool, sampleOutboxEventRequest())

	publisher := &recordingOutboxPublisher{err: errors.New(" remote timeout ")}
	firstStartedAt := time.Now()
	first, err := PublishNextOutboxEvent(ctx, pool, publisher.publish, time.Minute, time.Hour)
	if err == nil {
		t.Fatal("expected publication failure error")
	}
	if !errors.Is(err, ErrOutboxPublishFailed) {
		t.Fatalf("expected ErrOutboxPublishFailed, got %v", err)
	}
	if !first.Found || first.Published {
		t.Fatalf("expected found unpublished result, got %#v", first)
	}
	if first.EventID != persisted.EventID {
		t.Fatalf("expected event ID %q, got %q", persisted.EventID, first.EventID)
	}
	if publisher.calls() != 1 {
		t.Fatalf("expected publisher call count 1, got %d", publisher.calls())
	}

	firstRow := fetchOutboxPublisherRow(t, pool, first.OutboxID)
	if firstRow.Status != "pending" {
		t.Fatalf("expected pending status, got %q", firstRow.Status)
	}
	if firstRow.AttemptCount != 1 {
		t.Fatalf("expected attempt_count 1, got %d", firstRow.AttemptCount)
	}
	if !firstRow.NextAttemptAt.After(firstStartedAt) {
		t.Fatalf("expected next_attempt_at after failure start, got %v <= %v", firstRow.NextAttemptAt, firstStartedAt)
	}
	if !firstRow.LastError.Valid || firstRow.LastError.String != "remote timeout" {
		t.Fatalf("expected sanitized last_error, got %#v", firstRow.LastError)
	}
	if len([]rune(firstRow.LastError.String)) > maxStoredOutboxErrorLength {
		t.Fatalf("expected bounded last_error, got %d runes", len([]rune(firstRow.LastError.String)))
	}
	if firstRow.PublishedAt.Valid {
		t.Fatalf("expected published_at null, got %v", firstRow.PublishedAt.Time)
	}

	makeOutboxEligible(t, pool, first.OutboxID)
	second, err := PublishNextOutboxEvent(ctx, pool, publisher.publish, time.Minute, time.Hour)
	if err == nil {
		t.Fatal("expected second publication failure error")
	}
	if !errors.Is(err, ErrOutboxPublishFailed) {
		t.Fatalf("expected ErrOutboxPublishFailed, got %v", err)
	}
	secondRow := fetchOutboxPublisherRow(t, pool, second.OutboxID)
	if secondRow.AttemptCount != 2 {
		t.Fatalf("expected attempt_count 2, got %d", secondRow.AttemptCount)
	}
	if !secondRow.NextAttemptAt.After(firstRow.NextAttemptAt) {
		t.Fatalf("expected second backoff to move later than first, got %v <= %v", secondRow.NextAttemptAt, firstRow.NextAttemptAt)
	}
}

func TestPublishNextOutboxEventIntegrationRetryEventuallySucceeds(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()
	persistSampleOutboxEvent(t, pool, sampleOutboxEventRequest())

	publisher := &recordingOutboxPublisher{err: errors.New("temporary outage")}
	first, err := PublishNextOutboxEvent(ctx, pool, publisher.publish, 100*time.Millisecond, time.Second)
	if err == nil {
		t.Fatal("expected first publication failure")
	}
	if !errors.Is(err, ErrOutboxPublishFailed) {
		t.Fatalf("expected ErrOutboxPublishFailed, got %v", err)
	}

	makeOutboxEligible(t, pool, first.OutboxID)
	publisher.err = nil
	second, err := PublishNextOutboxEvent(ctx, pool, publisher.publish, 100*time.Millisecond, time.Second)
	if err != nil {
		t.Fatalf("expected retry success, got %v", err)
	}
	if !second.Found || !second.Published {
		t.Fatalf("expected successful retry result, got %#v", second)
	}

	row := fetchOutboxPublisherRow(t, pool, second.OutboxID)
	if row.Status != "published" {
		t.Fatalf("expected published status, got %q", row.Status)
	}
	if row.AttemptCount != 1 {
		t.Fatalf("expected attempt count to remain prior failures, got %d", row.AttemptCount)
	}
	if !row.PublishedAt.Valid {
		t.Fatal("expected published_at populated")
	}
	if row.LastError.Valid {
		t.Fatalf("expected last_error cleared, got %q", row.LastError.String)
	}
}

func TestPublishNextOutboxEventIntegrationConcurrentPublishers(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()
	persistSampleOutboxEvent(t, pool, sampleOutboxEventRequest())

	const goroutines = 5
	publisherStarted := make(chan struct{})
	releasePublisher := make(chan struct{})
	publisherDone := make(chan struct{})
	var once sync.Once
	var publisherCalls int
	var publisherMu sync.Mutex

	publish := func(ctx context.Context, event OutboxEvent) error {
		publisherMu.Lock()
		publisherCalls++
		publisherMu.Unlock()

		once.Do(func() { close(publisherStarted) })
		<-releasePublisher
		close(publisherDone)
		return nil
	}

	start := make(chan struct{})
	results := make(chan PublishNextOutboxResult, goroutines)
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := PublishNextOutboxEvent(ctx, pool, publish, 100*time.Millisecond, time.Second)
			results <- result
			errs <- err
		}()
	}

	close(start)
	<-publisherStarted

	noWorkResults := 0
	for noWorkResults < goroutines-1 {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("unexpected concurrent publisher error: %v", err)
			}
			result := <-results
			if result.Found {
				t.Fatalf("expected locked row to be skipped, got %#v", result)
			}
			noWorkResults++
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for skipped concurrent publishers")
		}
	}

	close(releasePublisher)
	<-publisherDone
	wg.Wait()

	finalErr := <-errs
	finalResult := <-results
	if finalErr != nil {
		t.Fatalf("publishing goroutine returned error: %v", finalErr)
	}
	if !finalResult.Found || !finalResult.Published {
		t.Fatalf("expected one published result, got %#v", finalResult)
	}

	publisherMu.Lock()
	gotPublisherCalls := publisherCalls
	publisherMu.Unlock()
	if gotPublisherCalls != 1 {
		t.Fatalf("expected exactly one publisher invocation, got %d", gotPublisherCalls)
	}

	row := fetchOutboxPublisherRow(t, pool, finalResult.OutboxID)
	if row.Status != "published" {
		t.Fatalf("expected published outbox row, got %q", row.Status)
	}
}

func TestPublishNextOutboxEventIntegrationSuccessfulPublishUpdateFailureRollsBack(t *testing.T) {
	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()
	persisted := persistSampleOutboxEvent(t, pool, sampleOutboxEventRequest())

	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION reject_published_outbox_update()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			IF NEW.status = 'published' THEN
				RAISE EXCEPTION 'reject published status for test';
			END IF;
			RETURN NEW;
		END;
		$$`); err != nil {
		t.Fatalf("create rejecting update function: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TRIGGER reject_published_outbox_update
		BEFORE UPDATE ON outbox
		FOR EACH ROW
		EXECUTE FUNCTION reject_published_outbox_update()`); err != nil {
		t.Fatalf("create rejecting update trigger: %v", err)
	}

	publisher := &recordingOutboxPublisher{}
	_, err := PublishNextOutboxEvent(ctx, pool, publisher.publish, 100*time.Millisecond, time.Second)
	if err == nil {
		t.Fatal("expected database update failure after successful publish")
	}
	if errors.Is(err, ErrOutboxPublishFailed) {
		t.Fatalf("expected database error, got publication failure: %v", err)
	}
	if publisher.calls() != 1 {
		t.Fatalf("expected publisher called once, got %d", publisher.calls())
	}

	row := fetchOutboxPublisherRowByEventID(t, pool, persisted.EventID)
	if row.Status != "pending" {
		t.Fatalf("expected rollback to leave pending status, got %q", row.Status)
	}
	if row.PublishedAt.Valid {
		t.Fatalf("expected rollback to leave published_at null, got %v", row.PublishedAt.Time)
	}

	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_published_outbox_update ON outbox`); err != nil {
		t.Fatalf("drop rejecting update trigger: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_published_outbox_update()`); err != nil {
		t.Fatalf("drop rejecting update function: %v", err)
	}

	second, err := PublishNextOutboxEvent(ctx, pool, publisher.publish, 100*time.Millisecond, time.Second)
	if err != nil {
		t.Fatalf("expected future call to publish again, got %v", err)
	}
	if !second.Found || !second.Published {
		t.Fatalf("expected future call to publish pending row, got %#v", second)
	}
	if publisher.calls() != 2 {
		t.Fatalf("expected duplicate publication window to allow second publisher call, got %d", publisher.calls())
	}
}

func sampleOutboxEventRequest() ingestion.EventInput {
	return ingestion.EventInput{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}
}

func persistSampleOutboxEvent(t *testing.T, pool *pgxpool.Pool, req ingestion.EventInput) ingestion.PersistResult {
	t.Helper()

	result, err := ingestion.PersistEventWithOutbox(context.Background(), pool, req, "payment-INV-2048")
	if err != nil {
		t.Fatalf("persist sample outbox event: %v", err)
	}
	if !result.Created {
		t.Fatalf("expected sample event to be created, got %#v", result)
	}
	return result
}

func fetchOutboxPublisherRow(t *testing.T, pool *pgxpool.Pool, outboxID string) outboxPublisherRow {
	t.Helper()

	var row outboxPublisherRow
	if err := pool.QueryRow(
		context.Background(),
		`SELECT id::text, event_id::text, status, attempt_count, next_attempt_at, published_at, last_error
		 FROM outbox
		 WHERE id = $1::uuid`,
		outboxID,
	).Scan(
		&row.ID,
		&row.EventID,
		&row.Status,
		&row.AttemptCount,
		&row.NextAttemptAt,
		&row.PublishedAt,
		&row.LastError,
	); err != nil {
		t.Fatalf("fetch outbox row %s: %v", outboxID, err)
	}
	return row
}

func fetchOutboxPublisherRowByEventID(t *testing.T, pool *pgxpool.Pool, eventID string) outboxPublisherRow {
	t.Helper()

	var row outboxPublisherRow
	if err := pool.QueryRow(
		context.Background(),
		`SELECT id::text, event_id::text, status, attempt_count, next_attempt_at, published_at, last_error
		 FROM outbox
		 WHERE event_id = $1::uuid`,
		eventID,
	).Scan(
		&row.ID,
		&row.EventID,
		&row.Status,
		&row.AttemptCount,
		&row.NextAttemptAt,
		&row.PublishedAt,
		&row.LastError,
	); err != nil {
		t.Fatalf("fetch outbox row for event %s: %v", eventID, err)
	}
	return row
}

func makeOutboxEligible(t *testing.T, pool *pgxpool.Pool, outboxID string) {
	t.Helper()

	if _, err := pool.Exec(context.Background(), `UPDATE outbox SET next_attempt_at = now() WHERE id = $1::uuid`, outboxID); err != nil {
		t.Fatalf("make outbox row eligible: %v", err)
	}
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
