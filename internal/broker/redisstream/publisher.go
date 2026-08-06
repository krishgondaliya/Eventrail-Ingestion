package redisstream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/krishgondaliya/eventrail-ingestion/internal/outbox"
	"github.com/redis/go-redis/v9"
)

type xaddClient interface {
	XAdd(ctx context.Context, args *redis.XAddArgs) *redis.StringCmd
}

type Publisher struct {
	client xaddClient
	stream string
}

func NewPublisher(client xaddClient, stream string) (*Publisher, error) {
	if client == nil {
		return nil, errors.New("redis stream publisher client is required")
	}

	normalizedStream := strings.TrimSpace(stream)
	if normalizedStream == "" {
		return nil, errors.New("redis stream publisher stream is required")
	}

	return &Publisher{
		client: client,
		stream: normalizedStream,
	}, nil
}

func (p *Publisher) Publish(ctx context.Context, event outbox.OutboxEvent) error {
	if p == nil {
		return errors.New("redis stream publisher is nil")
	}
	if p.client == nil {
		return errors.New("redis stream publisher client is required")
	}
	if strings.TrimSpace(p.stream) == "" {
		return errors.New("redis stream publisher stream is required")
	}

	args := &redis.XAddArgs{
		Stream: p.stream,
		ID:     "*",
		Values: map[string]interface{}{
			"outbox_id":  event.OutboxID,
			"event_id":   event.EventID,
			"event_type": event.EventType,
			"source":     event.Source,
			"payload":    string(event.Payload),
			"retry":      "0",
			"created_at": event.CreatedAt.UTC().Format(time.RFC3339),
		},
	}

	streamID, err := p.client.XAdd(ctx, args).Result()
	if err != nil {
		return fmt.Errorf("publish outbox %s event %s to Redis stream %s: %w", event.OutboxID, event.EventID, p.stream, err)
	}
	if streamID == "" {
		return fmt.Errorf("publish outbox %s event %s to Redis stream %s returned empty stream id", event.OutboxID, event.EventID, p.stream)
	}

	return nil
}
