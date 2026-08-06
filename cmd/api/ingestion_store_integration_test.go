package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	pool := newIntegrationTestPool(t)
	ctx := context.Background()

	req := CreateEventRequest{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}
	result, err := persistEventWithOutbox(ctx, pool, req, "payment-INV-2048")
	if err != nil {
		t.Fatalf("persistEventWithOutbox returned error: %v", err)
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
	pool := newIntegrationTestPool(t)
	ctx := context.Background()

	firstReq := CreateEventRequest{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}
	secondReq := CreateEventRequest{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload: json.RawMessage(`{
			"currency": "USD",
			"amount": 500,
			"invoice_id": "INV-2048"
		}`),
	}

	first, err := persistEventWithOutbox(ctx, pool, firstReq, "payment-INV-2048")
	if err != nil {
		t.Fatalf("first persist returned error: %v", err)
	}
	if !first.Created {
		t.Fatal("expected first request to create event")
	}

	second, err := persistEventWithOutbox(ctx, pool, secondReq, "payment-INV-2048")
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
	pool := newIntegrationTestPool(t)
	ctx := context.Background()

	firstReq := CreateEventRequest{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}
	secondReq := CreateEventRequest{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":600,"currency":"USD"}`),
	}

	first, err := persistEventWithOutbox(ctx, pool, firstReq, "payment-INV-2048")
	if err != nil {
		t.Fatalf("first persist returned error: %v", err)
	}

	_, err = persistEventWithOutbox(ctx, pool, secondReq, "payment-INV-2048")
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
	pool := newIntegrationTestPool(t)
	ctx := context.Background()

	req := CreateEventRequest{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}

	first, err := persistEventWithOutbox(ctx, pool, req, "")
	if err != nil {
		t.Fatalf("first persist returned error: %v", err)
	}
	second, err := persistEventWithOutbox(ctx, pool, req, "   ")
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
	pool := newIntegrationTestPool(t)
	ctx := context.Background()

	const goroutines = 10
	req := CreateEventRequest{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}

	start := make(chan struct{})
	results := make([]PersistEventResult, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errs[index] = persistEventWithOutbox(ctx, pool, req, "payment-INV-2048")
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
	pool := newIntegrationTestPool(t)
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

	req := CreateEventRequest{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   legacyPayload,
	}
	_, err := persistEventWithOutbox(ctx, pool, req, "payment-INV-2048")
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
	pool := newIntegrationTestPool(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `ALTER TABLE outbox ADD CONSTRAINT outbox_reject_test CHECK (false)`); err != nil {
		t.Fatalf("add rejecting outbox constraint: %v", err)
	}

	req := CreateEventRequest{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}
	_, err := persistEventWithOutbox(ctx, pool, req, "payment-INV-2048")
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
	pool := newIntegrationTestPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := CreateEventRequest{
		EventType: "invoice.paid",
		Source:    "payments-service",
		Payload:   json.RawMessage(`{"invoice_id":"INV-2048","amount":500,"currency":"USD"}`),
	}
	_, err := persistEventWithOutbox(ctx, pool, req, "payment-INV-2048")
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

func TestCreateEventHandlerPostgresIntegration(t *testing.T) {
	pool := newIntegrationTestPool(t)

	handler := newCreateEventHandler(func(
		ctx context.Context,
		req CreateEventRequest,
		idempotencyKey string,
	) (PersistEventResult, error) {
		return persistEventWithOutbox(ctx, pool, req, idempotencyKey)
	})

	first := performCreateEventRequest(t, handler, `{"event_type":"invoice.paid","source":"payments-service","payload":{"invoice_id":"INV-2048","amount":500,"currency":"USD"}}`, "payment-INV-2048")
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("expected first request status %d, got %d", http.StatusCreated, first.StatusCode)
	}
	if !first.Body.Created || first.Body.ID == "" {
		t.Fatalf("expected first response to create event, got %#v", first.Body)
	}

	second := performCreateEventRequest(t, handler, `{"source":"payments-service","event_type":"invoice.paid","payload":{"currency":"USD","amount":500,"invoice_id":"INV-2048"}}`, "payment-INV-2048")
	if second.StatusCode != http.StatusOK {
		t.Fatalf("expected second request status %d, got %d", http.StatusOK, second.StatusCode)
	}
	if second.Body.Created {
		t.Fatalf("expected identical retry to return created=false, got %#v", second.Body)
	}
	if second.Body.ID != first.Body.ID {
		t.Fatalf("expected identical retry ID %q, got %q", first.Body.ID, second.Body.ID)
	}

	conflict := performRawCreateEventRequest(t, handler, `{"event_type":"invoice.paid","source":"payments-service","payload":{"invoice_id":"INV-2048","amount":600,"currency":"USD"}}`, "payment-INV-2048")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("expected conflict status %d, got %d with body %q", http.StatusConflict, conflict.Code, conflict.Body.String())
	}

	if got := countRows(t, pool, "events"); got != 1 {
		t.Fatalf("expected one event row, got %d", got)
	}
	if got := countRows(t, pool, "outbox"); got != 1 {
		t.Fatalf("expected one outbox row, got %d", got)
	}
}

func newIntegrationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("create admin PostgreSQL pool: %v", err)
	}

	schemaName := newSafeIntegrationSchemaName(t)
	quotedSchemaName := quotePostgresIdentifier(schemaName)
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchemaName); err != nil {
		adminPool.Close()
		t.Fatalf("create test schema %s: %v", schemaName, err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		if _, err := adminPool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchemaName+" CASCADE"); err != nil {
			t.Logf("drop test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_POSTGRES_DSN: %v", err)
	}
	config.MaxConns = 16
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO "+quotedSchemaName+", public")
		return err
	}

	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("create test PostgreSQL pool: %v", err)
	}

	applyMigrations(t, ctx, testPool)
	return testPool
}

func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	migrationsDir := findMigrationsDir(t)
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.sql"))
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatalf("no migration files found in %s", migrationsDir)
	}

	for _, file := range files {
		sqlBytes, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read migration %s: %v", filepath.Base(file), err)
		}
		if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("apply migration %s: %v", filepath.Base(file), err)
		}
	}
}

func findMigrationsDir(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}

	for {
		goMod := filepath.Join(dir, "go.mod")
		migrationsDir := filepath.Join(dir, "migrations")
		if fileExists(goMod) && dirExists(migrationsDir) {
			return migrationsDir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root containing go.mod and migrations")
		}
		dir = parent
	}
}

func newSafeIntegrationSchemaName(t *testing.T) string {
	t.Helper()

	var randomBytes [8]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		t.Fatalf("generate schema suffix: %v", err)
	}
	return fmt.Sprintf("eventrail_test_%d_%x", time.Now().UnixNano(), randomBytes[:])
}

func quotePostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
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

type createEventHTTPResult struct {
	StatusCode int
	Body       CreateEventResponse
}

func performCreateEventRequest(t *testing.T, handler http.Handler, body string, idempotencyKey string) createEventHTTPResult {
	t.Helper()

	rr := performRawCreateEventRequest(t, handler, body, idempotencyKey)
	var response CreateEventResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("decode create event response body %q: %v", rr.Body.String(), err)
	}
	return createEventHTTPResult{
		StatusCode: rr.Code,
		Body:       response,
	}
}

func performRawCreateEventRequest(t *testing.T, handler http.Handler, body string, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", idempotencyKey)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
