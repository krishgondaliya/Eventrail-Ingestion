package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krishgondaliya/eventrail-ingestion/internal/broker/redisstream"
	"github.com/krishgondaliya/eventrail-ingestion/internal/delivery"
	"github.com/krishgondaliya/eventrail-ingestion/internal/httpapi"
	"github.com/krishgondaliya/eventrail-ingestion/internal/ingestion"
	"github.com/krishgondaliya/eventrail-ingestion/internal/outbox"
	"github.com/redis/go-redis/v9"
)

const (
	EventStream   = "eventrail.events"
	ConsumerGroup = "eventrail.cg"
	DLQStream     = "eventrail.events.dlq"
	RetryZSet     = "eventrail.events.retry" // delayed retry scheduler (ZSET)

	DefaultWorker      = "api-1"
	DefaultMaxRetries  = 5
	DefaultBaseBackoff = 500 * time.Millisecond

	DefaultOutboxPollInterval = 250 * time.Millisecond
	DefaultOutboxBaseBackoff  = 500 * time.Millisecond
	DefaultOutboxMaxBackoff   = 30 * time.Second

	PendingClaimIdle  = 30 * time.Second
	PendingClaimCount = 10
)

type Event struct {
	ID        string          `json:"id"`
	EventType string          `json:"event_type"`
	Source    string          `json:"source"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type ReplayRequest struct {
	From      string `json:"from"`       // RFC3339
	To        string `json:"to"`         // RFC3339
	Source    string `json:"source"`     // optional
	EventType string `json:"event_type"` // optional
	Limit     int    `json:"limit"`      // optional, default 1000
}

type ReplayResponse struct {
	Published int `json:"published"`
}

type SetGroupCursorRequest struct {
	Group   string `json:"group"`    // optional, default ConsumerGroup
	StartID string `json:"start_id"` // e.g. "0" or "0-0" or "$"
}

func main() {
	ctx := context.Background()

	pgDSN := os.Getenv("POSTGRES_DSN")
	redisAddr := os.Getenv("REDIS_ADDR")

	consumer := strings.TrimSpace(os.Getenv("CONSUMER_NAME"))
	if consumer == "" {
		consumer = DefaultWorker
	}

	maxRetries := envInt("MAX_RETRIES", DefaultMaxRetries)
	baseBackoff := envDuration("BASE_BACKOFF_MS", DefaultBaseBackoff)
	outboxPollInterval := envDuration("OUTBOX_POLL_INTERVAL_MS", DefaultOutboxPollInterval)

	pgPool, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Fatalf("failed to connect to postgres: %v", err)
	}
	defer pgPool.Close()

	redisClient := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})
	defer redisClient.Close()

	if err := ensureConsumerGroup(ctx, redisClient); err != nil {
		log.Fatalf("failed to ensure consumer group: %v", err)
	}

	streamPublisher, err := redisstream.NewPublisher(redisClient, EventStream)
	if err != nil {
		log.Fatalf("create Redis stream publisher: %v", err)
	}

	publishNext := func(ctx context.Context) (outbox.PublishNextOutboxResult, error) {
		return outbox.PublishNextOutboxEvent(
			ctx,
			pgPool,
			streamPublisher.Publish,
			DefaultOutboxBaseBackoff,
			DefaultOutboxMaxBackoff,
		)
	}

	outboxRunner, err := outbox.NewRunner(
		publishNext,
		outboxPollInterval,
		func(err error) {
			log.Printf("outbox publisher error: %v", err)
		},
	)
	if err != nil {
		log.Fatalf("create outbox runner: %v", err)
	}

	go func() {
		log.Println("outbox publisher started")
		if err := outboxRunner.Run(ctx); err != nil {
			log.Printf("outbox publisher stopped with error: %v", err)
		}
	}()

	// Worker reads stream, processes, acks, schedules retries, moves to DLQ
	go startStreamWorker(redisClient, consumer, maxRetries, baseBackoff)

	// Retry pump pulls due items from ZSET and republishes them to the stream
	go retryPump(redisClient)

	// --------------------
	// Health Check
	// --------------------
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		hctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		pgStatus := "ok"
		if err := pgPool.Ping(hctx); err != nil {
			pgStatus = "error"
		}

		redisStatus := "ok"
		if err := redisClient.Ping(hctx).Err(); err != nil {
			redisStatus = "error"
		}

		status := "ok"
		if pgStatus != "ok" || redisStatus != "ok" {
			status = "degraded"
			w.WriteHeader(http.StatusServiceUnavailable)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(
			`{"status":"` + status + `","postgres":"` + pgStatus + `","redis":"` + redisStatus + `"}`,
		))
	})

	persist := func(
		ctx context.Context,
		input ingestion.EventInput,
		idempotencyKey string,
	) (ingestion.PersistResult, error) {
		return ingestion.PersistEventWithOutbox(ctx, pgPool, input, idempotencyKey)
	}
	http.HandleFunc("/events", httpapi.NewCreateEventHandler(persist))

	// --------------------
	// GET /events/{id}
	// --------------------
	http.HandleFunc("/events/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		id := strings.TrimPrefix(r.URL.Path, "/events/")
		if id == "" {
			http.Error(w, "event id required", http.StatusBadRequest)
			return
		}

		var evt Event
		err := pgPool.QueryRow(
			context.Background(),
			`SELECT id, event_type, source, payload, created_at
			 FROM events WHERE id = $1`,
			id,
		).Scan(
			&evt.ID,
			&evt.EventType,
			&evt.Source,
			&evt.Payload,
			&evt.CreatedAt,
		)

		if err == pgx.ErrNoRows {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "failed to fetch event", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(evt)
	})

	// --------------------
	// POST /replay (backfill by time range from Postgres into Redis Stream)
	// --------------------
	http.HandleFunc("/replay", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req ReplayRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}

		if req.From == "" || req.To == "" {
			http.Error(w, "from and to are required (RFC3339)", http.StatusBadRequest)
			return
		}

		fromT, err := time.Parse(time.RFC3339, req.From)
		if err != nil {
			http.Error(w, "from must be RFC3339", http.StatusBadRequest)
			return
		}
		toT, err := time.Parse(time.RFC3339, req.To)
		if err != nil {
			http.Error(w, "to must be RFC3339", http.StatusBadRequest)
			return
		}

		limit := req.Limit
		if limit <= 0 || limit > 5000 {
			limit = 1000
		}

		query := `
			SELECT id, event_type, source, payload, created_at
			FROM events
			WHERE created_at >= $1 AND created_at <= $2`
		args := []interface{}{fromT, toT}

		if req.Source != "" {
			query += " AND source = $3"
			args = append(args, req.Source)
		}

		if req.EventType != "" {
			if req.Source != "" {
				query += " AND event_type = $4"
			} else {
				query += " AND event_type = $3"
			}
			args = append(args, req.EventType)
		}

		query += " ORDER BY created_at ASC LIMIT " + strconv.Itoa(limit)

		rows, err := pgPool.Query(context.Background(), query, args...)
		if err != nil {
			http.Error(w, "failed to query events", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		published := 0
		for rows.Next() {
			var id, eventType, source string
			var payload []byte
			var createdAt time.Time
			if err := rows.Scan(&id, &eventType, &source, &payload, &createdAt); err != nil {
				http.Error(w, "failed to read events", http.StatusInternalServerError)
				return
			}

			_, err := redisClient.XAdd(context.Background(), &redis.XAddArgs{
				Stream: EventStream,
				Values: map[string]interface{}{
					"event_id":   id,
					"event_type": eventType,
					"source":     source,
					"payload":    string(payload),
					"retry":      "0",
					"created_at": createdAt.UTC().Format(time.RFC3339),
					"replay":     "1",
				},
			}).Result()
			if err != nil {
				log.Printf("replay publish failed for %s: %v", id, err)
				continue
			}
			published++
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ReplayResponse{Published: published})
	})

	// --------------------
	// POST /consumer-groups/set-cursor (consumer replay)
	// Sets the group cursor so consumers can reprocess history.
	// --------------------
	http.HandleFunc("/consumer-groups/set-cursor", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req SetGroupCursorRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}

		group := strings.TrimSpace(req.Group)
		if group == "" {
			group = ConsumerGroup
		}

		startID := strings.TrimSpace(req.StartID)
		if startID == "" {
			http.Error(w, "start_id is required", http.StatusBadRequest)
			return
		}

		if err := redisClient.XGroupSetID(context.Background(), EventStream, group, startID).Err(); err != nil {
			http.Error(w, "failed to set group cursor", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})

	log.Println("EventRail API starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func ensureConsumerGroup(ctx context.Context, rdb *redis.Client) error {
	err := rdb.XGroupCreateMkStream(ctx, EventStream, ConsumerGroup, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

func startStreamWorker(rdb *redis.Client, consumer string, maxRetries int, baseBackoff time.Duration) {
	ctx := context.Background()
	log.Printf("stream worker started (group=%s consumer=%s)", ConsumerGroup, consumer)

	for {
		reclaimed, err := reclaimPendingMessagesForConsumer(
			ctx,
			rdb,
			EventStream,
			ConsumerGroup,
			consumer,
			PendingClaimIdle,
			PendingClaimCount,
		)
		if err != nil {
			log.Printf("XAUTOCLAIM error: %v", err)
		}
		for _, msg := range reclaimed {
			processStreamMessage(ctx, rdb, msg, maxRetries, baseBackoff)
		}

		res, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    ConsumerGroup,
			Consumer: consumer,
			Streams:  []string{EventStream, ">"},
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
				processStreamMessage(ctx, rdb, msg, maxRetries, baseBackoff)
			}
		}
	}
}

func reclaimPendingMessages(
	ctx context.Context,
	client *redis.Client,
	consumer string,
) ([]redis.XMessage, error) {
	return reclaimPendingMessagesForConsumer(
		ctx,
		client,
		EventStream,
		ConsumerGroup,
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

func processStreamMessage(ctx context.Context, rdb *redis.Client, msg redis.XMessage, maxRetries int, baseBackoff time.Duration) {
	retry := parseRetry(msg.Values["retry"])

	if err := processMessage(msg); err != nil {
		if err := handleDeliveryFailure(
			ctx,
			msg,
			err,
			retry,
			maxRetries,
			baseBackoff,
			func(ctx context.Context, msg redis.XMessage, nextRetry int, delay time.Duration) error {
				return scheduleRetry(ctx, rdb, msg, nextRetry, delay)
			},
			func(ctx context.Context, msg redis.XMessage, cause error) error {
				return moveToDLQ(ctx, rdb, msg, cause)
			},
			func(ctx context.Context, messageID string) error {
				return rdb.XAck(ctx, EventStream, ConsumerGroup, messageID).Err()
			},
		); err != nil {
			log.Printf("delivery failure handling failed (msg=%s): %v", msg.ID, err)
		}
		return
	}

	log.Printf("processed event stream_id=%s event_id=%v type=%v source=%v retry=%d",
		msg.ID, msg.Values["event_id"], msg.Values["event_type"], msg.Values["source"], retry)

	if err := acknowledgeDeliveredMessage(ctx, msg, func(ctx context.Context, messageID string) error {
		return rdb.XAck(ctx, EventStream, ConsumerGroup, messageID).Err()
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

func scheduleRetry(ctx context.Context, rdb *redis.Client, msg redis.XMessage, nextRetry int, delay time.Duration) error {
	// We store the full Values as JSON in a ZSET member so we can re-publish later.
	values := msg.Values
	values["retry"] = strconv.Itoa(nextRetry)
	values["original_stream_id"] = msg.ID

	b, err := json.Marshal(values)
	if err != nil {
		return err
	}

	due := time.Now().Add(delay).UnixMilli()
	return rdb.ZAdd(ctx, RetryZSet, redis.Z{
		Score:  float64(due),
		Member: string(b),
	}).Err()
}

func retryPump(rdb *redis.Client) {
	ctx := context.Background()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now().UnixMilli()

		members, err := rdb.ZRangeByScore(ctx, RetryZSet, &redis.ZRangeBy{
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
					_, err := rdb.XAdd(ctx, &redis.XAddArgs{
						Stream: EventStream,
						Values: values,
					}).Result()
					return err
				},
				func(ctx context.Context, member string) error {
					removed, err := rdb.ZRem(ctx, RetryZSet, member).Result()
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

func moveToDLQ(ctx context.Context, rdb *redis.Client, msg redis.XMessage, cause error) error {
	values := msg.Values
	values["dlq_at"] = time.Now().UTC().Format(time.RFC3339)
	values["error"] = cause.Error()
	values["original_stream_id"] = msg.ID

	_, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: DLQStream,
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

func envInt(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// BASE_BACKOFF_MS is an int in milliseconds
func envDuration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	ms, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return time.Duration(ms) * time.Millisecond
}
