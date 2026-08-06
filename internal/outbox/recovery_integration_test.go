package outbox_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krishgondaliya/eventrail-ingestion/internal/broker/redisstream"
	"github.com/krishgondaliya/eventrail-ingestion/internal/ingestion"
	"github.com/krishgondaliya/eventrail-ingestion/internal/outbox"
	"github.com/krishgondaliya/eventrail-ingestion/internal/testutil"
	"github.com/redis/go-redis/v9"
)

type recoveryOutboxRow struct {
	ID            string
	EventID       string
	Status        string
	AttemptCount  int
	NextAttemptAt time.Time
	PublishedAt   sql.NullTime
	LastError     sql.NullString
}

func TestOutboxRunnerRedisRecoveryIntegration(t *testing.T) {
	if os.Getenv("TEST_POSTGRES_DSN") == "" {
		t.Skip("TEST_POSTGRES_DSN is not set; skipping Redis recovery integration test")
	}
	redisAddr := os.Getenv("TEST_REDIS_ADDR")
	if redisAddr == "" {
		t.Skip("TEST_REDIS_ADDR is not set; skipping Redis recovery integration test")
	}

	pool := testutil.NewPostgresPool(t)
	ctx := context.Background()

	redisClient := redis.NewClient(&redis.Options{Addr: redisAddr})
	stream := newRecoveryStreamName(t)
	t.Cleanup(func() {
		if err := redisClient.Del(context.Background(), stream).Err(); err != nil {
			t.Logf("delete Redis test stream %s: %v", stream, err)
		}
		if err := redisClient.Close(); err != nil {
			t.Logf("close Redis test client: %v", err)
		}
	})

	streamPublisher, err := redisstream.NewPublisher(redisClient, stream)
	if err != nil {
		t.Fatalf("create Redis stream publisher: %v", err)
	}

	payload := json.RawMessage(`{"invoice_id":"INV-RECOVERY-1","amount":500,"currency":"USD"}`)
	persisted, err := ingestion.PersistEventWithOutbox(ctx, pool, ingestion.EventInput{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   payload,
	}, "payment-INV-RECOVERY-1")
	if err != nil {
		t.Fatalf("persist event with outbox: %v", err)
	}
	if !persisted.Created || persisted.EventID == "" {
		t.Fatalf("expected created event, got %#v", persisted)
	}

	initialRow := fetchRecoveryOutboxRowByEventID(t, pool, persisted.EventID)
	if initialRow.Status != "pending" {
		t.Fatalf("expected initial outbox status pending, got %q", initialRow.Status)
	}
	if initialRow.AttemptCount != 0 {
		t.Fatalf("expected initial attempt_count 0, got %d", initialRow.AttemptCount)
	}
	if initialRow.PublishedAt.Valid {
		t.Fatalf("expected initial published_at null, got %v", initialRow.PublishedAt.Time)
	}
	if got := countRecoveryRows(t, pool, "events"); got != 1 {
		t.Fatalf("expected one event row before recovery, got %d", got)
	}
	if got := countRecoveryRows(t, pool, "outbox"); got != 1 {
		t.Fatalf("expected one outbox row before recovery, got %d", got)
	}

	var recovered atomic.Bool
	var realPublishCalls atomic.Int32
	publish := func(ctx context.Context, event outbox.OutboxEvent) error {
		if !recovered.Load() {
			return errors.New("simulated Redis unavailable")
		}

		realPublishCalls.Add(1)
		return streamPublisher.Publish(ctx, event)
	}

	failureCommitted := make(chan time.Time)
	var signalFailureOnce sync.Once
	publishNext := func(ctx context.Context) (outbox.PublishNextOutboxResult, error) {
		attemptStartedAt := time.Now()
		result, err := outbox.PublishNextOutboxEvent(
			ctx,
			pool,
			publish,
			500*time.Millisecond,
			500*time.Millisecond,
		)
		if errors.Is(err, outbox.ErrOutboxPublishFailed) {
			signalFailureOnce.Do(func() {
				failureCommitted <- attemptStartedAt
			})
		}
		return result, err
	}

	var runnerErrors []error
	var runnerErrorsMu sync.Mutex
	runner, err := outbox.NewRunner(publishNext, 10*time.Millisecond, func(err error) {
		runnerErrorsMu.Lock()
		defer runnerErrorsMu.Unlock()
		runnerErrors = append(runnerErrors, err)
	})
	if err != nil {
		t.Fatalf("create outbox runner: %v", err)
	}

	runnerCtx, cancelRunner := context.WithCancel(ctx)
	defer cancelRunner()
	runnerDone := make(chan error, 1)
	go func() {
		runnerDone <- runner.Run(runnerCtx)
	}()

	failureAttemptStartedAt := waitForFailureCommit(t, failureCommitted, 2*time.Second)

	failedRow := fetchRecoveryOutboxRow(t, pool, initialRow.ID)
	if failedRow.Status != "pending" {
		t.Fatalf("expected failed outbox status pending, got %q", failedRow.Status)
	}
	if failedRow.AttemptCount != 1 {
		t.Fatalf("expected failed attempt_count 1, got %d", failedRow.AttemptCount)
	}
	if !failedRow.NextAttemptAt.After(failureAttemptStartedAt) {
		t.Fatalf("expected retry to be scheduled after failed attempt start")
	}
	if !failedRow.LastError.Valid || failedRow.LastError.String == "" {
		t.Fatalf("expected stored publication error, got %#v", failedRow.LastError)
	}
	if failedRow.PublishedAt.Valid {
		t.Fatalf("expected failed published_at null, got %v", failedRow.PublishedAt.Time)
	}
	if !recoveryEventExists(t, pool, persisted.EventID) {
		t.Fatal("expected persisted event to remain durable after publication failure")
	}
	if messages := readRecoveryStreamMessages(t, redisClient, stream); len(messages) != 0 {
		t.Fatalf("expected zero Redis messages before recovery, got %d", len(messages))
	}
	assertRunnerSawPublicationFailure(t, &runnerErrorsMu, &runnerErrors)

	recovered.Store(true)

	var finalRow recoveryOutboxRow
	var finalMessages []redis.XMessage
	waitForCondition(t, 5*time.Second, 20*time.Millisecond, func() bool {
		finalRow = fetchRecoveryOutboxRow(t, pool, initialRow.ID)
		finalMessages = readRecoveryStreamMessages(t, redisClient, stream)
		return finalRow.Status == "published" && len(finalMessages) == 1
	})

	if finalRow.AttemptCount != 1 {
		t.Fatalf("expected final attempt_count 1, got %d", finalRow.AttemptCount)
	}
	if !finalRow.PublishedAt.Valid {
		t.Fatal("expected final published_at to be populated")
	}
	if finalRow.LastError.Valid {
		t.Fatalf("expected final last_error null, got %q", finalRow.LastError.String)
	}
	if got := countRecoveryRows(t, pool, "events"); got != 1 {
		t.Fatalf("expected one event row after recovery, got %d", got)
	}
	if got := countRecoveryRows(t, pool, "outbox"); got != 1 {
		t.Fatalf("expected one outbox row after recovery, got %d", got)
	}
	if realPublishCalls.Load() != 1 {
		t.Fatalf("expected exactly one real Redis publish call, got %d", realPublishCalls.Load())
	}

	assertRecoveryRedisMessage(t, finalMessages[0], finalRow.ID, persisted.EventID, payload)

	cancelRunner()
	select {
	case err := <-runnerDone:
		if err != nil {
			t.Fatalf("expected runner to stop cleanly, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for outbox runner to stop")
	}
}

func waitForFailureCommit(t *testing.T, failureCommitted <-chan time.Time, timeout time.Duration) time.Time {
	t.Helper()

	select {
	case failureAttemptStartedAt := <-failureCommitted:
		return failureAttemptStartedAt
	case <-time.After(timeout):
		t.Fatal("timed out waiting for committed publication failure")
		return time.Time{}
	}
}

func assertRunnerSawPublicationFailure(t *testing.T, mu *sync.Mutex, errorsSeen *[]error) {
	t.Helper()

	mu.Lock()
	defer mu.Unlock()
	for _, err := range *errorsSeen {
		if errors.Is(err, outbox.ErrOutboxPublishFailed) {
			return
		}
	}
	t.Fatalf("expected runner error callback to receive ErrOutboxPublishFailed, got %d errors", len(*errorsSeen))
}

func assertRecoveryRedisMessage(
	t *testing.T,
	message redis.XMessage,
	outboxID string,
	eventID string,
	wantPayload json.RawMessage,
) {
	t.Helper()

	values := message.Values
	wantValues := map[string]string{
		"outbox_id":  outboxID,
		"event_id":   eventID,
		"event_type": "invoice.paid",
		"source":     "payments-service",
		"retry":      "0",
	}
	for key, want := range wantValues {
		got, ok := values[key]
		if !ok {
			t.Fatalf("missing Redis field %q", key)
		}
		if got != want {
			t.Fatalf("expected Redis field %s=%q, got %#v", key, want, got)
		}
	}
	if _, ok := values["created_at"]; !ok {
		t.Fatal("missing Redis field created_at")
	}

	payloadValue, ok := values["payload"].(string)
	if !ok {
		t.Fatalf("expected Redis payload to be string, got %T", values["payload"])
	}
	assertJSONValueEqual(t, []byte(payloadValue), wantPayload)
}

func fetchRecoveryOutboxRowByEventID(t *testing.T, pool *pgxpool.Pool, eventID string) recoveryOutboxRow {
	t.Helper()

	var row recoveryOutboxRow
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

func fetchRecoveryOutboxRow(t *testing.T, pool *pgxpool.Pool, outboxID string) recoveryOutboxRow {
	t.Helper()

	var row recoveryOutboxRow
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

func recoveryEventExists(t *testing.T, pool *pgxpool.Pool, eventID string) bool {
	t.Helper()

	var exists bool
	if err := pool.QueryRow(
		context.Background(),
		`SELECT EXISTS (SELECT 1 FROM events WHERE id = $1::uuid)`,
		eventID,
	).Scan(&exists); err != nil {
		t.Fatalf("check event exists %s: %v", eventID, err)
	}
	return exists
}

func countRecoveryRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()

	var count int
	query := `SELECT count(*) FROM "` + table + `"`
	if err := pool.QueryRow(context.Background(), query).Scan(&count); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	return count
}

func readRecoveryStreamMessages(t *testing.T, client *redis.Client, stream string) []redis.XMessage {
	t.Helper()

	messages, err := client.XRange(context.Background(), stream, "-", "+").Result()
	if err != nil {
		t.Fatalf("read Redis stream %s: %v", stream, err)
	}
	return messages
}

func waitForCondition(t *testing.T, timeout time.Duration, interval time.Duration, condition func() bool) {
	t.Helper()

	deadline := time.After(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if condition() {
			return
		}

		select {
		case <-deadline:
			t.Fatal("timed out waiting for condition")
		case <-ticker.C:
		}
	}
}

func newRecoveryStreamName(t *testing.T) string {
	t.Helper()

	var randomBytes [8]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		t.Fatalf("generate Redis stream suffix: %v", err)
	}
	return fmt.Sprintf("eventrail:recovery-test:%d:%x", time.Now().UnixNano(), randomBytes[:])
}

func assertJSONValueEqual(t *testing.T, got []byte, want []byte) {
	t.Helper()

	gotValue := decodeJSONValue(t, got)
	wantValue := decodeJSONValue(t, want)
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatal("JSON payload did not match expected value")
	}
}

func decodeJSONValue(t *testing.T, raw []byte) any {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode JSON value: %v", err)
	}
	return value
}
