package deliveryworker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/krishgondaliya/eventrail-ingestion/internal/delivery"
	"github.com/krishgondaliya/eventrail-ingestion/internal/operations"
	"github.com/redis/go-redis/v9"
)

func TestProcessMessageForceFailReturnsRetryableFailure(t *testing.T) {
	err := processMessage(redis.XMessage{
		Values: map[string]interface{}{
			"event_type": "force.fail",
		},
	})

	if err == nil {
		t.Fatal("expected force.fail error")
	}
	if !delivery.IsRetryable(err) {
		t.Fatalf("expected retryable failure, got %v", err)
	}
}

func TestProcessWebhookMalformedPayloadReturnsPermanentFailure(t *testing.T) {
	err := processMessage(webhookMessage(`{`))

	if err == nil {
		t.Fatal("expected malformed payload error")
	}
	if !delivery.IsPermanent(err) {
		t.Fatalf("expected permanent failure, got %v", err)
	}
}

func TestProcessWebhookMissingPayloadReturnsPermanentFailure(t *testing.T) {
	err := processMessage(redis.XMessage{
		Values: map[string]interface{}{
			"event_type": "webhook",
		},
	})

	if err == nil {
		t.Fatal("expected missing payload error")
	}
	if !delivery.IsPermanent(err) {
		t.Fatalf("expected permanent failure, got %v", err)
	}
}

func TestProcessWebhookMissingURLReturnsPermanentFailure(t *testing.T) {
	err := processMessage(webhookMessage(`{"data":{"invoice_id":"INV-2048"}}`))

	if err == nil {
		t.Fatal("expected missing URL error")
	}
	if !delivery.IsPermanent(err) {
		t.Fatalf("expected permanent failure, got %v", err)
	}
}

func TestProcessWebhookHTTPStatusFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantCheck  func(error) bool
	}{
		{
			name:       "400 permanent",
			statusCode: http.StatusBadRequest,
			wantCheck:  delivery.IsPermanent,
		},
		{
			name:       "429 retryable",
			statusCode: http.StatusTooManyRequests,
			wantCheck:  delivery.IsRetryable,
		},
		{
			name:       "500 retryable",
			statusCode: http.StatusInternalServerError,
			wantCheck:  delivery.IsRetryable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			err := processMessage(webhookMessage(webhookPayload(server.URL)))
			if err == nil {
				t.Fatal("expected HTTP failure")
			}
			if !tt.wantCheck(err) {
				t.Fatalf("unexpected failure classification: %v", err)
			}
		})
	}
}

func TestProcessWebhookHTTPSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := processMessage(webhookMessage(webhookPayload(server.URL))); err != nil {
		t.Fatalf("expected webhook success, got %v", err)
	}
}

func TestProcessWebhookResultCapturesSuccessStatusCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	result, err := processMessageWithResult(webhookMessage(webhookPayload(server.URL)))
	if err != nil {
		t.Fatalf("expected webhook success, got %v", err)
	}
	if result.ResponseCode == nil || *result.ResponseCode != http.StatusAccepted {
		t.Fatalf("expected response code %d, got %#v", http.StatusAccepted, result.ResponseCode)
	}
}

func TestProcessWebhookSetsIdempotencyKeyFromEventID(t *testing.T) {
	var gotIdempotencyKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdempotencyKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	msg := webhookMessage(webhookPayload(server.URL))
	msg.Values["event_id"] = "event-123"

	if err := processMessage(msg); err != nil {
		t.Fatalf("expected webhook success, got %v", err)
	}
	if gotIdempotencyKey != "event-123" {
		t.Fatalf("expected Idempotency-Key event-123, got %q", gotIdempotencyKey)
	}
}

func TestProcessingRecordFailurePreventsDestinationCall(t *testing.T) {
	destinationCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	worker := New(nil, Config{
		Recorder: failingProcessingRecorder{},
	})
	msg := webhookMessage(webhookPayload(server.URL))
	msg.ID = "1-0"
	msg.Values["event_id"] = "event-123"

	worker.processStreamMessage(context.Background(), msg)

	if destinationCalls != 0 {
		t.Fatalf("expected destination not to be called, got %d calls", destinationCalls)
	}
}

func TestProcessWebhookUnreachableDestinationReturnsRetryableFailure(t *testing.T) {
	err := processWebhookMessage(
		webhookMessage(webhookPayload("http://webhook.example.test")),
		&http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("connection refused")
			}),
		},
	)

	if err == nil {
		t.Fatal("expected unreachable destination error")
	}
	if !delivery.IsRetryable(err) {
		t.Fatalf("expected retryable failure, got %v", err)
	}
}

func webhookMessage(payload string) redis.XMessage {
	return redis.XMessage{
		Values: map[string]interface{}{
			"event_type": "webhook",
			"payload":    payload,
		},
	}
}

func webhookPayload(url string) string {
	return `{"url":"` + strings.ReplaceAll(url, `"`, `\"`) + `","data":{"invoice_id":"INV-2048","amount":500}}`
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type failingProcessingRecorder struct{}

func (failingProcessingRecorder) RecordProcessing(context.Context, operations.ProcessingRecord) error {
	return errors.New("postgres unavailable")
}

func (failingProcessingRecorder) RecordDelivered(context.Context, operations.DeliveryAttemptRecord) error {
	return nil
}

func (failingProcessingRecorder) RecordRetrying(context.Context, operations.RetryingRecord) error {
	return nil
}

func (failingProcessingRecorder) RecordDeadLettered(context.Context, operations.DeadLetterRecord) error {
	return nil
}

func TestProcessWebhookRequestContextStillConstructs(t *testing.T) {
	err := processWebhookMessage(
		webhookMessage(webhookPayload("://bad-url")),
		&http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, context.Canceled
			}),
		},
	)

	if err == nil {
		t.Fatal("expected invalid request construction error")
	}
	if !delivery.IsPermanent(err) {
		t.Fatalf("expected permanent failure, got %v", err)
	}
}
