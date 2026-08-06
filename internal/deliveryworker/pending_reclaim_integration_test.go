package deliveryworker

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestReclaimPendingMessagesRedisIntegration(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR is not set; skipping Redis pending reclaim integration test")
	}

	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr})
	stream := newPendingReclaimStreamName(t)
	group := "eventrail.reclaim.test.cg"
	consumerA := "consumer-a"
	consumerB := "consumer-b"

	t.Cleanup(func() {
		if err := client.Del(context.Background(), stream).Err(); err != nil {
			t.Logf("delete Redis test stream %s: %v", stream, err)
		}
		if err := client.Close(); err != nil {
			t.Logf("close Redis test client: %v", err)
		}
	})

	if err := client.XGroupCreateMkStream(ctx, stream, group, "0").Err(); err != nil {
		t.Fatalf("create Redis consumer group: %v", err)
	}

	messageID, err := client.XAdd(ctx, &redis.XAddArgs{
		Stream: stream,
		ID:     "*",
		Values: map[string]interface{}{
			"event_type": "invoice.paid",
			"retry":      "0",
		},
	}).Result()
	if err != nil {
		t.Fatalf("add Redis stream message: %v", err)
	}

	readResult, err := client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: consumerA,
		Streams:  []string{stream, ">"},
		Count:    1,
		Block:    time.Second,
	}).Result()
	if err != nil {
		t.Fatalf("read message with consumer A: %v", err)
	}
	if got := readResult[0].Messages[0].ID; got != messageID {
		t.Fatalf("expected consumer A to read %s, got %s", messageID, got)
	}

	time.Sleep(20 * time.Millisecond)

	reclaimed, err := reclaimPendingMessagesForConsumer(
		ctx,
		client,
		stream,
		group,
		consumerB,
		time.Millisecond,
		10,
	)
	if err != nil {
		t.Fatalf("reclaim pending messages: %v", err)
	}
	if len(reclaimed) != 1 {
		t.Fatalf("expected one reclaimed message, got %d", len(reclaimed))
	}
	if reclaimed[0].ID != messageID {
		t.Fatalf("expected reclaimed message %s, got %s", messageID, reclaimed[0].ID)
	}

	pending := pendingEntries(t, client, stream, group)
	if len(pending) != 1 {
		t.Fatalf("expected one pending message after reclaim, got %d", len(pending))
	}
	if pending[0].ID != messageID {
		t.Fatalf("expected pending message %s, got %s", messageID, pending[0].ID)
	}
	if pending[0].Consumer != consumerB {
		t.Fatalf("expected pending owner %s, got %s", consumerB, pending[0].Consumer)
	}

	if err := acknowledgeDeliveredMessage(ctx, reclaimed[0], func(ctx context.Context, messageID string) error {
		return client.XAck(ctx, stream, group, messageID).Err()
	}); err != nil {
		t.Fatalf("acknowledge reclaimed message: %v", err)
	}

	pending = pendingEntries(t, client, stream, group)
	if len(pending) != 0 {
		t.Fatalf("expected no pending messages after acknowledgement, got %d", len(pending))
	}
}

func TestReclaimPendingMessagesRedisIntegrationNoPending(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR is not set; skipping Redis pending reclaim integration test")
	}

	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr})
	stream := newPendingReclaimStreamName(t)
	group := "eventrail.reclaim.test.cg"

	t.Cleanup(func() {
		if err := client.Del(context.Background(), stream).Err(); err != nil {
			t.Logf("delete Redis test stream %s: %v", stream, err)
		}
		if err := client.Close(); err != nil {
			t.Logf("close Redis test client: %v", err)
		}
	})

	if err := client.XGroupCreateMkStream(ctx, stream, group, "0").Err(); err != nil {
		t.Fatalf("create Redis consumer group: %v", err)
	}

	reclaimed, err := reclaimPendingMessagesForConsumer(
		ctx,
		client,
		stream,
		group,
		"consumer-b",
		time.Millisecond,
		10,
	)
	if err != nil {
		t.Fatalf("reclaim pending messages with empty pending list: %v", err)
	}
	if len(reclaimed) != 0 {
		t.Fatalf("expected no reclaimed messages, got %d", len(reclaimed))
	}
}

func pendingEntries(t *testing.T, client *redis.Client, stream string, group string) []redis.XPendingExt {
	t.Helper()

	pending, err := client.XPendingExt(context.Background(), &redis.XPendingExtArgs{
		Stream: stream,
		Group:  group,
		Start:  "-",
		End:    "+",
		Count:  10,
	}).Result()
	if err != nil {
		t.Fatalf("read pending entries: %v", err)
	}
	return pending
}

func newPendingReclaimStreamName(t *testing.T) string {
	t.Helper()

	var randomBytes [8]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		t.Fatalf("generate Redis stream suffix: %v", err)
	}
	return fmt.Sprintf("eventrail:pending-reclaim-test:%d:%x", time.Now().UnixNano(), randomBytes[:])
}
