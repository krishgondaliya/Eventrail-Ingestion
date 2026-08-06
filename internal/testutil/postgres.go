package testutil

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("create admin PostgreSQL pool: %v", err)
	}
	ensureUUIDExtension(t, ctx, adminPool)

	schemaName := newSafeIntegrationSchemaName(t)
	quotedSchemaName := quotePostgresIdentifier(schemaName)
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchemaName); err != nil {
		adminPool.Close()
		t.Fatalf("create test schema %s: %v", schemaName, err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
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
	config.MaxConns = 16
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO "+quotedSchemaName+", public")
		return err
	}

	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("create test PostgreSQL pool: %v", err)
	}

	applyMigrations(t, ctx, testPool)
	return testPool
}

func ensureUUIDExtension(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	const lockID int64 = 0x6576656e7472616c
	if _, err := pool.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		t.Fatalf("lock uuid extension setup: %v", err)
	}
	defer func() {
		if _, err := pool.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, lockID); err != nil {
			t.Logf("unlock uuid extension setup: %v", err)
		}
	}()

	if _, err := pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS "uuid-ossp" WITH SCHEMA public`); err != nil {
		t.Fatalf("create uuid-ossp extension: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER EXTENSION "uuid-ossp" SET SCHEMA public`); err != nil {
		t.Fatalf("move uuid-ossp extension to public schema: %v", err)
	}
}

func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	migrationsDir := findMigrationsDir(t)
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.sql"))
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatalf("no migration files found in %s", migrationsDir)
	}

	for _, file := range files {
		sqlBytes, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read migration %s: %v", filepath.Base(file), err)
		}
		if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("apply migration %s: %v", filepath.Base(file), err)
		}
	}
}

func findMigrationsDir(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}

	for {
		goMod := filepath.Join(dir, "go.mod")
		migrationsDir := filepath.Join(dir, "migrations")
		if fileExists(goMod) && dirExists(migrationsDir) {
			return migrationsDir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root containing go.mod and migrations")
		}
		dir = parent
	}
}

func newSafeIntegrationSchemaName(t *testing.T) string {
	t.Helper()

	var randomBytes [8]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		t.Fatalf("generate schema suffix: %v", err)
	}
	return fmt.Sprintf("eventrail_test_%d_%x", time.Now().UnixNano(), randomBytes[:])
}

func quotePostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
