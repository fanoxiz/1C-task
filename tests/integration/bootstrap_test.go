//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"onec/internal/mobius/httpapi"
	"onec/migrations"
)

func TestBootstrap(t *testing.T) {
	ctx, pool := testDatabase(t)
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
	handler := httpapi.Mobius(func(ctx context.Context) error { return migrations.Check(ctx, pool) }, nil)
	checkReady := func(want int) {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/readyz", nil))
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
