package redisstream

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestPublisherRedisIntegration(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR is not set; skipping Redis integration test")
	}

	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()

	stream := newTestStreamName(t)
	t.Cleanup(func() {
		if err := client.Del(context.Background(), stream).Err(); err != nil {
			t.Logf("delete Redis test stream %s: %v", stream, err)
		}
	})

	publisher, err := NewPublisher(client, stream)
	if err != nil {
		t.Fatalf("NewPublisher returned error: %v", err)
	}

	event := sampleOutboxEvent()
	if err := publisher.Publish(ctx, event); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}

	messages, err := client.XRange(ctx, stream, "-", "+").Result()
	if err != nil {
		t.Fatalf("XRANGE returned error: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected one Redis message, got %d", len(messages))
	}

	values := messages[0].Values
	wantValues := map[string]string{
		"outbox_id":  event.OutboxID,
		"event_id":   event.EventID,
		"event_type": event.EventType,
		"source":     event.Source,
		"payload":    string(event.Payload),
		"retry":      "0",
		"created_at": event.CreatedAt.UTC().Format(time.RFC3339),
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
}

func newTestStreamName(t *testing.T) string {
	t.Helper()

	var randomBytes [8]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		t.Fatalf("generate Redis stream suffix: %v", err)
	}
	return fmt.Sprintf("eventrail:test:%d:%x", time.Now().UnixNano(), randomBytes[:])
}
