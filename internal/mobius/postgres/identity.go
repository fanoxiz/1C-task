package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"onec/internal/protocol"
)

var (
	ErrUnknownIdentity = errors.New("unknown identity")
	ErrReconnect       = errors.New("reconnect required")
	ErrInvalidCursor   = errors.New("invalid cursor")
)

type IdentityStore struct{ pool *pgxpool.Pool }

func NewIdentityStore(pool *pgxpool.Pool) *IdentityStore { return &IdentityStore{pool: pool} }

func (s *IdentityStore) Register(ctx context.Context, name string, hash []byte) (protocol.State, error) {
	var state protocol.State
	err := s.pool.QueryRow(ctx, `INSERT INTO game.loki (display_name, token_hash, connected, last_seen_at)
		VALUES ($1, $2, true, clock_timestamp())
		RETURNING id::text, display_name, connected, created_at, last_seen_at, clock_timestamp()`, name, hash).
		Scan(&state.Loki.ID, &state.Loki.DisplayName, &state.Loki.Connected, &state.Loki.CreatedAt, &state.Loki.LastSeenAt, &state.ServerTime)
	return state, err
}

func (s *IdentityStore) Connect(ctx context.Context, hash []byte) (protocol.State, error) {
	return s.visit(ctx, hash, "connect")
}

func (s *IdentityStore) State(ctx context.Context, hash []byte) (protocol.State, error) {
	return s.visit(ctx, hash, "state")
}

func (s *IdentityStore) Disconnect(ctx context.Context, hash []byte) error {
	_, err := s.visit(ctx, hash, "disconnect")
	return err
}

func (s *IdentityStore) visit(ctx context.Context, hash []byte, action string) (protocol.State, error) {
	var state protocol.State
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return state, err
	}
	defer tx.Rollback(context.Background())
	err = tx.QueryRow(ctx, `SELECT id::text, display_name, connected, created_at, last_seen_at
		FROM game.loki WHERE token_hash = $1 FOR UPDATE`, hash).
		Scan(&state.Loki.ID, &state.Loki.DisplayName, &state.Loki.Connected, &state.Loki.CreatedAt, &state.Loki.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, ErrUnknownIdentity
	}
	if err != nil {
		return state, err
	}
	// Read the current database clock after the row lock, not transaction-start time.
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&state.ServerTime); err != nil {
		return state, err
	}
	if state.Loki.Connected && state.Loki.LastSeenAt != nil && !state.ServerTime.Before(state.Loki.LastSeenAt.Add(30*time.Second)) {
		state.Loki.Connected = false
	}
	if action == "connect" {
		state.Loki.Connected = true
	}
	if action == "disconnect" {
		state.Loki.Connected = false
	}
	if state.Loki.Connected {
		state.Loki.LastSeenAt = &state.ServerTime
	}
	if _, err := tx.Exec(ctx, "UPDATE game.loki SET connected = $2, last_seen_at = $3 WHERE id = $1", state.Loki.ID, state.Loki.Connected, state.Loki.LastSeenAt); err != nil {
		return state, err
	}
	if err := tx.Commit(ctx); err != nil {
		return state, err
	}
	// A late poll must commit the offline state before reporting a reconnect error.
	if action == "state" && !state.Loki.Connected {
		return state, ErrReconnect
	}
	return state, nil
}

func (s *IdentityStore) ResetPresence(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, "UPDATE game.loki SET connected = false WHERE connected")
	return err
}

func (s *IdentityStore) Expire(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE game.loki SET connected = false
		WHERE connected AND last_seen_at <= clock_timestamp() - interval '30 seconds'`)
	return err
}

func (s *IdentityStore) List(ctx context.Context, cursor string, onlineOnly bool) (protocol.LokiList, error) {
	result := protocol.LokiList{Items: []protocol.Loki{}}
	if cursor != "" {
		var id pgtype.UUID
		if err := id.Scan(cursor); err != nil || !id.Valid {
			return result, ErrInvalidCursor
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, display_name,
		connected AND last_seen_at > clock_timestamp() - interval '30 seconds', created_at, last_seen_at
		FROM game.loki WHERE (NULLIF($1, '')::uuid IS NULL OR id > NULLIF($1, '')::uuid)
		AND (NOT $2 OR (connected AND last_seen_at > clock_timestamp() - interval '30 seconds'))
		ORDER BY id LIMIT 51`, cursor, onlineOnly)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item protocol.Loki
		if err := rows.Scan(&item.ID, &item.DisplayName, &item.Connected, &item.CreatedAt, &item.LastSeenAt); err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(result.Items) > 50 {
		result.Items = result.Items[:50]
		result.NextCursor = result.Items[49].ID
	}
	return result, nil
}
