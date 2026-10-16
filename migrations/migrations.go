package migrations

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var files embed.FS

// Apply runs pending embedded SQL files in lexical order, in a single transaction.
// Existing migrations are immutable; new migrations get a new six-digit prefix.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	// Serialize concurrent migration commands before touching the migration journal.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(762018001)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_migrations (
		name text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
	)`); err != nil {
		return err
	}
	entries, err := files.ReadDir(".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var applied bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM public.schema_migrations WHERE name = $1)", entry.Name()).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := files.ReadFile(entry.Name())
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO public.schema_migrations (name) VALUES ($1)", entry.Name()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Check rejects missing or incompatible migrations without changing the database.
func Check(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, "SELECT name FROM public.schema_migrations ORDER BY name")
	if err != nil {
		return fmt.Errorf("migration journal unavailable; run mobius migrate")
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	entries, err := files.ReadDir(".")
	if err != nil {
		return err
	}
	if len(names) != len(entries) {
		return fmt.Errorf("schema version mismatch; run matching migrations")
	}
	for i, entry := range entries {
		if names[i] != entry.Name() {
			return fmt.Errorf("schema version mismatch; run matching migrations")
		}
	}
	var schemaExists bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'game')").Scan(&schemaExists); err != nil {
		return err
	}
	if !schemaExists {
		return fmt.Errorf("game schema is missing")
	}
	return nil
}
