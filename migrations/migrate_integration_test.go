package migrations

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestApplyIntegrationIsIdempotent(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("create admin PostgreSQL pool: %v", err)
	}

	schemaName := newTestSchemaName(t)
	quotedSchemaName := quotePostgresIdentifier(schemaName)
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchemaName); err != nil {
		adminPool.Close()
		t.Fatalf("create test schema %s: %v", schemaName, err)
	}

	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		if _, err := adminPool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchemaName+" CASCADE"); err != nil {
			t.Logf("drop test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_POSTGRES_DSN: %v", err)
	}
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO "+quotedSchemaName+", public")
		return err
	}

	pool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("create test PostgreSQL pool: %v", err)
	}

	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	assertTableExists(t, ctx, pool, "events")
	assertTableExists(t, ctx, pool, "outbox")

	names, err := migrationNames()
	if err != nil {
		t.Fatalf("list embedded migrations: %v", err)
	}
	assertRecordedOnce(t, ctx, pool, names)

	if err := Apply(ctx, pool); err != nil {
		t.Fatalf("second Apply returned error: %v", err)
	}
	assertRecordedOnce(t, ctx, pool, names)
}

func assertTableExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) {
	t.Helper()

	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
		t.Fatalf("check table %s exists: %v", table, err)
	}
	if !exists {
		t.Fatalf("expected table %s to exist", table)
	}
}

func assertRecordedOnce(t *testing.T, ctx context.Context, pool *pgxpool.Pool, names []string) {
	t.Helper()

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if count != len(names) {
		t.Fatalf("expected %d migration records, got %d", len(names), count)
	}

	for _, name := range names {
		var nameCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE filename = $1`, name).Scan(&nameCount); err != nil {
			t.Fatalf("count migration %s: %v", name, err)
		}
		if nameCount != 1 {
			t.Fatalf("expected migration %s to be recorded once, got %d", name, nameCount)
		}
	}
}

func newTestSchemaName(t *testing.T) string {
	t.Helper()

	var randomBytes [8]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		t.Fatalf("generate schema suffix: %v", err)
	}
	return fmt.Sprintf("eventrail_migrations_test_%d_%x", time.Now().UnixNano(), randomBytes[:])
}

func quotePostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
