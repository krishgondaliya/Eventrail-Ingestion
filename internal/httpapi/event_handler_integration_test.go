package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krishgondaliya/eventrail-ingestion/internal/ingestion"
	"github.com/krishgondaliya/eventrail-ingestion/internal/testutil"
)

func TestCreateEventHandlerPostgresIntegration(t *testing.T) {
	pool := testutil.NewPostgresPool(t)

	handler := NewCreateEventHandler(func(
		ctx context.Context,
		input ingestion.EventInput,
		idempotencyKey string,
	) (ingestion.PersistResult, error) {
		return ingestion.PersistEventWithOutbox(ctx, pool, input, idempotencyKey)
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

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()

	var count int
	query := `SELECT count(*) FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`
	if err := pool.QueryRow(context.Background(), query).Scan(&count); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	return count
}
