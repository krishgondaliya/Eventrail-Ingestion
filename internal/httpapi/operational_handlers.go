package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/krishgondaliya/eventrail-ingestion/internal/operations"
)

type EventStatusReader interface {
	EventStatus(context.Context, string) (operations.EventStatus, error)
}

type DLQStore interface {
	ListDLQ(context.Context, string, int) ([]operations.DLQRecord, error)
	DLQDetail(context.Context, string) (operations.DLQDetail, error)
	RedriveDLQ(context.Context, string, operations.RedrivePublisher) (operations.RedriveResult, error)
}

type DLQTriageClient interface {
	Triage(context.Context, triageRequest) (triageResponse, error)
}

type MetricsReader interface {
	MetricsSummary(context.Context) (operations.MetricsSummary, error)
}

func NewEventStatusHandler(store EventStatusReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireGet(w, r) {
			return
		}

		cleanPath := strings.TrimRight(r.URL.Path, "/")
		eventID := strings.TrimSuffix(strings.TrimPrefix(cleanPath, "/events/"), "/status")
		eventID = strings.Trim(eventID, "/")
		if eventID == "" {
			http.Error(w, "event id required", http.StatusBadRequest)
			return
		}

		status, err := store.EventStatus(r.Context(), eventID)
		if errors.Is(err, operations.ErrEventNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "failed to fetch event status", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, eventStatusResponseFromStore(status))
	})
}

func NewDLQHandler(store DLQStore, publish operations.RedrivePublisher, triageClient DLQTriageClient) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/dlq"), "/")
		if path == "" {
			handleDLQList(w, r, store)
			return
		}

		parts := strings.Split(path, "/")
		if len(parts) == 1 {
			handleDLQDetail(w, r, store, parts[0])
			return
		}
		if len(parts) == 2 && parts[1] == "redrive" {
			handleDLQRedrive(w, r, store, publish, parts[0])
			return
		}
		if len(parts) == 2 && parts[1] == "triage" {
			handleDLQTriage(w, r, store, triageClient, parts[0])
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
}

func NewMetricsSummaryHandler(store MetricsReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireGet(w, r) {
			return
		}

		summary, err := store.MetricsSummary(r.Context())
		if err != nil {
			http.Error(w, "failed to fetch metrics summary", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, metricsSummaryResponse{
			TotalEvents:        summary.TotalEvents,
			PendingPublication: summary.PendingPublication,
			Delivered:          summary.Delivered,
			Retrying:           summary.Retrying,
			OpenDLQ:            summary.OpenDLQ,
			Redriven:           summary.Redriven,
		})
	})
}

func handleDLQList(w http.ResponseWriter, r *http.Request, store DLQStore) {
	if !requireGet(w, r) {
		return
	}

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = operations.DLQStatusOpen
	}
	if status != operations.DLQStatusOpen && status != operations.DLQStatusRedriven {
		http.Error(w, "invalid DLQ status", http.StatusBadRequest)
		return
	}

	limit := 50
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > 200 {
			http.Error(w, "limit must be between 1 and 200", http.StatusBadRequest)
			return
		}
		limit = parsed
	}

	records, err := store.ListDLQ(r.Context(), status, limit)
	if err != nil {
		http.Error(w, "failed to list DLQ records", http.StatusInternalServerError)
		return
	}

	response := make([]dlqRecordResponse, 0, len(records))
	for _, record := range records {
		response = append(response, dlqRecordResponseFromStore(record))
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": response})
}

func handleDLQDetail(w http.ResponseWriter, r *http.Request, store DLQStore, eventID string) {
	if !requireGet(w, r) {
		return
	}

	detail, err := store.DLQDetail(r.Context(), eventID)
	if errors.Is(err, operations.ErrDLQNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to fetch DLQ record", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, dlqDetailResponse{
		Record:           dlqRecordResponseFromStore(detail.Record),
		History:          statusHistoryResponseFromStore(detail.History),
		DeliveryAttempts: deliveryAttemptResponseFromStore(detail.DeliveryAttempts),
	})
}

func handleDLQRedrive(w http.ResponseWriter, r *http.Request, store DLQStore, publish operations.RedrivePublisher, eventID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	result, err := store.RedriveDLQ(r.Context(), eventID, publish)
	if errors.Is(err, operations.ErrDLQNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if errors.Is(err, operations.ErrDLQNotOpen) {
		http.Error(w, "DLQ record is not open", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "failed to redrive DLQ record", http.StatusServiceUnavailable)
		return
	}

	writeJSON(w, http.StatusAccepted, redriveResponse{
		EventID:  result.EventID,
		Status:   result.Status,
		StreamID: result.StreamID,
	})
}

func handleDLQTriage(w http.ResponseWriter, r *http.Request, store DLQStore, triageClient DLQTriageClient, eventID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if triageClient == nil {
		http.Error(w, "AI triage service is not configured", http.StatusServiceUnavailable)
		return
	}

	detail, err := store.DLQDetail(r.Context(), eventID)
	if errors.Is(err, operations.ErrDLQNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to fetch DLQ record", http.StatusInternalServerError)
		return
	}

	triage, err := triageClient.Triage(r.Context(), triageRequestFromDLQDetail(detail))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "TRIAGE_UNAVAILABLE"})
		return
	}
	writeJSON(w, http.StatusOK, triage)
}

type eventStatusResponse struct {
	EventID          string                    `json:"event_id"`
	EventType        string                    `json:"event_type"`
	Source           string                    `json:"source"`
	CurrentStatus    string                    `json:"current_status"`
	History          []statusHistoryResponse   `json:"history"`
	DeliveryAttempts []deliveryAttemptResponse `json:"delivery_attempts"`
}

type statusHistoryResponse struct {
	Status    string          `json:"status"`
	Details   json.RawMessage `json:"details"`
	CreatedAt time.Time       `json:"created_at"`
}

type deliveryAttemptResponse struct {
	AttemptNumber int       `json:"attempt_number"`
	Outcome       string    `json:"outcome"`
	ResponseCode  *int      `json:"response_code"`
	Error         *string   `json:"error"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at"`
}

type dlqRecordResponse struct {
	EventID        string     `json:"event_id"`
	EventType      string     `json:"event_type"`
	Source         string     `json:"source"`
	AttemptCount   int        `json:"attempt_count"`
	LastError      *string    `json:"last_error"`
	Status         string     `json:"status"`
	DeadLetteredAt time.Time  `json:"dead_lettered_at"`
	RedrivenAt     *time.Time `json:"redriven_at"`
}

type dlqDetailResponse struct {
	Record           dlqRecordResponse         `json:"record"`
	History          []statusHistoryResponse   `json:"history"`
	DeliveryAttempts []deliveryAttemptResponse `json:"delivery_attempts"`
}

type redriveResponse struct {
	EventID  string `json:"event_id"`
	Status   string `json:"status"`
	StreamID string `json:"stream_id"`
}

type metricsSummaryResponse struct {
	TotalEvents        int `json:"total_events"`
	PendingPublication int `json:"pending_publication"`
	Delivered          int `json:"delivered"`
	Retrying           int `json:"retrying"`
	OpenDLQ            int `json:"open_dlq"`
	Redriven           int `json:"redriven"`
}

func eventStatusResponseFromStore(status operations.EventStatus) eventStatusResponse {
	return eventStatusResponse{
		EventID:          status.Event.EventID,
		EventType:        status.Event.EventType,
		Source:           status.Event.Source,
		CurrentStatus:    status.CurrentStatus,
		History:          statusHistoryResponseFromStore(status.History),
		DeliveryAttempts: deliveryAttemptResponseFromStore(status.DeliveryAttempts),
	}
}

func statusHistoryResponseFromStore(history []operations.StatusHistoryEntry) []statusHistoryResponse {
	response := make([]statusHistoryResponse, 0, len(history))
	for _, entry := range history {
		response = append(response, statusHistoryResponse{
			Status:    entry.Status,
			Details:   entry.Details,
			CreatedAt: entry.CreatedAt,
		})
	}
	return response
}

func deliveryAttemptResponseFromStore(attempts []operations.DeliveryAttempt) []deliveryAttemptResponse {
	response := make([]deliveryAttemptResponse, 0, len(attempts))
	for _, attempt := range attempts {
		response = append(response, deliveryAttemptResponse{
			AttemptNumber: attempt.AttemptNumber,
			Outcome:       attempt.Outcome,
			ResponseCode:  attempt.ResponseCode,
			Error:         attempt.Error,
			StartedAt:     attempt.StartedAt,
			CompletedAt:   attempt.CompletedAt,
		})
	}
	return response
}

func dlqRecordResponseFromStore(record operations.DLQRecord) dlqRecordResponse {
	return dlqRecordResponse{
		EventID:        record.EventID,
		EventType:      record.EventType,
		Source:         record.Source,
		AttemptCount:   record.AttemptCount,
		LastError:      record.LastError,
		Status:         record.Status,
		DeadLetteredAt: record.DeadLetteredAt,
		RedrivenAt:     record.RedrivenAt,
	}
}

func triageRequestFromDLQDetail(detail operations.DLQDetail) triageRequest {
	request := triageRequest{
		EventType:    detail.Record.EventType,
		Source:       detail.Record.Source,
		Destination:  destinationFromEventPayload(detail.Event.Payload),
		HTTPStatus:   latestResponseCode(detail.DeliveryAttempts),
		Error:        latestError(detail),
		AttemptCount: detail.Record.AttemptCount,
	}
	if request.Destination == "" {
		request.Destination = "Webhook destination"
	}
	if request.AttemptCount < 1 {
		request.AttemptCount = 1
	}
	businessEventType, schemaVersion := safeMetadataFromEventPayload(detail.Event.Payload)
	request.BusinessEventType = businessEventType
	request.SchemaVersion = schemaVersion
	return request
}

func latestResponseCode(attempts []operations.DeliveryAttempt) *int {
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i].ResponseCode != nil {
			return attempts[i].ResponseCode
		}
	}
	return nil
}

func latestError(detail operations.DLQDetail) string {
	for i := len(detail.DeliveryAttempts) - 1; i >= 0; i-- {
		if detail.DeliveryAttempts[i].Error != nil && strings.TrimSpace(*detail.DeliveryAttempts[i].Error) != "" {
			return strings.TrimSpace(*detail.DeliveryAttempts[i].Error)
		}
	}
	if detail.Record.LastError != nil && strings.TrimSpace(*detail.Record.LastError) != "" {
		return strings.TrimSpace(*detail.Record.LastError)
	}
	return "Delivery failed"
}

func safeMetadataFromEventPayload(payload json.RawMessage) (*string, *string) {
	var webhook struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(payload, &webhook); err != nil {
		return nil, nil
	}
	return stringValuePointer(webhook.Data["business_event_type"]), stringValuePointer(webhook.Data["schema_version"])
}

func destinationFromEventPayload(payload json.RawMessage) string {
	businessEventType, _ := safeMetadataFromEventPayload(payload)
	if businessEventType != nil && *businessEventType == "invoice.paid" {
		return "Receipt Service"
	}
	return ""
}

func stringValuePointer(value any) *string {
	raw, ok := value.(string)
	if !ok {
		return nil
	}
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return nil
	}
	return &clean
}
