//go:build integration

package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"onec/internal/mobius/postgres"
)

func testDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL must point to a dedicated empty database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var occupied bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'game')
		OR to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&occupied); err != nil {
		t.Fatal(err)
	}
	if occupied {
		t.Fatal("refusing to use a database with an existing game schema or migration journal")
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := pool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS game CASCADE; DROP TABLE IF EXISTS public.schema_migrations"); err != nil {
			t.Error(err)
		}
	})
	return ctx, pool
}
