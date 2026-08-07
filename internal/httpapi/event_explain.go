package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/krishgondaliya/eventrail-ingestion/internal/delivery"
	"github.com/krishgondaliya/eventrail-ingestion/internal/operations"
)

const (
	maxExplainHistoryItems    = 20
	maxExplainAttemptItems    = 20
	maxExplainErrorLength     = 1000
	defaultExplainDestination = "Webhook destination"
)

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type EventExplainReader interface {
	EventStatus(context.Context, string) (operations.EventStatus, error)
	DLQDetail(context.Context, string) (operations.DLQDetail, error)
}

type EventExplainClient interface {
	Explain(context.Context, explainRequest) (explainResponse, error)
}

func NewEventExplainHandler(store EventExplainReader, client EventExplainClient) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if client == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "EXPLANATION_UNAVAILABLE"})
			return
		}

		eventID := strings.TrimSuffix(strings.TrimPrefix(strings.TrimRight(r.URL.Path, "/"), "/events/"), "/explain")
		eventID = strings.Trim(eventID, "/")
		if !validEventID(eventID) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "INVALID_EVENT_ID"})
			return
		}

		snapshot, err := explainRequestFromStore(r.Context(), store, eventID)
		if errors.Is(err, operations.ErrEventNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if errors.Is(err, errEventNotReadyForExplanation) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "EVENT_NOT_READY_FOR_EXPLANATION"})
			return
		}
		if err != nil {
			http.Error(w, "failed to build event explanation snapshot", http.StatusInternalServerError)
			return
		}

		explanation, err := client.Explain(r.Context(), snapshot)
		if errors.Is(err, ErrAIServiceInvalidResponse) {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "EXPLANATION_INVALID_RESPONSE"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "EXPLANATION_UNAVAILABLE"})
			return
		}
		writeJSON(w, http.StatusOK, explanation)
	})
}

var errEventNotReadyForExplanation = errors.New("event not ready for explanation")

func validEventID(eventID string) bool {
	return uuidPattern.MatchString(strings.TrimSpace(eventID))
}

func explainRequestFromStore(ctx context.Context, store EventExplainReader, eventID string) (explainRequest, error) {
	status, err := store.EventStatus(ctx, eventID)
	if err != nil {
		return explainRequest{}, err
	}

	var dlqDetail operations.DLQDetail
	dlqExists := false
	detail, err := store.DLQDetail(ctx, eventID)
	if err == nil {
		dlqDetail = detail
		dlqExists = true
	} else if !errors.Is(err, operations.ErrDLQNotFound) {
		return explainRequest{}, err
	}

	currentStatus, ok := mapExplainStatus(status.CurrentStatus)
	if !ok {
		return explainRequest{}, errEventNotReadyForExplanation
	}
	history := explainStatusHistoryFromStore(status.History)
	attempts := explainDeliveryAttemptsFromStore(status.DeliveryAttempts)
	if len(history) == 0 && len(attempts) == 0 {
		return explainRequest{}, errEventNotReadyForExplanation
	}

	businessEventType, _ := safeMetadataFromEventPayload(status.Event.Payload)
	destination := destinationFromEventPayload(status.Event.Payload)
	if destination == "" {
		destination = defaultExplainDestination
	}

	enteredDLQ := dlqExists || statusHistoryContains(status.History, operations.StatusDeadLettered)
	redriveCount := countStatus(status.History, operations.StatusRedriven)
	if redriveCount == 0 && dlqExists && dlqDetail.Record.RedrivenAt != nil {
		redriveCount = 1
	}

	return explainRequest{
		EventType:         status.Event.EventType,
		BusinessEventType: businessEventType,
		Source:            status.Event.Source,
		Destination:       destination,
		CurrentStatus:     currentStatus,
		StatusHistory:     boundExplainHistory(history),
		DeliveryAttempts:  boundExplainAttempts(attempts),
		RetryCount:        retryCountFromHistory(status.History),
		EnteredDLQ:        enteredDLQ,
		RedriveCount:      redriveCount,
		Delivered:         currentStatus == operations.StatusDelivered,
	}, nil
}

func explainStatusHistoryFromStore(history []operations.StatusHistoryEntry) []explainStatusHistory {
	items := make([]explainStatusHistory, 0, len(history))
	for _, entry := range history {
		status, ok := mapExplainStatus(entry.Status)
		if !ok {
			continue
		}
		occurredAt := formatOptionalTime(entry.CreatedAt)
		items = append(items, explainStatusHistory{
			Status:     status,
			OccurredAt: occurredAt,
		})
	}
	return items
}

func explainDeliveryAttemptsFromStore(attempts []operations.DeliveryAttempt) []explainDeliveryAttempt {
	items := make([]explainDeliveryAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		items = append(items, explainDeliveryAttempt{
			AttemptNumber: attempt.AttemptNumber,
			HTTPStatus:    attempt.ResponseCode,
			Outcome:       explainAttemptOutcome(attempt),
			Error:         safeExplainError(attempt),
			OccurredAt:    formatOptionalTime(attempt.CompletedAt),
		})
	}
	return items
}

func mapExplainStatus(status string) (string, bool) {
	switch status {
	case operations.StatusStored,
		operations.StatusPendingPublication,
		operations.StatusPublished,
		operations.StatusProcessing,
		operations.StatusRetrying,
		operations.StatusDeadLettered,
		operations.StatusRedriven,
		operations.StatusDelivered:
		return status, true
	case "RECEIVED":
		return "RECEIVED", true
	default:
		return "", false
	}
}

func explainAttemptOutcome(attempt operations.DeliveryAttempt) string {
	if attempt.ResponseCode != nil {
		code := *attempt.ResponseCode
		if code >= 200 && code <= 299 {
			return "success"
		}
		switch delivery.ClassifyHTTPStatus(code) {
		case delivery.FailureRetryable:
			return "temporary_failure"
		case delivery.FailurePermanent:
			return "permanent_failure"
		}
	}
	if attempt.Outcome == operations.DeliveryOutcomeSucceeded {
		return "success"
	}
	if attempt.Outcome != operations.DeliveryOutcomeFailed {
		return "unknown"
	}

	errText := ""
	if attempt.Error != nil {
		errText = strings.ToLower(*attempt.Error)
	}
	if strings.Contains(errText, "permanent") {
		return "permanent_failure"
	}
	if strings.Contains(errText, "timeout") ||
		strings.Contains(errText, "connection") ||
		strings.Contains(errText, "no such host") ||
		strings.Contains(errText, "deliver webhook request") {
		return "transport_failure"
	}
	if strings.Contains(errText, "retryable") {
		return "temporary_failure"
	}
	return "unknown"
}

func retryCountFromHistory(history []operations.StatusHistoryEntry) int {
	maxRetry := 0
	retryingStatuses := 0
	for _, entry := range history {
		if entry.Status != operations.StatusRetrying {
			continue
		}
		retryingStatuses++
		var details struct {
			Retry     *int `json:"retry"`
			NextRetry *int `json:"next_retry"`
		}
		if err := json.Unmarshal(entry.Details, &details); err == nil {
			if details.NextRetry != nil && *details.NextRetry > maxRetry {
				maxRetry = *details.NextRetry
			}
			if details.Retry != nil && *details.Retry > maxRetry {
				maxRetry = *details.Retry
			}
		}
	}
	if maxRetry > 0 {
		return maxRetry
	}
	return retryingStatuses
}

func safeExplainError(attempt operations.DeliveryAttempt) *string {
	if attempt.Error == nil {
		return nil
	}
	message := strings.TrimSpace(*attempt.Error)
	if message == "" {
		return nil
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "invoice_id"),
		strings.Contains(lower, "amount"),
		strings.Contains(lower, "currency"):
		message = "Required destination field was missing"
	case strings.Contains(lower, "http://") || strings.Contains(lower, "https://"):
		message = "Destination delivery failed before a safe HTTP response was recorded"
	case strings.Contains(lower, "authorization") || strings.Contains(lower, "token") || strings.Contains(lower, "secret"):
		message = "Destination rejected the request with an authentication or authorization error"
	case strings.Contains(lower, "stack trace") || strings.Contains(lower, "panic") || strings.Contains(lower, "database") || strings.Contains(lower, "dsn"):
		return nil
	}
	if len(message) > maxExplainErrorLength {
		message = strings.TrimSpace(message[:maxExplainErrorLength])
	}
	if message == "" {
		return nil
	}
	return &message
}

func formatOptionalTime(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func statusHistoryContains(history []operations.StatusHistoryEntry, status string) bool {
	for _, entry := range history {
		if entry.Status == status {
			return true
		}
	}
	return false
}

func countStatus(history []operations.StatusHistoryEntry, status string) int {
	count := 0
	for _, entry := range history {
		if entry.Status == status {
			count++
		}
	}
	return count
}

func boundExplainAttempts(attempts []explainDeliveryAttempt) []explainDeliveryAttempt {
	if len(attempts) <= maxExplainAttemptItems {
		return attempts
	}
	return append([]explainDeliveryAttempt(nil), attempts[len(attempts)-maxExplainAttemptItems:]...)
}

func boundExplainHistory(history []explainStatusHistory) []explainStatusHistory {
	if len(history) <= maxExplainHistoryItems {
		return history
	}

	// Python accepts at most 20 history items. Preserve the initial acceptance
	// state, terminal recovery markers, and the most recent transitions.
	keep := map[int]bool{0: true}
	for i, entry := range history {
		switch entry.Status {
		case operations.StatusDeadLettered, operations.StatusRedriven, operations.StatusDelivered:
			keep[i] = true
		}
	}
	for i := len(history) - 1; i >= 0 && len(keep) < maxExplainHistoryItems; i-- {
		keep[i] = true
	}

	bounded := make([]explainStatusHistory, 0, maxExplainHistoryItems)
	for i, entry := range history {
		if keep[i] {
			bounded = append(bounded, entry)
			if len(bounded) == maxExplainHistoryItems {
				break
			}
		}
	}
	return bounded
}
