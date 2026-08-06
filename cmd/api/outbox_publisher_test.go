package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOutboxBackoff(t *testing.T) {
	tests := []struct {
		name    string
		base    time.Duration
		max     time.Duration
		attempt int
		want    time.Duration
	}{
		{
			name:    "attempt one",
			base:    100 * time.Millisecond,
			max:     10 * time.Second,
			attempt: 1,
			want:    100 * time.Millisecond,
		},
		{
			name:    "attempt two",
			base:    100 * time.Millisecond,
			max:     10 * time.Second,
			attempt: 2,
			want:    200 * time.Millisecond,
		},
		{
			name:    "several exponential steps",
			base:    100 * time.Millisecond,
			max:     10 * time.Second,
			attempt: 5,
			want:    1600 * time.Millisecond,
		},
		{
			name:    "maximum cap",
			base:    1 * time.Second,
			max:     5 * time.Second,
			attempt: 10,
			want:    5 * time.Second,
		},
		{
			name:    "very large attempt does not overflow",
			base:    1 * time.Second,
			max:     1 * time.Hour,
			attempt: 1_000_000,
			want:    1 * time.Hour,
		},
		{
			name:    "zero attempt uses base",
			base:    100 * time.Millisecond,
			max:     10 * time.Second,
			attempt: 0,
			want:    100 * time.Millisecond,
		},
		{
			name:    "negative attempt uses base",
			base:    100 * time.Millisecond,
			max:     10 * time.Second,
			attempt: -3,
			want:    100 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := outboxBackoff(tt.base, tt.max, tt.attempt)
			if got != tt.want {
				t.Fatalf("expected %s, got %s", tt.want, got)
			}
		})
	}
}

func TestSanitizeOutboxError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "normal error",
			err:  errors.New("remote service unavailable"),
			want: "remote service unavailable",
		},
		{
			name: "trims whitespace",
			err:  errors.New(" \t remote service unavailable \n "),
			want: "remote service unavailable",
		},
		{
			name: "empty error",
			err:  errors.New(""),
			want: "publication failed",
		},
		{
			name: "whitespace only error",
			err:  errors.New("  \n\t  "),
			want: "publication failed",
		},
		{
			name: "nil error",
			err:  nil,
			want: "publication failed",
		},
		{
			name: "unusual unicode",
			err:  errors.New(" snowman ☃ check ✓ "),
			want: "snowman ☃ check ✓",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeOutboxError(tt.err)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestSanitizeOutboxErrorTruncatesLongMessage(t *testing.T) {
	longMessage := strings.Repeat("a", maxStoredOutboxErrorLength+50)
	got := sanitizeOutboxError(errors.New(longMessage))

	if len([]rune(got)) != maxStoredOutboxErrorLength {
		t.Fatalf("expected %d runes, got %d", maxStoredOutboxErrorLength, len([]rune(got)))
	}
	if got != strings.Repeat("a", maxStoredOutboxErrorLength) {
		t.Fatal("unexpected truncated error message")
	}
}

func TestValidatePublishNextOutboxConfig(t *testing.T) {
	pool := new(pgxpool.Pool)
	validPublish := func() publishOutboxEventFunc {
		return func(ctx context.Context, event OutboxEvent) error {
			return nil
		}
	}

	tests := []struct {
		name        string
		pool        *pgxpool.Pool
		publish     publishOutboxEventFunc
		baseBackoff time.Duration
		maxBackoff  time.Duration
		wantErr     bool
	}{
		{
			name:        "nil publisher",
			pool:        pool,
			publish:     nil,
			baseBackoff: 100 * time.Millisecond,
			maxBackoff:  1 * time.Second,
			wantErr:     true,
		},
		{
			name:        "nonpositive base",
			pool:        pool,
			publish:     validPublish(),
			baseBackoff: 0,
			maxBackoff:  1 * time.Second,
			wantErr:     true,
		},
		{
			name:        "maximum smaller than base",
			pool:        pool,
			publish:     validPublish(),
			baseBackoff: 2 * time.Second,
			maxBackoff:  1 * time.Second,
			wantErr:     true,
		},
		{
			name:        "valid configuration",
			pool:        pool,
			publish:     validPublish(),
			baseBackoff: 100 * time.Millisecond,
			maxBackoff:  1 * time.Second,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePublishNextOutboxConfig(tt.pool, tt.publish, tt.baseBackoff, tt.maxBackoff)

			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
		})
	}
}

func TestValidatePublishNextOutboxConfigRejectsNilPool(t *testing.T) {
	err := validatePublishNextOutboxConfig(
		nil,
		func(ctx context.Context, event OutboxEvent) error { return nil },
		100*time.Millisecond,
		1*time.Second,
	)
	if err == nil {
		t.Fatal("expected nil pool error")
	}
}
