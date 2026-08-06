package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/krishgondaliya/eventrail-ingestion/internal/ingestion"
)

type recordingPersistEvent struct {
	result          ingestion.PersistResult
	err             error
	calls           int
	receivedCtx     context.Context
	receivedInput   ingestion.EventInput
	receivedKey     string
	failOnExtraCall bool
}

func (p *recordingPersistEvent) persist(
	ctx context.Context,
	input ingestion.EventInput,
	idempotencyKey string,
) (ingestion.PersistResult, error) {
	p.calls++
	p.receivedCtx = ctx
	p.receivedInput = input
	p.receivedKey = idempotencyKey

	if p.failOnExtraCall && p.calls > 1 {
		return ingestion.PersistResult{}, errors.New("persist called more than once")
	}

	return p.result, p.err
}

func TestCreateEventHandlerRejectsNonPost(t *testing.T) {
	persist := &recordingPersistEvent{}
	handler := NewCreateEventHandler(persist.persist)

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
	if allow := rr.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("expected Allow %q, got %q", http.MethodPost, allow)
	}
	if persist.calls != 0 {
		t.Fatalf("expected persist not to be called, got %d calls", persist.calls)
	}
}

func TestCreateEventHandlerValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "malformed JSON",
			body: `{"event_type":`,
		},
		{
			name: "missing event_type",
			body: `{"source":"billing","payload":{"amount":500}}`,
		},
		{
			name: "missing source",
			body: `{"event_type":"invoice.created","payload":{"amount":500}}`,
		},
		{
			name: "missing payload",
			body: `{"event_type":"invoice.created","source":"billing"}`,
		},
		{
			name: "null payload",
			body: `{"event_type":"invoice.created","source":"billing","payload":null}`,
		},
		{
			name: "two request JSON objects",
			body: `{"event_type":"invoice.created","source":"billing","payload":{}} {"event_type":"invoice.created","source":"billing","payload":{}}`,
		},
		{
			name: "invalid trailing text",
			body: `{"event_type":"invoice.created","source":"billing","payload":{}} nope`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			persist := &recordingPersistEvent{}
			handler := NewCreateEventHandler(persist.persist)

			req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(tt.body))
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
			}
			if persist.calls != 0 {
				t.Fatalf("expected persist not to be called, got %d calls", persist.calls)
			}
		})
	}
}

func TestCreateEventHandlerAcceptsTrailingWhitespace(t *testing.T) {
	persist := &recordingPersistEvent{
		result: ingestion.PersistResult{EventID: "event-new", Created: true},
	}
	handler := NewCreateEventHandler(persist.persist)

	body := "{\"event_type\":\"invoice.created\",\"source\":\"billing\",\"payload\":{}}\n\t  "
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, rr.Code)
	}
	if persist.calls != 1 {
		t.Fatalf("expected persist to be called once, got %d calls", persist.calls)
	}
}

func TestCreateEventHandlerAcceptsFalseyPayloads(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "zero",
			payload: `0`,
		},
		{
			name:    "false",
			payload: `false`,
		},
		{
			name:    "empty string",
			payload: `""`,
		},
		{
			name:    "empty object",
			payload: `{}`,
		},
		{
			name:    "empty array",
			payload: `[]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			persist := &recordingPersistEvent{
				result: ingestion.PersistResult{EventID: "event-new", Created: true},
			}
			handler := NewCreateEventHandler(persist.persist)

			body := `{"event_type":"invoice.created","source":"billing","payload":` + tt.payload + `}`
			req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusCreated {
				t.Fatalf("expected status %d, got %d", http.StatusCreated, rr.Code)
			}
			if persist.calls != 1 {
				t.Fatalf("expected persist to be called once, got %d calls", persist.calls)
			}
		})
	}
}

func TestCreateEventHandlerSuccess(t *testing.T) {
	tests := []struct {
		name        string
		result      ingestion.PersistResult
		wantStatus  int
		wantID      string
		wantCreated bool
	}{
		{
			name:        "new event",
			result:      ingestion.PersistResult{EventID: "event-new", Created: true},
			wantStatus:  http.StatusCreated,
			wantID:      "event-new",
			wantCreated: true,
		},
		{
			name:        "identical retry",
			result:      ingestion.PersistResult{EventID: "event-existing", Created: false},
			wantStatus:  http.StatusOK,
			wantID:      "event-existing",
			wantCreated: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			persist := &recordingPersistEvent{
				result:          tt.result,
				failOnExtraCall: true,
			}
			handler := NewCreateEventHandler(persist.persist)

			body := `{"event_type":"invoice.created","source":"billing","payload":{"amount":500}}`
			req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
			req.Header.Set("Idempotency-Key", " key-123 ")
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d", tt.wantStatus, rr.Code)
			}
			if contentType := rr.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
				t.Fatalf("expected JSON Content-Type, got %q", contentType)
			}

			var got CreateEventResponse
			if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
				t.Fatalf("response was not valid JSON: %v", err)
			}
			if got.ID != tt.wantID {
				t.Fatalf("expected id %q, got %q", tt.wantID, got.ID)
			}
			if got.Created != tt.wantCreated {
				t.Fatalf("expected created %v, got %v", tt.wantCreated, got.Created)
			}

			if persist.calls != 1 {
				t.Fatalf("expected persist to be called once, got %d calls", persist.calls)
			}
			if persist.receivedCtx == nil {
				t.Fatal("expected persist to receive request context")
			}
			if persist.receivedCtx != req.Context() {
				t.Fatal("expected persist to receive the original request context")
			}
			if persist.receivedKey != " key-123 " {
				t.Fatalf("expected idempotency key to be passed through, got %q", persist.receivedKey)
			}
			if persist.receivedInput.EventType != "invoice.created" {
				t.Fatalf("expected event_type invoice.created, got %q", persist.receivedInput.EventType)
			}
			if persist.receivedInput.Source != "billing" {
				t.Fatalf("expected source billing, got %q", persist.receivedInput.Source)
			}
			if string(persist.receivedInput.Payload) != `{"amount":500}` {
				t.Fatalf("expected payload to be parsed, got %s", string(persist.receivedInput.Payload))
			}
		})
	}
}

func TestCreateEventHandlerIdempotencyConflict(t *testing.T) {
	wrappedConflict := fmt.Errorf(
		"outer database detail event-internal request_hash abc123 payload secret: %w",
		fmt.Errorf("inner: %w", ingestion.ErrIdempotencyConflict),
	)
	persist := &recordingPersistEvent{err: wrappedConflict}
	handler := NewCreateEventHandler(persist.persist)

	body := `{"event_type":"invoice.created","source":"billing","payload":{"secret":"payload-value"}}`
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, rr.Code)
	}
	if persist.calls != 1 {
		t.Fatalf("expected persist to be called once, got %d calls", persist.calls)
	}

	responseBody := rr.Body.String()
	for _, forbidden := range []string{
		"payload-value",
		"abc123",
		"event-internal",
		"database detail",
	} {
		if strings.Contains(responseBody, forbidden) {
			t.Fatalf("conflict response leaked %q in %q", forbidden, responseBody)
		}
	}
}

func TestCreateEventHandlerInternalError(t *testing.T) {
	persist := &recordingPersistEvent{
		err: errors.New("database exploded with request_hash abc123 and payload secret"),
	}
	handler := NewCreateEventHandler(persist.persist)

	body := `{"event_type":"invoice.created","source":"billing","payload":{"secret":"payload-value"}}`
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rr.Code)
	}
	if persist.calls != 1 {
		t.Fatalf("expected persist to be called once, got %d calls", persist.calls)
	}

	responseBody := rr.Body.String()
	if !strings.Contains(responseBody, "failed to persist event") {
		t.Fatalf("expected generic persistence error, got %q", responseBody)
	}
	for _, forbidden := range []string{
		"database exploded",
		"request_hash",
		"abc123",
		"payload-value",
		"secret",
	} {
		if strings.Contains(responseBody, forbidden) {
			t.Fatalf("internal error response leaked %q in %q", forbidden, responseBody)
		}
	}
}
