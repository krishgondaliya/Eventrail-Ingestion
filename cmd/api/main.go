package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krishgondaliya/eventrail-ingestion/internal/broker/redisstream"
	"github.com/krishgondaliya/eventrail-ingestion/internal/deliveryworker"
	"github.com/krishgondaliya/eventrail-ingestion/internal/httpapi"
	"github.com/krishgondaliya/eventrail-ingestion/internal/ingestion"
	"github.com/krishgondaliya/eventrail-ingestion/internal/outbox"
	"github.com/krishgondaliya/eventrail-ingestion/migrations"
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
	signalCtx, stopSignals := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stopSignals()

	ctx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()

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

	if err := migrations.Apply(ctx, pgPool); err != nil {
		log.Fatalf("failed to apply PostgreSQL migrations: %v", err)
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})
	defer redisClient.Close()

	deliveryWorker := deliveryworker.New(redisClient, deliveryworker.Config{
		Stream:        EventStream,
		ConsumerGroup: ConsumerGroup,
		Consumer:      consumer,
		DLQStream:     DLQStream,
		RetryZSet:     RetryZSet,
		MaxRetries:    maxRetries,
		BaseBackoff:   baseBackoff,
	})
	if err := deliveryWorker.EnsureConsumerGroup(ctx); err != nil {
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

	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		log.Println("outbox publisher started")
		if err := outboxRunner.Run(ctx); err != nil {
			log.Printf("outbox publisher stopped with error: %v", err)
		}
	}()

	// Worker reads stream, reclaims pending messages, processes delivery, retries, and DLQs.
	workers.Add(1)
	go func() {
		defer workers.Done()
		if err := deliveryWorker.Run(ctx); err != nil {
			log.Printf("delivery worker stopped with error: %v", err)
		}
	}()

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
		if err := httpapi.DecodeJSONRequest(w, r, &req); err != nil {
			if httpapi.IsRequestBodyTooLarge(err) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
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
		if err := httpapi.DecodeJSONRequest(w, r, &req); err != nil {
			if httpapi.IsRequestBodyTooLarge(err) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
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

	server := &http.Server{
		Addr:              ":8080",
		Handler:           http.DefaultServeMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Println("EventRail API starting on :8080")
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case <-signalCtx.Done():
		log.Println("shutdown signal received")
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown failed: %v", err)
		}
		cancelShutdown()
		cancelApp()
		workers.Wait()
	case err := <-serverErr:
		if err != nil {
			log.Printf("HTTP server stopped with error: %v", err)
		}
		cancelApp()
		workers.Wait()
	}
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
