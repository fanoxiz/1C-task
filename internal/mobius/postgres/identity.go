package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"onec/internal/game"
	"onec/internal/protocol"
)

var (
	ErrUnknownIdentity = errors.New("unknown identity")
	ErrReconnect       = errors.New("reconnect required")
	ErrInvalidCursor   = errors.New("invalid cursor")
)

type IdentityStore struct {
	pool       *pgxpool.Pool
	serverLock *serverLock
}

const newcomerMessage = "Вы ещё не сыграли ни одной партии. Самое время начать!"

func NewIdentityStore(pool *pgxpool.Pool) *IdentityStore { return &IdentityStore{pool: pool} }

func (s *IdentityStore) Register(ctx context.Context, name string, hash []byte) (protocol.State, error) {
	var state protocol.State
	state.Statistics.Message = newcomerMessage
	err := s.pool.QueryRow(ctx, `INSERT INTO game.loki (display_name, token_hash, connected, last_seen_at)
		VALUES ($1, $2, true, clock_timestamp())
		RETURNING id::text, display_name, connected, created_at, last_seen_at, clock_timestamp()`, name, hash).
		Scan(&state.Loki.ID, &state.Loki.DisplayName, &state.Loki.Connected, &state.Loki.CreatedAt, &state.Loki.LastSeenAt, &state.ServerTime)
	return state, err
}

func (s *IdentityStore) Connect(ctx context.Context, hash []byte) (protocol.State, error) {
	return s.visit(ctx, hash, "connect", nil)
}

func (s *IdentityStore) State(ctx context.Context, hash []byte) (protocol.State, error) {
	return s.visit(ctx, hash, "state", nil)
}

func (s *IdentityStore) Disconnect(ctx context.Context, hash []byte) error {
	_, err := s.visit(ctx, hash, "disconnect", nil)
	return err
}

func (s *IdentityStore) visit(ctx context.Context, hash []byte, action string, operation func(pgx.Tx, *protocol.State) error) (protocol.State, error) {
	return s.withLoki(ctx, "token_hash", hash, action, operation)
}

func (s *IdentityStore) withLoki(ctx context.Context, column string, key any, action string, operation func(pgx.Tx, *protocol.State) error) (protocol.State, error) {
	var state protocol.State
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return state, err
	}
	defer tx.Rollback(context.Background())
	err = tx.QueryRow(ctx, `SELECT id::text, display_name, connected, created_at, last_seen_at
		FROM game.loki WHERE `+column+` = $1 FOR UPDATE`, key).
		Scan(&state.Loki.ID, &state.Loki.DisplayName, &state.Loki.Connected, &state.Loki.CreatedAt, &state.Loki.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if column == "id" {
			return state, ErrNotFound
		}
		return state, ErrUnknownIdentity
	}
	if err != nil {
		return state, err
	}
	// Read the current database clock after the row lock, not transaction-start time.
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&state.ServerTime); err != nil {
		return state, err
	}
	stale := state.Loki.Connected && state.Loki.LastSeenAt != nil && !state.ServerTime.Before(state.Loki.LastSeenAt.Add(30*time.Second))
	if stale || action == "disconnect" {
		if err := disconnectActivity(ctx, tx, state.Loki.ID, state.ServerTime); err != nil {
			return state, err
		}
		state.Loki.Connected = false
	}
	if action == "connect" {
		state.Loki.Connected = true
	}
	if action == "disconnect" {
		state.Loki.Connected = false
	}
	if state.Loki.Connected && action != "inspect" {
		state.Loki.LastSeenAt = &state.ServerTime
	}
	if _, err := tx.Exec(ctx, "UPDATE game.loki SET connected = $2, last_seen_at = $3 WHERE id = $1", state.Loki.ID, state.Loki.Connected, state.Loki.LastSeenAt); err != nil {
		return state, err
	}
	if err := expireInvitation(ctx, tx, state.Loki.ID, state.ServerTime); err != nil {
		return state, err
	}
	var operationErr error
	if action == "state" && !state.Loki.Connected {
		operationErr = ErrReconnect
	} else if operation != nil {
		operationErr = operation(tx, &state)
	}
	if operationErr != nil && !businessError(operationErr) {
		return state, operationErr
	}
	if err := snapshot(ctx, tx, &state); err != nil {
		return state, err
	}
	// Lifecycle cleanup must survive a rejected late response or poll.
	// A request may have waited on a row after passing the HTTP ownership guard.
	if err := s.CheckServer(ctx); err != nil {
		return state, err
	}
	if err := tx.Commit(ctx); err != nil {
		return state, err
	}
	return state, operationErr
}

func businessError(err error) bool {
	return errors.Is(err, ErrReconnect) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrExpired) ||
		errors.Is(err, ErrConflict) || errors.Is(err, ErrBusy) || errors.Is(err, ErrOffline) || errors.Is(err, ErrStaleMove) ||
		errors.Is(err, game.ErrWrongTurn) || errors.Is(err, game.ErrCellOccupied) || errors.Is(err, game.ErrFinished) || errors.Is(err, game.ErrOutOfBounds)
}

func (s *IdentityStore) ResetPresence(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	rows, err := tx.Query(ctx, "SELECT id::text FROM game.loki ORDER BY id FOR UPDATE")
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return err
	}
	for _, id := range ids {
		if err := disconnectActivity(ctx, tx, id, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "UPDATE game.loki SET connected = false WHERE connected"); err != nil {
		return err
	}
	if err := s.CheckServer(ctx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *IdentityStore) Expire(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM game.loki l WHERE
		(connected AND last_seen_at <= clock_timestamp() - interval '30 seconds') OR
		EXISTS (SELECT 1 FROM game.invitations i WHERE i.loki_id = l.id AND i.status = 'pending' AND i.expires_at <= clock_timestamp())
		ORDER BY id LIMIT 100`)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.withLoki(ctx, "id", id, "inspect", nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *IdentityStore) List(ctx context.Context, cursor string, onlineOnly bool) (protocol.LokiList, error) {
	result := protocol.LokiList{Items: []protocol.LokiStatus{}}
	if err := s.pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&result.ServerTime); err != nil {
		return result, err
	}
	if cursor != "" {
		var id pgtype.UUID
		if err := id.Scan(cursor); err != nil || !id.Valid {
			return result, ErrInvalidCursor
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT l.id::text, l.display_name,
		l.connected AND l.last_seen_at > clock_timestamp() - interval '30 seconds', l.created_at, l.last_seen_at, to_jsonb(i), to_jsonb(g),
		(SELECT count(*) FROM game.moves m WHERE m.game_id=g.id), score.wins, score.played,
		CASE WHEN score.played > 0 THEN score.wins::double precision / score.played END
		FROM game.loki l
		LEFT JOIN LATERAL (SELECT * FROM game.invitations WHERE loki_id = l.id ORDER BY created_at DESC, id DESC LIMIT 1) i ON true
		LEFT JOIN game.games g ON g.loki_id = l.id AND g.status = 'active'
		LEFT JOIN LATERAL (SELECT count(*) played, count(*) FILTER (WHERE result='loki_win') wins
			FROM game.games WHERE loki_id=l.id AND status='completed') score ON true
		WHERE (NULLIF($1, '')::uuid IS NULL OR l.id > NULLIF($1, '')::uuid)
		AND (NOT $2 OR (l.connected AND l.last_seen_at > clock_timestamp() - interval '30 seconds'))
		ORDER BY l.id LIMIT 51`, cursor, onlineOnly)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item protocol.LokiStatus
		var count int
		if err := rows.Scan(&item.ID, &item.DisplayName, &item.Connected, &item.CreatedAt, &item.LastSeenAt, &item.Invitation, &item.Game, &count,
			&item.Statistics.Wins, &item.Statistics.Played, &item.Statistics.WinRate); err != nil {
			return result, err
		}
		if item.Game != nil {
			fillTurn(item.Game, count)
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
