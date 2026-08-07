package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

const secretTestDSN = "postgres://eventrail:super-secret-password@localhost:5432/eventrail"

func TestLoadRejectsMissingRequiredValues(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr string
	}{
		{
			name:    "missing POSTGRES_DSN",
			key:     "POSTGRES_DSN",
			wantErr: "POSTGRES_DSN",
		},
		{
			name:    "missing REDIS_ADDR",
			key:     "REDIS_ADDR",
			wantErr: "REDIS_ADDR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("POSTGRES_DSN", secretTestDSN)
			t.Setenv("REDIS_ADDR", "localhost:6379")
			t.Setenv(tt.key, " ")

			_, err := Load()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error to name %s, got %v", tt.wantErr, err)
			}
			assertNoSecretDSN(t, err)
		})
	}
}

func TestLoadUsesDefaultsForUnsetOptionalValues(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("POSTGRES_DSN", secretTestDSN)
	t.Setenv("REDIS_ADDR", "localhost:6379")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got.PostgresDSN != secretTestDSN {
		t.Fatalf("expected postgres DSN to be preserved")
	}
	if got.RedisAddr != "localhost:6379" {
		t.Fatalf("expected Redis addr localhost:6379, got %q", got.RedisAddr)
	}
	if got.AIServiceURL != "http://127.0.0.1:8090" {
		t.Fatalf("expected default AI service URL, got %q", got.AIServiceURL)
	}
	if got.AIServiceTimeout != 10*time.Second {
		t.Fatalf("expected default AI service timeout 10s, got %v", got.AIServiceTimeout)
	}
	if got.ConsumerName != "api-1" {
		t.Fatalf("expected default consumer api-1, got %q", got.ConsumerName)
	}
	if got.MaxRetries != 5 {
		t.Fatalf("expected default max retries 5, got %d", got.MaxRetries)
	}
	if got.BaseBackoff != 500*time.Millisecond {
		t.Fatalf("expected default base backoff 500ms, got %v", got.BaseBackoff)
	}
	if got.OutboxPollInterval != 250*time.Millisecond {
		t.Fatalf("expected default outbox poll interval 250ms, got %v", got.OutboxPollInterval)
	}
}

func TestLoadRejectsInvalidMaxRetries(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{
			name:  "not integer",
			value: "many",
		},
		{
			name:  "zero",
			value: "0",
		},
		{
			name:  "negative",
			value: "-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("POSTGRES_DSN", secretTestDSN)
			t.Setenv("REDIS_ADDR", "localhost:6379")
			t.Setenv("MAX_RETRIES", tt.value)

			_, err := Load()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "MAX_RETRIES") {
				t.Fatalf("expected MAX_RETRIES error, got %v", err)
			}
			assertNoSecretDSN(t, err)
		})
	}
}

func TestLoadRejectsInvalidDurationValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{
			name:  "invalid BASE_BACKOFF_MS",
			key:   "BASE_BACKOFF_MS",
			value: "soon",
		},
		{
			name:  "zero BASE_BACKOFF_MS",
			key:   "BASE_BACKOFF_MS",
			value: "0",
		},
		{
			name:  "negative BASE_BACKOFF_MS",
			key:   "BASE_BACKOFF_MS",
			value: "-10",
		},
		{
			name:  "invalid OUTBOX_POLL_INTERVAL_MS",
			key:   "OUTBOX_POLL_INTERVAL_MS",
			value: "later",
		},
		{
			name:  "zero OUTBOX_POLL_INTERVAL_MS",
			key:   "OUTBOX_POLL_INTERVAL_MS",
			value: "0",
		},
		{
			name:  "negative OUTBOX_POLL_INTERVAL_MS",
			key:   "OUTBOX_POLL_INTERVAL_MS",
			value: "-20",
		},
		{
			name:  "invalid AI_SERVICE_TIMEOUT_MS",
			key:   "AI_SERVICE_TIMEOUT_MS",
			value: "later",
		},
		{
			name:  "zero AI_SERVICE_TIMEOUT_MS",
			key:   "AI_SERVICE_TIMEOUT_MS",
			value: "0",
		},
		{
			name:  "negative AI_SERVICE_TIMEOUT_MS",
			key:   "AI_SERVICE_TIMEOUT_MS",
			value: "-100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("POSTGRES_DSN", secretTestDSN)
			t.Setenv("REDIS_ADDR", "localhost:6379")
			t.Setenv(tt.key, tt.value)

			_, err := Load()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("expected %s error, got %v", tt.key, err)
			}
			assertNoSecretDSN(t, err)
		})
	}
}

func TestLoadParsesValidOverrides(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("POSTGRES_DSN", secretTestDSN)
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("AI_SERVICE_URL", "http://ai-service:8090")
	t.Setenv("AI_SERVICE_TIMEOUT_MS", "1500")
	t.Setenv("CONSUMER_NAME", "worker-7")
	t.Setenv("MAX_RETRIES", "9")
	t.Setenv("BASE_BACKOFF_MS", "1250")
	t.Setenv("OUTBOX_POLL_INTERVAL_MS", "750")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got.PostgresDSN != secretTestDSN {
		t.Fatalf("expected postgres DSN to be preserved")
	}
	if got.RedisAddr != "redis:6379" {
		t.Fatalf("expected Redis addr redis:6379, got %q", got.RedisAddr)
	}
	if got.AIServiceURL != "http://ai-service:8090" {
		t.Fatalf("expected AI service URL override, got %q", got.AIServiceURL)
	}
	if got.AIServiceTimeout != 1500*time.Millisecond {
		t.Fatalf("expected AI service timeout override, got %v", got.AIServiceTimeout)
	}
	if got.ConsumerName != "worker-7" {
		t.Fatalf("expected consumer worker-7, got %q", got.ConsumerName)
	}
	if got.MaxRetries != 9 {
		t.Fatalf("expected max retries 9, got %d", got.MaxRetries)
	}
	if got.BaseBackoff != 1250*time.Millisecond {
		t.Fatalf("expected base backoff 1250ms, got %v", got.BaseBackoff)
	}
	if got.OutboxPollInterval != 750*time.Millisecond {
		t.Fatalf("expected outbox poll interval 750ms, got %v", got.OutboxPollInterval)
	}
}

func TestLoadUsesDefaultForBlankConsumerName(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("POSTGRES_DSN", secretTestDSN)
	t.Setenv("REDIS_ADDR", "localhost:6379")
	t.Setenv("CONSUMER_NAME", " ")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.ConsumerName != "api-1" {
		t.Fatalf("expected default consumer api-1, got %q", got.ConsumerName)
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"POSTGRES_DSN",
		"REDIS_ADDR",
		"AI_SERVICE_URL",
		"AI_SERVICE_TIMEOUT_MS",
		"CONSUMER_NAME",
		"MAX_RETRIES",
		"BASE_BACKOFF_MS",
		"OUTBOX_POLL_INTERVAL_MS",
	} {
		unsetEnv(t, key)
	}
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()

	oldValue, hadValue := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if hadValue {
			_ = os.Setenv(key, oldValue)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func assertNoSecretDSN(t *testing.T, err error) {
	t.Helper()

	if strings.Contains(err.Error(), secretTestDSN) || strings.Contains(err.Error(), "super-secret-password") {
		t.Fatalf("configuration error exposed PostgreSQL DSN: %v", err)
	}
}
