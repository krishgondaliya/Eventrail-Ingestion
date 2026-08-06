package deliveryworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/krishgondaliya/eventrail-ingestion/internal/delivery"
	"github.com/redis/go-redis/v9"
)

const (
	PendingClaimIdle  = 30 * time.Second
	PendingClaimCount = 10
)

type Config struct {
	Stream        string
	ConsumerGroup string
	Consumer      string
	DLQStream     string
	RetryZSet     string
	MaxRetries    int
	BaseBackoff   time.Duration
}

type Worker struct {
	client *redis.Client
	config Config
}

func New(client *redis.Client, config Config) *Worker {
	return &Worker{
		client: client,
		config: config,
	}
}

func (w *Worker) EnsureConsumerGroup(ctx context.Context) error {
	err := w.client.XGroupCreateMkStream(ctx, w.config.Stream, w.config.ConsumerGroup, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

func (w *Worker) Run(ctx context.Context) {
	go w.retryPump(ctx)
	w.runStreamWorker(ctx)
}

func (w *Worker) runStreamWorker(ctx context.Context) {
	log.Printf("stream worker started (group=%s consumer=%s)", w.config.ConsumerGroup, w.config.Consumer)

	for {
		reclaimed, err := reclaimPendingMessagesForConsumer(
			ctx,
			w.client,
			w.config.Stream,
			w.config.ConsumerGroup,
			w.config.Consumer,
			PendingClaimIdle,
			PendingClaimCount,
		)
		if err != nil {
			log.Printf("XAUTOCLAIM error: %v", err)
		}
		for _, msg := range reclaimed {
			w.processStreamMessage(ctx, msg)
		}

		res, err := w.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    w.config.ConsumerGroup,
			Consumer: w.config.Consumer,
			Streams:  []string{w.config.Stream, ">"},
			Count:    10,
			Block:    5 * time.Second,
		}).Result()

		if err != nil {
			if err == redis.Nil {
				continue
			}
			log.Printf("XREADGROUP error: %v", err)
			time.Sleep(500 * time.Millisecond)
			continue
		}

		for _, stream := range res {
			for _, msg := range stream.Messages {
				w.processStreamMessage(ctx, msg)
			}
		}
	}
}

func reclaimPendingMessages(
	ctx context.Context,
	client *redis.Client,
	stream string,
	group string,
	consumer string,
) ([]redis.XMessage, error) {
	return reclaimPendingMessagesForConsumer(
		ctx,
		client,
		stream,
		group,
		consumer,
		PendingClaimIdle,
		PendingClaimCount,
	)
}

func reclaimPendingMessagesForConsumer(
	ctx context.Context,
	client *redis.Client,
	stream string,
	group string,
	consumer string,
	minIdle time.Duration,
	count int64,
) ([]redis.XMessage, error) {
	messages, _, err := client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   stream,
		Group:    group,
		Consumer: consumer,
		MinIdle:  minIdle,
		Start:    "0-0",
		Count:    count,
	}).Result()
	if err == redis.Nil {
		return nil, nil
	}
	return messages, err
}

func (w *Worker) processStreamMessage(ctx context.Context, msg redis.XMessage) {
	retry := parseRetry(msg.Values["retry"])

	if err := processMessage(msg); err != nil {
		if err := handleDeliveryFailure(
			ctx,
			msg,
			err,
			retry,
			w.config.MaxRetries,
			w.config.BaseBackoff,
			func(ctx context.Context, msg redis.XMessage, nextRetry int, delay time.Duration) error {
				return scheduleRetry(ctx, w.client, w.config.RetryZSet, msg, nextRetry, delay)
			},
			func(ctx context.Context, msg redis.XMessage, cause error) error {
				return moveToDLQ(ctx, w.client, w.config.DLQStream, msg, cause)
			},
			func(ctx context.Context, messageID string) error {
				return w.client.XAck(ctx, w.config.Stream, w.config.ConsumerGroup, messageID).Err()
			},
		); err != nil {
			log.Printf("delivery failure handling failed (msg=%s): %v", msg.ID, err)
		}
		return
	}

	log.Printf("processed event stream_id=%s event_id=%v type=%v source=%v retry=%d",
		msg.ID, msg.Values["event_id"], msg.Values["event_type"], msg.Values["source"], retry)

	if err := acknowledgeDeliveredMessage(ctx, msg, func(ctx context.Context, messageID string) error {
		return w.client.XAck(ctx, w.config.Stream, w.config.ConsumerGroup, messageID).Err()
	}); err != nil {
		log.Printf("XACK error: %v", err)
	}
}

// processMessage is where delivery work happens.
// For testing retries, if event_type == "force.fail" we fail intentionally.
func processMessage(msg redis.XMessage) error {
	et, _ := msg.Values["event_type"].(string)
	if et == "force.fail" {
		return delivery.NewRetryableFailure(errors.New("forced failure for testing"))
	}

	if et == "webhook" {
		return processWebhookMessage(msg, &http.Client{Timeout: 10 * time.Second})
	}

	return nil
}

func processWebhookMessage(msg redis.XMessage, client *http.Client) error {
	payloadStr, ok := msg.Values["payload"].(string)
	if !ok {
		return delivery.NewPermanentFailure(errors.New("webhook event missing payload"))
	}

	var payloadData struct {
		URL  string `json:"url"`
		Data any    `json:"data"`
	}
	if err := json.Unmarshal([]byte(payloadStr), &payloadData); err != nil {
		return delivery.NewPermanentFailure(fmt.Errorf("invalid webhook payload: %w", err))
	}

	if payloadData.URL == "" {
		return delivery.NewPermanentFailure(errors.New("webhook url is required"))
	}

	bodyBytes, err := json.Marshal(payloadData.Data)
	if err != nil {
		return delivery.NewPermanentFailure(fmt.Errorf("failed to marshal webhook data: %w", err))
	}

	req, err := http.NewRequest(http.MethodPost, payloadData.URL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return delivery.NewPermanentFailure(fmt.Errorf("create webhook request: %w", err))
	}
	if eventID, ok := msg.Values["event_id"].(string); ok && eventID != "" {
		req.Header.Set("Idempotency-Key", eventID)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return delivery.NewRetryableFailure(fmt.Errorf("deliver webhook request: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return delivery.NewHTTPFailure(resp.StatusCode, resp.Status)
	}

	log.Printf("webhook delivered successfully to %s", payloadData.URL)
	return nil
}

type scheduleRetryFunc func(context.Context, redis.XMessage, int, time.Duration) error
type writeDLQFunc func(context.Context, redis.XMessage, error) error
type acknowledgeMessageFunc func(context.Context, string) error

func handleDeliveryFailure(
	ctx context.Context,
	msg redis.XMessage,
	cause error,
	retry int,
	maxRetries int,
	baseBackoff time.Duration,
	schedule scheduleRetryFunc,
	writeDLQ writeDLQFunc,
	ack acknowledgeMessageFunc,
) error {
	switch delivery.DecideFailureAction(cause, retry, maxRetries) {
	case delivery.FailureActionRetry:
		nextRetry := retry + 1
		delay := backoffDelay(baseBackoff, nextRetry)
		if err := schedule(ctx, msg, nextRetry, delay); err != nil {
			return fmt.Errorf("schedule retry for message %s: %w", msg.ID, err)
		}
	case delivery.FailureActionDeadLetter:
		if err := writeDLQ(ctx, msg, cause); err != nil {
			return fmt.Errorf("write message %s to DLQ: %w", msg.ID, err)
		}
	default:
		return fmt.Errorf("unknown delivery failure action for message %s", msg.ID)
	}

	if err := ack(ctx, msg.ID); err != nil {
		return fmt.Errorf("acknowledge message %s after failure handling: %w", msg.ID, err)
	}
	return nil
}

func acknowledgeDeliveredMessage(ctx context.Context, msg redis.XMessage, ack acknowledgeMessageFunc) error {
	if err := ack(ctx, msg.ID); err != nil {
		return fmt.Errorf("acknowledge delivered message %s: %w", msg.ID, err)
	}
	return nil
}

func scheduleRetry(
	ctx context.Context,
	rdb *redis.Client,
	retryZSet string,
	msg redis.XMessage,
	nextRetry int,
	delay time.Duration,
) error {
	// We store the full Values as JSON in a ZSET member so we can re-publish later.
	values := msg.Values
	values["retry"] = strconv.Itoa(nextRetry)
	values["original_stream_id"] = msg.ID

	b, err := json.Marshal(values)
	if err != nil {
		return err
	}

	due := time.Now().Add(delay).UnixMilli()
	return rdb.ZAdd(ctx, retryZSet, redis.Z{
		Score:  float64(due),
		Member: string(b),
	}).Err()
}

func (w *Worker) retryPump(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now().UnixMilli()

		members, err := w.client.ZRangeByScore(ctx, w.config.RetryZSet, &redis.ZRangeBy{
			Min:    "-inf",
			Max:    strconv.FormatInt(now, 10),
			Offset: 0,
			Count:  50,
		}).Result()
		if err != nil || len(members) == 0 {
			continue
		}

		for _, m := range members {
			if err := republishRetryMember(
				ctx,
				m,
				func(ctx context.Context, values map[string]interface{}) error {
					_, err := w.client.XAdd(ctx, &redis.XAddArgs{
						Stream: w.config.Stream,
						Values: values,
					}).Result()
					return err
				},
				func(ctx context.Context, member string) error {
					removed, err := w.client.ZRem(ctx, w.config.RetryZSet, member).Result()
					if err != nil {
						return err
					}
					if removed == 0 {
						return fmt.Errorf("remove retry member affected 0 rows")
					}
					return nil
				},
			); err != nil {
				log.Printf("retry republish failed: %v", err)
			}
		}
	}
}

func republishRetryMember(
	ctx context.Context,
	member string,
	publish func(context.Context, map[string]interface{}) error,
	remove func(context.Context, string) error,
) error {
	var values map[string]interface{}
	if err := json.Unmarshal([]byte(member), &values); err != nil {
		return fmt.Errorf("decode retry member: %w", err)
	}

	if err := publish(ctx, values); err != nil {
		return fmt.Errorf("publish retry member: %w", err)
	}
	if err := remove(ctx, member); err != nil {
		return fmt.Errorf("remove published retry member: %w", err)
	}
	return nil
}

func moveToDLQ(ctx context.Context, rdb *redis.Client, dlqStream string, msg redis.XMessage, cause error) error {
	values := msg.Values
	values["dlq_at"] = time.Now().UTC().Format(time.RFC3339)
	values["error"] = cause.Error()
	values["original_stream_id"] = msg.ID

	_, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: dlqStream,
		Values: values,
	}).Result()
	if err != nil {
		return err
	}
	return nil
}

func parseRetry(v interface{}) int {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return i
}

func backoffDelay(base time.Duration, retry int) time.Duration {
	// Exponential backoff: base * 2^(retry-1), capped
	mult := 1 << (retry - 1)
	d := time.Duration(mult) * base
	if d > 10*time.Second {
		return 10 * time.Second
	}
	return d
}
