package redisstream

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/krishgondaliya/eventrail-ingestion/internal/outbox"
	"github.com/redis/go-redis/v9"
)

type fakeXAddClient struct {
	calls       int
	receivedCtx context.Context
	receivedArg *redis.XAddArgs
	streamID    string
	err         error
}

func (c *fakeXAddClient) XAdd(ctx context.Context, args *redis.XAddArgs) *redis.StringCmd {
	c.calls++
	c.receivedCtx = ctx
	c.receivedArg = args

	cmd := redis.NewStringCmd(ctx)
	if c.err != nil {
		cmd.SetErr(c.err)
		return cmd
	}
	cmd.SetVal(c.streamID)
	return cmd
}

func TestNewPublisher(t *testing.T) {
	tests := []struct {
		name       string
		client     xaddClient
		stream     string
		wantErr    bool
		wantStream string
	}{
		{
			name:       "valid client and stream",
			client:     &fakeXAddClient{},
			stream:     "eventrail.events",
			wantStream: "eventrail.events",
		},
		{
			name:    "nil client",
			client:  nil,
			stream:  "eventrail.events",
			wantErr: true,
		},
		{
			name:    "empty stream",
			client:  &fakeXAddClient{},
			stream:  "",
			wantErr: true,
		},
		{
			name:    "whitespace stream",
			client:  &fakeXAddClient{},
			stream:  " \t\n ",
			wantErr: true,
		},
		{
			name:       "stream is trimmed",
			client:     &fakeXAddClient{},
			stream:     "  eventrail.events  ",
			wantStream: "eventrail.events",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			publisher, err := NewPublisher(tt.client, tt.stream)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
			if publisher.stream != tt.wantStream {
				t.Fatalf("expected stream %q, got %q", tt.wantStream, publisher.stream)
			}
		})
	}
}

func TestPublisherPublishSuccess(t *testing.T) {
	client := &fakeXAddClient{streamID: "1-0"}
	publisher, err := NewPublisher(client, " eventrail.events ")
	if err != nil {
		t.Fatalf("NewPublisher returned error: %v", err)
	}

	ctx := context.WithValue(context.Background(), testContextKey{}, "request-context")
	event := sampleOutboxEvent()
	originalPayload := string(event.Payload)

	if err := publisher.Publish(ctx, event); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}

	if client.calls != 1 {
		t.Fatalf("expected one XADD call, got %d", client.calls)
	}
	if client.receivedCtx != ctx {
		t.Fatal("expected original context to reach Redis client")
	}
	if client.receivedArg.Stream != "eventrail.events" {
		t.Fatalf("expected stream eventrail.events, got %q", client.receivedArg.Stream)
	}
	if client.receivedArg.ID != "*" {
		t.Fatalf("expected Redis generated ID *, got %q", client.receivedArg.ID)
	}

	values, ok := client.receivedArg.Values.(map[string]interface{})
	if !ok {
		t.Fatalf("expected values map[string]interface{}, got %T", client.receivedArg.Values)
	}

	wantValues := map[string]string{
		"outbox_id":  "outbox-123",
		"event_id":   "event-456",
		"event_type": "invoice.paid",
		"source":     "payments-service",
		"payload":    `{"invoice_id":"INV-2048","amount":500}`,
		"retry":      "0",
		"created_at": "2026-08-06T13:14:15Z",
	}
	for key, want := range wantValues {
		got, ok := values[key]
		if !ok {
			t.Fatalf("missing Redis field %q", key)
		}
		if got != want {
			t.Fatalf("expected %s=%q, got %#v", key, want, got)
		}
	}

	if string(event.Payload) != originalPayload {
		t.Fatalf("expected original event payload not to be mutated, got %s", string(event.Payload))
	}
}

func TestPublisherPublishRedisFailure(t *testing.T) {
	client := &fakeXAddClient{
		streamID: "",
		err:      errors.New("redis refused payload secret"),
	}
	publisher, err := NewPublisher(client, "eventrail.events")
	if err != nil {
		t.Fatalf("NewPublisher returned error: %v", err)
	}

	err = publisher.Publish(context.Background(), sampleOutboxEvent())
	if err == nil {
		t.Fatal("expected Redis error")
	}
	if client.calls != 1 {
		t.Fatalf("expected exactly one XADD call, got %d", client.calls)
	}

	errorText := err.Error()
	for _, want := range []string{"eventrail.events", "outbox-123", "event-456"} {
		if !strings.Contains(errorText, want) {
			t.Fatalf("expected error to include %q, got %q", want, errorText)
		}
	}
	if strings.Contains(errorText, "INV-2048") || strings.Contains(errorText, `"amount":500`) {
		t.Fatalf("error leaked payload content: %q", errorText)
	}
}

func TestPublisherPublishEmptyRedisStreamID(t *testing.T) {
	client := &fakeXAddClient{streamID: ""}
	publisher, err := NewPublisher(client, "eventrail.events")
	if err != nil {
		t.Fatalf("NewPublisher returned error: %v", err)
	}

	err = publisher.Publish(context.Background(), sampleOutboxEvent())
	if err == nil {
		t.Fatal("expected empty stream ID error")
	}
	if client.calls != 1 {
		t.Fatalf("expected exactly one XADD call, got %d", client.calls)
	}
	if strings.Contains(err.Error(), "INV-2048") || strings.Contains(err.Error(), `"amount":500`) {
		t.Fatalf("error leaked payload content: %q", err.Error())
	}
}

func TestPublisherPublishCancelledContext(t *testing.T) {
	client := &fakeXAddClient{err: context.Canceled}
	publisher, err := NewPublisher(client, "eventrail.events")
	if err != nil {
		t.Fatalf("NewPublisher returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = publisher.Publish(ctx, sampleOutboxEvent())
	if err == nil {
		t.Fatal("expected cancelled context error")
	}
	if client.calls != 1 {
		t.Fatalf("expected exactly one XADD call, got %d", client.calls)
	}
	if client.receivedCtx != ctx {
		t.Fatal("expected cancelled context to reach Redis client")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected error to wrap context.Canceled, got %v", err)
	}
	if strings.Contains(err.Error(), "INV-2048") || strings.Contains(err.Error(), `"amount":500`) {
		t.Fatalf("error leaked payload content: %q", err.Error())
	}
}

func TestPublisherPublishInvalidReceiverState(t *testing.T) {
	event := sampleOutboxEvent()

	var nilPublisher *Publisher
	if err := nilPublisher.Publish(context.Background(), event); err == nil {
		t.Fatal("expected nil receiver error")
	}

	missingClient := &Publisher{stream: "eventrail.events"}
	if err := missingClient.Publish(context.Background(), event); err == nil {
		t.Fatal("expected missing client error")
	}

	missingStream := &Publisher{client: &fakeXAddClient{}, stream: ""}
	if err := missingStream.Publish(context.Background(), event); err == nil {
		t.Fatal("expected missing stream error")
	}
}

type testContextKey struct{}

func sampleOutboxEvent() outbox.OutboxEvent {
	return outbox.OutboxEvent{
		OutboxID:     "outbox-123",
		EventID:      "event-456",
		EventType:    "invoice.paid",
		Source:       "payments-service",
		Payload:      []byte(`{"invoice_id":"INV-2048","amount":500}`),
		CreatedAt:    time.Date(2026, 8, 6, 9, 14, 15, 0, time.FixedZone("EDT", -4*60*60)),
		AttemptCount: 3,
	}
}
