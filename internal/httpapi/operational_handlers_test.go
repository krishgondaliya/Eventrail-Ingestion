package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/krishgondaliya/eventrail-ingestion/internal/operations"
)

type fakeOperationalStore struct {
	eventStatus operations.EventStatus
	eventErr    error
	listStatus  string
	listLimit   int
	listRecords []operations.DLQRecord
	dlqDetail   operations.DLQDetail
	dlqErr      error
	redrive     operations.RedriveResult
	redriveErr  error
	metrics     operations.MetricsSummary
}

func (s *fakeOperationalStore) EventStatus(context.Context, string) (operations.EventStatus, error) {
	return s.eventStatus, s.eventErr
}

func (s *fakeOperationalStore) ListDLQ(ctx context.Context, status string, limit int) ([]operations.DLQRecord, error) {
	s.listStatus = status
	s.listLimit = limit
	return s.listRecords, nil
}

func (s *fakeOperationalStore) DLQDetail(context.Context, string) (operations.DLQDetail, error) {
	return s.dlqDetail, s.dlqErr
}

func (s *fakeOperationalStore) RedriveDLQ(ctx context.Context, eventID string, publish operations.RedrivePublisher) (operations.RedriveResult, error) {
	if s.redriveErr != nil {
		return operations.RedriveResult{}, s.redriveErr
	}
	if _, err := publish(ctx, map[string]interface{}{"event_id": eventID}); err != nil {
		return operations.RedriveResult{}, err
	}
	return s.redrive, nil
}

func (s *fakeOperationalStore) MetricsSummary(context.Context) (operations.MetricsSummary, error) {
	return s.metrics, nil
}

func TestEventStatusHandlerReturnsStatus(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeOperationalStore{
		eventStatus: operations.EventStatus{
			Event: operations.EventMetadata{
				EventID:   "event-1",
				EventType: "invoice.paid",
				Source:    "payments",
			},
			CurrentStatus: operations.StatusDelivered,
			History: []operations.StatusHistoryEntry{{
				Status:    operations.StatusStored,
				Details:   json.RawMessage(`{}`),
				CreatedAt: now,
			}},
		},
	}

	rr := performOperationalRequest(NewEventStatusHandler(store), http.MethodGet, "/events/event-1/status")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	var got eventStatusResponse
	decodeJSONResponse(t, rr, &got)
	if got.EventID != "event-1" || got.CurrentStatus != operations.StatusDelivered || len(got.History) != 1 {
		t.Fatalf("unexpected event status response: %#v", got)
	}
}

func TestEventStatusHandlerMissingEventReturnsNotFound(t *testing.T) {
	store := &fakeOperationalStore{eventErr: operations.ErrEventNotFound}

	rr := performOperationalRequest(NewEventStatusHandler(store), http.MethodGet, "/events/missing/status")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestDLQListDefaultsToOpenRecords(t *testing.T) {
	store := &fakeOperationalStore{}

	rr := performOperationalRequest(NewDLQHandler(store, nil), http.MethodGet, "/dlq")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if store.listStatus != operations.DLQStatusOpen {
		t.Fatalf("expected status OPEN, got %q", store.listStatus)
	}
	if store.listLimit != 50 {
		t.Fatalf("expected limit 50, got %d", store.listLimit)
	}
}

func TestDLQListRejectsInvalidParameters(t *testing.T) {
	tests := []string{
		"/dlq?status=CLOSED",
		"/dlq?limit=0",
		"/dlq?limit=201",
		"/dlq?limit=many",
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			rr := performOperationalRequest(NewDLQHandler(&fakeOperationalStore{}, nil), http.MethodGet, path)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
			}
		})
	}
}

func TestDLQDetailContainsHistoryAndAttempts(t *testing.T) {
	store := &fakeOperationalStore{
		dlqDetail: operations.DLQDetail{
			Record: operations.DLQRecord{
				EventID:        "event-1",
				EventType:      "invoice.paid",
				Source:         "payments",
				Status:         operations.DLQStatusOpen,
				AttemptCount:   1,
				DeadLetteredAt: time.Now().UTC(),
			},
			History:          []operations.StatusHistoryEntry{{Status: operations.StatusDeadLettered, Details: json.RawMessage(`{}`)}},
			DeliveryAttempts: []operations.DeliveryAttempt{{AttemptNumber: 1, Outcome: operations.DeliveryOutcomeFailed}},
		},
	}

	rr := performOperationalRequest(NewDLQHandler(store, nil), http.MethodGet, "/dlq/event-1")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	var got dlqDetailResponse
	decodeJSONResponse(t, rr, &got)
	if got.Record.EventID != "event-1" || len(got.History) != 1 || len(got.DeliveryAttempts) != 1 {
		t.Fatalf("unexpected DLQ detail response: %#v", got)
	}
}

func TestDLQRedriveResponses(t *testing.T) {
	tests := []struct {
		name       string
		redriveErr error
		wantStatus int
	}{
		{name: "not found", redriveErr: operations.ErrDLQNotFound, wantStatus: http.StatusNotFound},
		{name: "not open", redriveErr: operations.ErrDLQNotOpen, wantStatus: http.StatusConflict},
		{name: "publish failure", redriveErr: errors.New("redis down"), wantStatus: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeOperationalStore{redriveErr: tt.redriveErr}
			rr := performOperationalRequest(NewDLQHandler(store, func(context.Context, map[string]interface{}) (string, error) {
				return "", errors.New("redis down")
			}), http.MethodPost, "/dlq/event-1/redrive")
			if rr.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d", tt.wantStatus, rr.Code)
			}
		})
	}
}

func TestDLQRedriveSuccess(t *testing.T) {
	store := &fakeOperationalStore{
		redrive: operations.RedriveResult{EventID: "event-1", Status: operations.DLQStatusRedriven, StreamID: "1-0"},
	}
	rr := performOperationalRequest(NewDLQHandler(store, func(context.Context, map[string]interface{}) (string, error) {
		return "1-0", nil
	}), http.MethodPost, "/dlq/event-1/redrive")

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, rr.Code)
	}
	var got redriveResponse
	decodeJSONResponse(t, rr, &got)
	if got.EventID != "event-1" || got.Status != operations.DLQStatusRedriven || got.StreamID != "1-0" {
		t.Fatalf("unexpected redrive response: %#v", got)
	}
}

func TestMetricsSummaryHandler(t *testing.T) {
	store := &fakeOperationalStore{
		metrics: operations.MetricsSummary{
			TotalEvents:        10,
			PendingPublication: 2,
			Delivered:          6,
			Retrying:           1,
			OpenDLQ:            1,
			Redriven:           3,
		},
	}

	rr := performOperationalRequest(NewMetricsSummaryHandler(store), http.MethodGet, "/metrics/summary")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	var got metricsSummaryResponse
	decodeJSONResponse(t, rr, &got)
	if got.TotalEvents != 10 || got.PendingPublication != 2 || got.Delivered != 6 || got.Retrying != 1 || got.OpenDLQ != 1 || got.Redriven != 3 {
		t.Fatalf("unexpected metrics summary response: %#v", got)
	}
}

func TestOperationalGetHandlersRejectNonGet(t *testing.T) {
	tests := []struct {
		name    string
		handler http.Handler
		path    string
	}{
		{name: "event status", handler: NewEventStatusHandler(&fakeOperationalStore{}), path: "/events/event-1/status"},
		{name: "DLQ list", handler: NewDLQHandler(&fakeOperationalStore{}, nil), path: "/dlq"},
		{name: "DLQ detail", handler: NewDLQHandler(&fakeOperationalStore{}, nil), path: "/dlq/event-1"},
		{name: "metrics", handler: NewMetricsSummaryHandler(&fakeOperationalStore{}), path: "/metrics/summary"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := performOperationalRequest(tt.handler, http.MethodPost, tt.path)
			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
			}
			if allow := rr.Header().Get("Allow"); allow != http.MethodGet {
				t.Fatalf("expected Allow GET, got %q", allow)
			}
		})
	}
}

func TestDLQRedriveRejectsNonPost(t *testing.T) {
	rr := performOperationalRequest(NewDLQHandler(&fakeOperationalStore{}, nil), http.MethodGet, "/dlq/event-1/redrive")

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
	if allow := rr.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("expected Allow POST, got %q", allow)
	}
}

func performOperationalRequest(handler http.Handler, method string, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func decodeJSONResponse(t *testing.T, rr *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if contentType := rr.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected JSON Content-Type, got %q", contentType)
	}
	if err := json.NewDecoder(rr.Body).Decode(dst); err != nil {
		t.Fatalf("decode response %q: %v", rr.Body.String(), err)
	}
}
