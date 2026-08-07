package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultConsumerName       = "api-1"
	defaultMaxRetries         = 5
	defaultBaseBackoffMS      = 500
	defaultOutboxPollInterval = 250
	defaultAIServiceURL       = "http://127.0.0.1:8090"
	defaultAIServiceTimeoutMS = 10000
)

type Config struct {
	PostgresDSN        string
	RedisAddr          string
	AIServiceURL       string
	AIServiceTimeout   time.Duration
	ConsumerName       string
	MaxRetries         int
	BaseBackoff        time.Duration
	OutboxPollInterval time.Duration
}

func Load() (Config, error) {
	postgresDSN, err := requiredString("POSTGRES_DSN")
	if err != nil {
		return Config{}, err
	}
	redisAddr, err := requiredString("REDIS_ADDR")
	if err != nil {
		return Config{}, err
	}

	consumerName := strings.TrimSpace(os.Getenv("CONSUMER_NAME"))
	if consumerName == "" {
		consumerName = defaultConsumerName
	}
	aiServiceURL := strings.TrimSpace(os.Getenv("AI_SERVICE_URL"))
	if aiServiceURL == "" {
		aiServiceURL = defaultAIServiceURL
	}

	maxRetries, err := optionalPositiveInt("MAX_RETRIES", defaultMaxRetries)
	if err != nil {
		return Config{}, err
	}
	baseBackoffMS, err := optionalPositiveInt("BASE_BACKOFF_MS", defaultBaseBackoffMS)
	if err != nil {
		return Config{}, err
	}
	outboxPollIntervalMS, err := optionalPositiveInt("OUTBOX_POLL_INTERVAL_MS", defaultOutboxPollInterval)
	if err != nil {
		return Config{}, err
	}
	aiServiceTimeoutMS, err := optionalPositiveInt("AI_SERVICE_TIMEOUT_MS", defaultAIServiceTimeoutMS)
	if err != nil {
		return Config{}, err
	}

	return Config{
		PostgresDSN:        postgresDSN,
		RedisAddr:          redisAddr,
		AIServiceURL:       aiServiceURL,
		AIServiceTimeout:   time.Duration(aiServiceTimeoutMS) * time.Millisecond,
		ConsumerName:       consumerName,
		MaxRetries:         maxRetries,
		BaseBackoff:        time.Duration(baseBackoffMS) * time.Millisecond,
		OutboxPollInterval: time.Duration(outboxPollIntervalMS) * time.Millisecond,
	}, nil
}

func requiredString(key string) (string, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func optionalPositiveInt(key string, defaultValue int) (int, error) {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return defaultValue, nil
	}

	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer", key)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", key)
	}
	return value, nil
}
