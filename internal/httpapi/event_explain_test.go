package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/krishgondaliya/eventrail-ingestion/internal/operations"
)

const (
	explainEventID      = "11111111-1111-4111-8111-111111111111"
	explainMissingID    = "22222222-2222-4222-8222-222222222222"
	explainSensitiveURL = "http://private.example/receipt"
)

type fakeExplainClient struct {
	request  explainRequest
	response explainResponse
	err      error
	calls    int
}

func (c *fakeExplainClient) Explain(_ context.Context, request explainRequest) (explainResponse, error) {
	c.calls++
	c.request = request
	if c.err != nil {
		return explainResponse{}, c.err
	}
	return c.response, nil
}

func TestExplainRequestFromStoreConstructsAuthoritativeSnapshots(t *testing.T) {
	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		status        operations.EventStatus
		dlq           operations.DLQDetail
		dlqErr        error
		wantCurrent   string
		wantOutcomes  []string
		wantRetry     int
		wantDLQ       bool
		wantRedrives  int
		wantDelivered bool
	}{
		{
			name: "healthy first attempt",
			status: eventStatusFixture([]operations.StatusHistoryEntry{
				historyAt(operations.StatusStored, base, nil),
				historyAt(operations.StatusPendingPublication, base.Add(time.Second), nil),
				historyAt(operations.StatusPublished, base.Add(2*time.Second), nil),
				historyAt(operations.StatusProcessing, base.Add(3*time.Second), nil),
				historyAt(operations.StatusDelivered, base.Add(4*time.Second), nil),
			}, []operations.DeliveryAttempt{
				attemptAt(1, operations.DeliveryOutcomeSucceeded, intPtr(200), nil, base.Add(4*time.Second)),
			}),
			dlqErr:        operations.ErrDLQNotFound,
			wantCurrent:   operations.StatusDelivered,
			wantOutcomes:  []string{"success"},
			wantRetry:     0,
			wantDLQ:       false,
			wantRedrives:  0,
			wantDelivered: true,
		},
		{
			name: "temporary failure followed by delivery",
			status: eventStatusFixture([]operations.StatusHistoryEntry{
				historyAt(operations.StatusStored, base, nil),
				historyAt(operations.StatusProcessing, base.Add(time.Second), nil),
				historyAt(operations.StatusRetrying, base.Add(2*time.Second), map[string]any{"next_retry": 1}),
				historyAt(operations.StatusProcessing, base.Add(3*time.Second), nil),
				historyAt(operations.StatusDelivered, base.Add(4*time.Second), nil),
			}, []operations.DeliveryAttempt{
				attemptAt(1, operations.DeliveryOutcomeFailed, intPtr(503), stringPtr("retryable delivery failure status=503: 503 Service Unavailable"), base.Add(2*time.Second)),
				attemptAt(2, operations.DeliveryOutcomeSucceeded, intPtr(200), nil, base.Add(4*time.Second)),
			}),
			dlqErr:        operations.ErrDLQNotFound,
			wantCurrent:   operations.StatusDelivered,
			wantOutcomes:  []string{"temporary_failure", "success"},
			wantRetry:     1,
			wantDLQ:       false,
			wantRedrives:  0,
			wantDelivered: true,
		},
		{
			name: "DLQ validation failure",
			status: eventStatusFixture([]operations.StatusHistoryEntry{
				historyAt(operations.StatusStored, base, nil),
				historyAt(operations.StatusProcessing, base.Add(time.Second), nil),
				historyAt(operations.StatusDeadLettered, base.Add(2*time.Second), nil),
			}, []operations.DeliveryAttempt{
				attemptAt(1, operations.DeliveryOutcomeFailed, intPtr(400), stringPtr("permanent delivery failure status=400: Required field invoice_id was missing"), base.Add(2*time.Second)),
			}),
			dlq:           dlqFixture(nil),
			wantCurrent:   operations.StatusDeadLettered,
			wantOutcomes:  []string{"permanent_failure"},
			wantRetry:     0,
			wantDLQ:       true,
			wantRedrives:  0,
			wantDelivered: false,
		},
		{
			name: "redriven and delivered",
			status: eventStatusFixture([]operations.StatusHistoryEntry{
				historyAt(operations.StatusStored, base, nil),
				historyAt(operations.StatusDeadLettered, base.Add(time.Second), nil),
				historyAt(operations.StatusRedriven, base.Add(2*time.Second), nil),
				historyAt(operations.StatusProcessing, base.Add(3*time.Second), nil),
				historyAt(operations.StatusDelivered, base.Add(4*time.Second), nil),
			}, []operations.DeliveryAttempt{
				attemptAt(1, operations.DeliveryOutcomeFailed, intPtr(400), stringPtr("permanent delivery failure status=400"), base.Add(time.Second)),
				attemptAt(2, operations.DeliveryOutcomeSucceeded, intPtr(200), nil, base.Add(4*time.Second)),
			}),
			dlq:           dlqFixture(timePtr(base.Add(2 * time.Second))),
			wantCurrent:   operations.StatusDelivered,
			wantOutcomes:  []string{"permanent_failure", "success"},
			wantRetry:     0,
			wantDLQ:       true,
			wantRedrives:  1,
			wantDelivered: true,
		},
		{
			name: "redriven but not delivered",
			status: eventStatusFixture([]operations.StatusHistoryEntry{
				historyAt(operations.StatusStored, base, nil),
				historyAt(operations.StatusDeadLettered, base.Add(time.Second), nil),
				historyAt(operations.StatusRedriven, base.Add(2*time.Second), nil),
				historyAt(operations.StatusProcessing, base.Add(3*time.Second), nil),
			}, []operations.DeliveryAttempt{
				attemptAt(1, operations.DeliveryOutcomeFailed, intPtr(400), stringPtr("permanent delivery failure status=400"), base.Add(time.Second)),
			}),
			dlq:           dlqFixture(timePtr(base.Add(2 * time.Second))),
			wantCurrent:   operations.StatusProcessing,
			wantOutcomes:  []string{"permanent_failure"},
			wantRetry:     0,
			wantDLQ:       true,
			wantRedrives:  1,
			wantDelivered: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeOperationalStore{
				eventStatus: tt.status,
				dlqDetail:   tt.dlq,
				dlqErr:      tt.dlqErr,
			}
			got, err := explainRequestFromStore(context.Background(), store, explainEventID)
			if err != nil {
				t.Fatalf("explainRequestFromStore returned error: %v", err)
			}
			if got.CurrentStatus != tt.wantCurrent || got.RetryCount != tt.wantRetry ||
				got.EnteredDLQ != tt.wantDLQ || got.RedriveCount != tt.wantRedrives || got.Delivered != tt.wantDelivered {
				t.Fatalf("unexpected snapshot facts: %#v", got)
			}
			if got.EventType != "webhook" || got.Source != "Payment Service" || got.Destination != "Receipt Service" {
				t.Fatalf("unexpected event metadata: %#v", got)
			}
			if got.BusinessEventType == nil || *got.BusinessEventType != "invoice.paid" {
				t.Fatalf("expected business event type invoice.paid, got %#v", got.BusinessEventType)
			}
			if len(got.StatusHistory) != len(tt.status.History) {
				t.Fatalf("expected %d history entries, got %d", len(tt.status.History), len(got.StatusHistory))
			}
			assertAttemptOutcomes(t, got.DeliveryAttempts, tt.wantOutcomes)
		})
	}
}

func TestExplainRequestBoundsHistoryPreservingImportantTransitions(t *testing.T) {
	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	history := []operations.StatusHistoryEntry{historyAt(operations.StatusStored, base, nil)}
	for i := 1; i <= 24; i++ {
		history = append(history, historyAt(operations.StatusProcessing, base.Add(time.Duration(i)*time.Second), nil))
	}
	history = append(history,
		historyAt(operations.StatusDeadLettered, base.Add(25*time.Second), nil),
		historyAt(operations.StatusRedriven, base.Add(26*time.Second), nil),
		historyAt(operations.StatusDelivered, base.Add(27*time.Second), nil),
	)
	store := &fakeOperationalStore{
		eventStatus: eventStatusFixture(history, []operations.DeliveryAttempt{
			attemptAt(1, operations.DeliveryOutcomeSucceeded, intPtr(200), nil, base.Add(27*time.Second)),
		}),
		dlqDetail: dlqFixture(timePtr(base.Add(26 * time.Second))),
	}

	got, err := explainRequestFromStore(context.Background(), store, explainEventID)
	if err != nil {
		t.Fatalf("explainRequestFromStore returned error: %v", err)
	}
	if len(got.StatusHistory) != maxExplainHistoryItems {
		t.Fatalf("expected bounded history length %d, got %d", maxExplainHistoryItems, len(got.StatusHistory))
	}
	for _, status := range []string{operations.StatusStored, operations.StatusDeadLettered, operations.StatusRedriven, operations.StatusDelivered} {
		if !explainHistoryContains(got.StatusHistory, status) {
			t.Fatalf("expected bounded history to preserve %s in %#v", status, got.StatusHistory)
		}
	}
}

func TestEventExplainOutboundJSONExcludesSensitivePayloadAndErrors(t *testing.T) {
	responseCode := 400
	store := &fakeOperationalStore{
		eventStatus: operations.EventStatus{
			Event: operations.EventMetadata{
				EventID:   explainEventID,
				EventType: "webhook",
				Source:    "Payment Service",
				Payload: json.RawMessage(`{
					"url":"http://private.example/receipt",
					"headers":{"Authorization":"Bearer token"},
					"idempotency_key":"secret-idempotency-key",
					"data":{
						"business_event_type":"invoice.paid",
						"invoice_id":"INV-SECRET-999",
						"amount":734,
						"currency":"USD",
						"account_id":"acct-secret",
						"customer_id":"cust-secret",
						"payload":{"raw_logs":"stack trace"}
					}
				}`),
			},
			CurrentStatus: operations.StatusDeadLettered,
			History: []operations.StatusHistoryEntry{
				historyAt(operations.StatusStored, time.Now().UTC(), nil),
				historyAt(operations.StatusDeadLettered, time.Now().UTC().Add(time.Second), nil),
			},
			DeliveryAttempts: []operations.DeliveryAttempt{{
				AttemptNumber: 1,
				Outcome:       operations.DeliveryOutcomeFailed,
				ResponseCode:  &responseCode,
				Error:         stringPtr(`permanent delivery failure status=400: Required field invoice_id was missing at http://private.example/receipt with secret-idempotency-key`),
			}},
		},
		dlqDetail: dlqFixture(nil),
	}
	var outbound string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/explain" {
			t.Fatalf("expected /explain path, got %s", r.URL.Path)
		}
		var err error
		outbound, err = readRequestBody(r)
		if err != nil {
			t.Fatalf("read outbound request: %v", err)
		}
		writeJSON(w, http.StatusOK, validExplainResponse("deterministic", nil))
	}))
	defer server.Close()

	rr := performOperationalRequest(
		NewEventExplainHandler(store, NewAITriageClient(server.URL, server.Client())),
		http.MethodPost,
		"/events/"+explainEventID+"/explain",
	)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	for _, forbidden := range []string{
		"invoice_id",
		"INV-SECRET-999",
		"amount",
		"734",
		"currency",
		"USD",
		"payload",
		"private.example",
		"headers",
		"Authorization",
		"idempotency_key",
		"secret-idempotency-key",
		"account_id",
		"customer_id",
		"raw_logs",
		"stack trace",
	} {
		if strings.Contains(outbound, forbidden) {
			t.Fatalf("explain request exposed forbidden value %q in %s", forbidden, outbound)
		}
	}
}

func TestEventExplainHandlerBehavior(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		store      *fakeOperationalStore
		client     *fakeExplainClient
		wantStatus int
	}{
		{
			name:       "valid event",
			path:       "/events/" + explainEventID + "/explain",
			store:      readyExplainStore(),
			client:     &fakeExplainClient{response: validExplainResponse("deterministic", nil)},
			wantStatus: http.StatusOK,
		},
		{
			name:       "malformed event ID",
			path:       "/events/not-a-uuid/explain",
			store:      readyExplainStore(),
			client:     &fakeExplainClient{response: validExplainResponse("deterministic", nil)},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing event",
			path:       "/events/" + explainMissingID + "/explain",
			store:      &fakeOperationalStore{eventErr: operations.ErrEventNotFound},
			client:     &fakeExplainClient{response: validExplainResponse("deterministic", nil)},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "insufficient history",
			path: "/events/" + explainEventID + "/explain",
			store: &fakeOperationalStore{
				eventStatus: operations.EventStatus{
					Event:         operations.EventMetadata{EventID: explainEventID, EventType: "webhook", Source: "Payment Service"},
					CurrentStatus: operations.StatusStored,
				},
				dlqErr: operations.ErrDLQNotFound,
			},
			client:     &fakeExplainClient{response: validExplainResponse("deterministic", nil)},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "AI unavailable",
			path:       "/events/" + explainEventID + "/explain",
			store:      readyExplainStore(),
			client:     &fakeExplainClient{err: ErrAIServiceUnavailable},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "AI invalid response",
			path:       "/events/" + explainEventID + "/explain",
			store:      readyExplainStore(),
			client:     &fakeExplainClient{err: ErrAIServiceInvalidResponse},
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "valid LLM response with model",
			path:       "/events/" + explainEventID + "/explain",
			store:      readyExplainStore(),
			client:     &fakeExplainClient{response: validExplainResponse("openai", stringPtr("gpt-5-test"))},
			wantStatus: http.StatusOK,
		},
		{
			name:       "nil client",
			path:       "/events/" + explainEventID + "/explain",
			store:      readyExplainStore(),
			client:     nil,
			wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client EventExplainClient
			if tt.client != nil {
				client = tt.client
			}
			rr := performOperationalRequest(NewEventExplainHandler(tt.store, client), http.MethodPost, tt.path)
			if rr.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d: %s", tt.wantStatus, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestEventExplainRejectsNonPost(t *testing.T) {
	rr := performOperationalRequest(NewEventExplainHandler(readyExplainStore(), &fakeExplainClient{}), http.MethodGet, "/events/"+explainEventID+"/explain")

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
	if allow := rr.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("expected Allow POST, got %q", allow)
	}
}

func TestAITriageClientExplainHandlesServiceFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    error
	}{
		{name: "non 2xx", statusCode: http.StatusInternalServerError, body: `{}`, wantErr: ErrAIServiceUnavailable},
		{name: "invalid JSON", statusCode: http.StatusOK, body: `{`, wantErr: ErrAIServiceInvalidResponse},
		{name: "invalid schema", statusCode: http.StatusOK, body: `{"headline":""}`, wantErr: ErrAIServiceInvalidResponse},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/explain" {
					t.Fatalf("expected /explain path, got %s", r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := NewAITriageClient(server.URL, server.Client())
			_, err := client.Explain(context.Background(), minimalExplainRequest())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected error %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestAITriageClientExplainTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(25 * time.Millisecond)
		writeJSON(w, http.StatusOK, validExplainResponse("deterministic", nil))
	}))
	defer server.Close()

	client := NewAITriageClient(server.URL, &http.Client{Timeout: time.Nanosecond})
	_, err := client.Explain(context.Background(), minimalExplainRequest())
	if !errors.Is(err, ErrAIServiceUnavailable) {
		t.Fatalf("expected unavailable error, got %v", err)
	}
}

func TestAITriageClientExplainAcceptsValidResponses(t *testing.T) {
	tests := []struct {
		name     string
		response explainResponse
	}{
		{name: "deterministic null model", response: validExplainResponse("deterministic", nil)},
		{name: "LLM model", response: validExplainResponse("openai", stringPtr("gpt-5-test"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, tt.response)
			}))
			defer server.Close()

			client := NewAITriageClient(server.URL, server.Client())
			got, err := client.Explain(context.Background(), minimalExplainRequest())
			if err != nil {
				t.Fatalf("Explain returned error: %v", err)
			}
			if got.Provider != tt.response.Provider || stringPointerValue(got.Model) != stringPointerValue(tt.response.Model) {
				t.Fatalf("unexpected response metadata: %#v", got)
			}
		})
	}
}

func TestEventExplainFailureDoesNotMutateOperationalState(t *testing.T) {
	store := readyExplainStore()
	beforeStatus := store.eventStatus.CurrentStatus
	beforeAttemptCount := len(store.eventStatus.DeliveryAttempts)
	beforeDLQStatus := store.dlqDetail.Record.Status
	client := &fakeExplainClient{err: ErrAIServiceUnavailable}

	rr := performOperationalRequest(NewEventExplainHandler(store, client), http.MethodPost, "/events/"+explainEventID+"/explain")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
	if store.eventStatus.CurrentStatus != beforeStatus ||
		len(store.eventStatus.DeliveryAttempts) != beforeAttemptCount ||
		store.dlqDetail.Record.Status != beforeDLQStatus ||
		store.redriveCalls != 0 {
		t.Fatalf("explanation failure mutated operational state: %#v", store)
	}
}

func eventStatusFixture(history []operations.StatusHistoryEntry, attempts []operations.DeliveryAttempt) operations.EventStatus {
	currentStatus := ""
	if len(history) > 0 {
		currentStatus = history[len(history)-1].Status
	}
	return operations.EventStatus{
		Event: operations.EventMetadata{
			EventID:   explainEventID,
			EventType: "webhook",
			Source:    "Payment Service",
			Payload: json.RawMessage(`{
				"url":"` + explainSensitiveURL + `",
				"data":{
					"business_event_type":"invoice.paid",
					"invoice_id":"INV-SECRET-999",
					"amount":734,
					"currency":"USD"
				}
			}`),
		},
		CurrentStatus:    currentStatus,
		History:          history,
		DeliveryAttempts: attempts,
	}
}

func readyExplainStore() *fakeOperationalStore {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	return &fakeOperationalStore{
		eventStatus: eventStatusFixture([]operations.StatusHistoryEntry{
			historyAt(operations.StatusStored, now, nil),
			historyAt(operations.StatusDelivered, now.Add(time.Second), nil),
		}, []operations.DeliveryAttempt{
			attemptAt(1, operations.DeliveryOutcomeSucceeded, intPtr(200), nil, now.Add(time.Second)),
		}),
		dlqErr: operations.ErrDLQNotFound,
	}
}

func dlqFixture(redrivenAt *time.Time) operations.DLQDetail {
	return operations.DLQDetail{
		Record: operations.DLQRecord{
			EventID:        explainEventID,
			EventType:      "webhook",
			Source:         "Payment Service",
			AttemptCount:   1,
			Status:         operations.DLQStatusOpen,
			DeadLetteredAt: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
			RedrivenAt:     redrivenAt,
		},
	}
}

func historyAt(status string, at time.Time, details map[string]any) operations.StatusHistoryEntry {
	detailsJSON := json.RawMessage(`{}`)
	if details != nil {
		body, err := json.Marshal(details)
		if err != nil {
			panic(err)
		}
		detailsJSON = body
	}
	return operations.StatusHistoryEntry{Status: status, Details: detailsJSON, CreatedAt: at}
}

func attemptAt(number int, outcome string, responseCode *int, message *string, completedAt time.Time) operations.DeliveryAttempt {
	return operations.DeliveryAttempt{
		AttemptNumber: number,
		Outcome:       outcome,
		ResponseCode:  responseCode,
		Error:         message,
		StartedAt:     completedAt.Add(-100 * time.Millisecond),
		CompletedAt:   completedAt,
	}
}

func assertAttemptOutcomes(t *testing.T, attempts []explainDeliveryAttempt, want []string) {
	t.Helper()
	if len(attempts) != len(want) {
		t.Fatalf("expected %d attempts, got %d", len(want), len(attempts))
	}
	for i, outcome := range want {
		if attempts[i].Outcome != outcome {
			t.Fatalf("attempt %d expected outcome %q, got %q", i, outcome, attempts[i].Outcome)
		}
	}
}

func explainHistoryContains(history []explainStatusHistory, status string) bool {
	for _, entry := range history {
		if entry.Status == status {
			return true
		}
	}
	return false
}

func minimalExplainRequest() explainRequest {
	return explainRequest{
		EventType:     "webhook",
		Source:        "Payment Service",
		Destination:   "Receipt Service",
		CurrentStatus: operations.StatusDelivered,
		StatusHistory: []explainStatusHistory{{Status: operations.StatusStored}},
		DeliveryAttempts: []explainDeliveryAttempt{{
			AttemptNumber: 1,
			HTTPStatus:    intPtr(200),
			Outcome:       "success",
		}},
		Delivered: true,
	}
}

func validExplainResponse(provider string, model *string) explainResponse {
	mode := "deterministic_runbook"
	if provider != "deterministic" {
		mode = "llm_grounded"
	}
	return explainResponse{
		Headline:           "Receipt delivered successfully",
		WhatHappened:       "The event was stored and delivered.",
		BusinessImpact:     "The receipt workflow completed.",
		NextAction:         "No recovery action is needed.",
		RecommendedActions: []string{"Keep the event ID for audit."},
		RecoveryStatus:     "not_needed",
		Evidence: []explainEvidence{{
			Type:        "delivery_outcome",
			Description: "Event reached Delivered.",
		}},
		Citations: []triageCitation{{
			RunbookID:  "receipt-validation-v1",
			ChunkID:    "receipt-validation-v1/symptoms",
			Title:      "Receipt Validation Failures",
			SourcePath: "receipt-validation.md",
		}},
		AnalysisMode: mode,
		Provider:     provider,
		Model:        model,
	}
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func intPtr(value int) *int {
	return &value
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func readRequestBody(r *http.Request) (string, error) {
	var body strings.Builder
	if _, err := io.Copy(&body, r.Body); err != nil {
		return "", err
	}
	return body.String(), nil
}
