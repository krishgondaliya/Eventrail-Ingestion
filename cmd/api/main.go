package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/krishgondaliya/eventrail-ingestion/internal/broker/redisstream"
	"github.com/krishgondaliya/eventrail-ingestion/internal/config"
	"github.com/krishgondaliya/eventrail-ingestion/internal/deliveryworker"
	"github.com/krishgondaliya/eventrail-ingestion/internal/httpapi"
	"github.com/krishgondaliya/eventrail-ingestion/internal/ingestion"
	"github.com/krishgondaliya/eventrail-ingestion/internal/operations"
	"github.com/krishgondaliya/eventrail-ingestion/internal/outbox"
	"github.com/krishgondaliya/eventrail-ingestion/migrations"
	"github.com/redis/go-redis/v9"
)

const (
	EventStream   = "eventrail.events"
	ConsumerGroup = "eventrail.cg"
	DLQStream     = "eventrail.events.dlq"
	RetryZSet     = "eventrail.events.retry" // delayed retry scheduler (ZSET)

	DefaultOutboxBaseBackoff = 500 * time.Millisecond
	DefaultOutboxMaxBackoff  = 30 * time.Second
)

var (
	version = "dev"
	commit  = "unknown"
	builtAt = "unknown"
)

type Event struct {
	ID        string          `json:"id"`
	EventType string          `json:"event_type"`
	Source    string          `json:"source"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type ReplayRequest struct {
	From      string        `json:"from"`       // RFC3339
	To        string        `json:"to"`         // RFC3339
	Source    string        `json:"source"`     // optional
	EventType string        `json:"event_type"` // optional
	Limit     int           `json:"limit"`      // optional, default 1000
	Cursor    *ReplayCursor `json:"cursor"`     // optional
}

type ReplayResponse struct {
	Published  int           `json:"published"`
	HasMore    bool          `json:"has_more"`
	NextCursor *ReplayCursor `json:"next_cursor,omitempty"`
}

type ReplayCursor struct {
	CreatedAt string `json:"created_at"`
	EventID   string `json:"event_id"`
}

type replayRows interface {
	Close()
	Err() error
	Next() bool
	Scan(dest ...any) error
}

type replayQueryFunc func(ctx context.Context, query string, args ...any) (replayRows, error)

type replayPublishFunc func(ctx context.Context, values map[string]interface{}) (string, error)

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

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	pgPool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("failed to connect to postgres: %v", err)
	}
	defer pgPool.Close()

	if err := migrations.Apply(ctx, pgPool); err != nil {
		log.Fatalf("failed to apply PostgreSQL migrations: %v", err)
	}
	operationsStore := operations.NewStore(pgPool)
	aiTriageClient := httpapi.NewAITriageClient(cfg.AIServiceURL, &http.Client{
		Timeout: cfg.AIServiceTimeout,
	})

	redisClient := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddr,
	})
	defer redisClient.Close()

	deliveryWorker := deliveryworker.New(redisClient, deliveryworker.Config{
		Stream:        EventStream,
		ConsumerGroup: ConsumerGroup,
		Consumer:      cfg.ConsumerName,
		DLQStream:     DLQStream,
		RetryZSet:     RetryZSet,
		MaxRetries:    cfg.MaxRetries,
		BaseBackoff:   cfg.BaseBackoff,
		Recorder:      operationsStore,
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
		cfg.OutboxPollInterval,
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

	readinessHandler := httpapi.NewReadinessHandler(
		func(ctx context.Context) error {
			return pgPool.Ping(ctx)
		},
		func(ctx context.Context) error {
			return redisClient.Ping(ctx).Err()
		},
	)
	mux := http.NewServeMux()

	mux.Handle("/health/live", httpapi.NewLivenessHandler())
	mux.Handle("/health/ready", readinessHandler)
	mux.Handle("/health", readinessHandler)
	mux.Handle("/version", httpapi.NewVersionHandler("eventrail-api", version, commit, builtAt))
	mux.Handle("/dlq", httpapi.NewDLQHandler(operationsStore, func(ctx context.Context, values map[string]interface{}) (string, error) {
		return redisClient.XAdd(ctx, &redis.XAddArgs{
			Stream: EventStream,
			Values: values,
		}).Result()
	}, aiTriageClient))
	mux.Handle("/dlq/", httpapi.NewDLQHandler(operationsStore, func(ctx context.Context, values map[string]interface{}) (string, error) {
		return redisClient.XAdd(ctx, &redis.XAddArgs{
			Stream: EventStream,
			Values: values,
		}).Result()
	}, aiTriageClient))
	mux.Handle("/metrics/summary", httpapi.NewMetricsSummaryHandler(operationsStore))

	persist := func(
		ctx context.Context,
		input ingestion.EventInput,
		idempotencyKey string,
	) (ingestion.PersistResult, error) {
		return ingestion.PersistEventWithOutbox(ctx, pgPool, input, idempotencyKey)
	}
	mux.HandleFunc("/events", httpapi.NewCreateEventHandler(persist))
	eventStatusHandler := httpapi.NewEventStatusHandler(operationsStore)

	// --------------------
	// GET /events/{id}
	// --------------------
	mux.HandleFunc("/events/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/status") {
			eventStatusHandler.ServeHTTP(w, r)
			return
		}

		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
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
			r.Context(),
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

	mux.HandleFunc("/replay", newReplayHandler(
		func(ctx context.Context, query string, args ...any) (replayRows, error) {
			return pgPool.Query(ctx, query, args...)
		},
		func(ctx context.Context, values map[string]interface{}) (string, error) {
			return redisClient.XAdd(ctx, &redis.XAddArgs{
				Stream: EventStream,
				Values: values,
			}).Result()
		},
	))

	// --------------------
	// POST /consumer-groups/set-cursor (consumer replay)
	// Sets the group cursor so consumers can reprocess history.
	// --------------------
	mux.HandleFunc("/consumer-groups/set-cursor", func(w http.ResponseWriter, r *http.Request) {
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

		if err := redisClient.XGroupSetID(r.Context(), EventStream, group, startID).Err(); err != nil {
			http.Error(w, "failed to set group cursor", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{
		Addr:              ":8080",
		Handler:           httpapi.WithCORS(mux, dashboardOrigins()),
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

func dashboardOrigins() []string {
	return []string{
		"http://localhost:5173",
		"http://127.0.0.1:5173",
	}
}
