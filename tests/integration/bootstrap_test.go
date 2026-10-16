//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"onec/internal/mobius/httpapi"
	"onec/internal/mobius/postgres"
	"onec/migrations"
)

func TestBootstrap(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL must point to a dedicated empty database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var occupied bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'game')
		OR to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&occupied); err != nil {
		t.Fatal(err)
	}
	if occupied {
		t.Fatal("refusing to use a database with an existing game schema or migration journal")
	}
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := pool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS game CASCADE; DROP TABLE IF EXISTS public.schema_migrations"); err != nil {
			t.Error(err)
		}
	}()
	if err := migrations.Check(ctx, pool); err == nil {
		t.Fatal("unmigrated database reported ready")
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA game"); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, pool); err == nil {
		t.Fatal("failed migration was reported successful")
	}
	var journalExists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.schema_migrations') IS NOT NULL").Scan(&journalExists); err != nil {
		t.Fatal(err)
	}
	if journalExists {
		t.Fatal("failed migration left a partial journal")
	}
	if _, err := pool.Exec(ctx, "DROP SCHEMA game"); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE TABLE game.bootstrap_probe (value text); INSERT INTO game.bootstrap_probe VALUES ('preserved')"); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("repeated migration: %v", err)
	}
	var value string
	if err := pool.QueryRow(ctx, "SELECT value FROM game.bootstrap_probe").Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("migration did not preserve data: %q %v", value, err)
	}
	handler := httpapi.Mobius(func(ctx context.Context) error { return migrations.Check(ctx, pool) })
	checkReady := func(want int) {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != want {
			t.Fatalf("readiness: got %d, want %d", response.Code, want)
		}
	}
	checkReady(http.StatusOK)
	if _, err := pool.Exec(ctx, "DELETE FROM public.schema_migrations"); err != nil {
		t.Fatal(err)
	}
	checkReady(http.StatusServiceUnavailable)
}
