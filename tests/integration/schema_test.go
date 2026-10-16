//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"onec/migrations"
)

func TestGameSchemaConstraints(t *testing.T) {
	ctx, pool := testDatabase(t)
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	id := func(sql string, args ...any) string {
		t.Helper()
		var value string
		if err := tx.QueryRow(ctx, sql, args...).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	loki := id("INSERT INTO game.loki (display_name, token_hash) VALUES ('Loki', decode(repeat('01', 32), 'hex')) RETURNING id::text")
	other := id("INSERT INTO game.loki (display_name, token_hash) VALUES ('Loki', decode(repeat('02', 32), 'hex')) RETURNING id::text")
	newInvite := func(owner, status string) string {
		t.Helper()
		return id(`INSERT INTO game.invitations (loki_id, request_id, mobius_side, status, expires_at, responded_at, statistics_snapshot)
			VALUES ($1, gen_random_uuid(), 'X', $2, clock_timestamp() + interval '30 seconds',
			CASE WHEN $2 = 'pending' THEN NULL ELSE clock_timestamp() END, '{}') RETURNING id::text`, owner, status)
	}
	invite := newInvite(loki, "accepted")
	gameID := id("INSERT INTO game.games (loki_id, invitation_id, mobius_side) VALUES ($1, $2, 'X') RETURNING id::text", loki, invite)
	exec("UPDATE game.invitations SET game_id = $1 WHERE id = $2", gameID, invite)
	otherInvite := newInvite(other, "accepted")
	otherGame := id("INSERT INTO game.games (loki_id, invitation_id, mobius_side) VALUES ($1, $2, 'X') RETURNING id::text", other, otherInvite)
	moveRequest := "00000000-0000-0000-0000-000000000001"
	moveID := id(`INSERT INTO game.moves (game_id, number, actor, row_index, column_index, request_id)
		VALUES ($1, 1, 'mobius', 0, 0, $2) RETURNING id::text`, gameID, moveRequest)
	exec(`INSERT INTO game.events (loki_id, sequence, type, game_id, move_id, payload)
		VALUES ($1, 1, 'mobius_move', $2, $3, '{}')`, loki, gameID, moveID)
	// Sequence numbers are private to a Loki; another Loki may also have sequence 1.
	exec("INSERT INTO game.events (loki_id, sequence, type, game_id, payload) VALUES ($1, 1, 'game_started', $2, '{}')", other, otherGame)
	secondInvite := newInvite(loki, "accepted")
	pending := newInvite(other, "pending")

	cases := []struct {
		name       string
		code       string
		constraint string
		sql        string
		args       []any
	}{
		{"duplicate_cell", "23505", "moves_cell", "INSERT INTO game.moves (game_id, number, actor, row_index, column_index, request_id) VALUES ($1, 2, 'loki', 0, 0, gen_random_uuid())", []any{gameID}},
		{"duplicate_number", "23505", "moves_number", "INSERT INTO game.moves (game_id, number, actor, row_index, column_index, request_id) VALUES ($1, 1, 'loki', 0, 1, gen_random_uuid())", []any{gameID}},
		{"duplicate_request", "23505", "moves_request", "INSERT INTO game.moves (game_id, number, actor, row_index, column_index, request_id) VALUES ($1, 2, 'loki', 0, 1, $2)", []any{gameID, moveRequest}},
		{"two_active_games", "23505", "games_one_active", "INSERT INTO game.games (loki_id, invitation_id, mobius_side) VALUES ($1, $2, 'X')", []any{loki, secondInvite}},
		{"two_pending_invites", "23505", "invitations_one_pending", "INSERT INTO game.invitations (loki_id, request_id, mobius_side, expires_at, statistics_snapshot) VALUES ($1, gen_random_uuid(), 'O', clock_timestamp() + interval '30 seconds', '{}')", []any{other}},
		{"foreign_invitation", "23503", "games_invitation_owner", "INSERT INTO game.games (loki_id, invitation_id, mobius_side, status, result, finished_at) VALUES ($1, $2, 'X', 'completed', 'draw', clock_timestamp())", []any{loki, pending}},
		{"foreign_game_event", "23503", "events_game_owner", "INSERT INTO game.events (loki_id, sequence, type, game_id, payload) VALUES ($1, 2, 'game_started', $2, '{}')", []any{other, gameID}},
		{"foreign_invitation_event", "23503", "events_invitation_owner", "INSERT INTO game.events (loki_id, sequence, type, invitation_id, payload) VALUES ($1, 2, 'invitation_created', $2, '{}')", []any{loki, pending}},
		{"move_from_another_game", "23503", "events_move", "INSERT INTO game.events (loki_id, sequence, type, game_id, move_id, payload) VALUES ($1, 2, 'mobius_move', $2, $3, '{}')", []any{other, otherGame, moveID}},
		{"duplicate_event_sequence", "23505", "events_sequence", "INSERT INTO game.events (loki_id, sequence, type, game_id, payload) VALUES ($1, 1, 'game_started', $2, '{}')", []any{loki, gameID}},
		{"completed_without_result", "23514", "games_result", "UPDATE game.games SET status = 'completed', finished_at = clock_timestamp() WHERE id = $1", []any{gameID}},
		{"active_with_result", "23514", "games_result", "UPDATE game.games SET result = 'draw' WHERE id = $1", []any{gameID}},
		{"invalid_result", "23514", "games_result", "UPDATE game.games SET status = 'completed', result = 'unknown', finished_at = clock_timestamp() WHERE id = $1", []any{gameID}},
		{"completed_without_time", "23514", "games_result", "UPDATE game.games SET status = 'completed', result = 'draw' WHERE id = $1", []any{gameID}},
		{"invalid_side", "23514", "games_mobius_side_check", "UPDATE game.games SET mobius_side = 'Z' WHERE id = $1", []any{gameID}},
		{"row_out_of_bounds", "23514", "moves_row_index_check", "UPDATE game.moves SET row_index = 3 WHERE id = $1", []any{moveID}},
		{"negative_row", "23514", "moves_row_index_check", "UPDATE game.moves SET row_index = -1 WHERE id = $1", []any{moveID}},
		{"column_out_of_bounds", "23514", "moves_column_index_check", "UPDATE game.moves SET column_index = -1 WHERE id = $1", []any{moveID}},
		{"column_too_large", "23514", "moves_column_index_check", "UPDATE game.moves SET column_index = 3 WHERE id = $1", []any{moveID}},
		{"number_out_of_bounds", "23514", "moves_number_check", "UPDATE game.moves SET number = 10 WHERE id = $1", []any{moveID}},
		{"zero_move_number", "23514", "moves_number_check", "UPDATE game.moves SET number = 0 WHERE id = $1", []any{moveID}},
		{"unknown_actor", "23514", "moves_actor_check", "UPDATE game.moves SET actor = 'stranger' WHERE id = $1", []any{moveID}},
		{"move_event_without_move", "23514", "events_move_reference", "UPDATE game.events SET move_id = NULL WHERE loki_id = $1", []any{loki}},
		{"wrong_game_backreference", "23503", "invitations_game", "UPDATE game.invitations SET game_id = $1 WHERE id = $2", []any{otherGame, invite}},
		{"short_token_hash", "23514", "loki_token_hash_check", "UPDATE game.loki SET token_hash = decode('ff', 'hex') WHERE id = $1", []any{loki}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			savepoint, err := tx.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer savepoint.Rollback(ctx)
			_, err = savepoint.Exec(ctx, tc.sql, tc.args...)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tc.code || pgErr.ConstraintName != tc.constraint {
				t.Fatalf("got %v, want SQLSTATE %s, constraint %s", err, tc.code, tc.constraint)
			}
		})
	}

	// A completed game no longer occupies the active-game slot.
	exec("UPDATE game.games SET status = 'completed', result = 'draw', finished_at = clock_timestamp() WHERE id = $1", gameID)
	exec("UPDATE game.invitations SET mobius_side = 'O' WHERE id = $1", secondInvite)
	newGame := id("INSERT INTO game.games (loki_id, invitation_id, mobius_side) VALUES ($1, $2, 'O') RETURNING id::text", loki, secondInvite)
	exec("UPDATE game.invitations SET game_id = $1 WHERE id = $2", newGame, secondInvite)
	newMove := id("INSERT INTO game.moves (game_id, number, actor, row_index, column_index, request_id) VALUES ($1, 1, 'loki', 0, 0, $2) RETURNING id::text", newGame, moveRequest)
	exec("INSERT INTO game.events (loki_id, sequence, type, game_id, payload) VALUES ($1, 2, 'game_started', $2, '{}')", loki, newGame)
	exec("DELETE FROM game.games WHERE id = $1", newGame)
	assertCount(t, ctx, tx, "SELECT count(*) FROM game.moves WHERE id = $1", newMove, 0)
	assertCount(t, ctx, tx, "SELECT count(*) FROM game.events WHERE game_id = $1", newGame, 0)
	assertCount(t, ctx, tx, "SELECT count(*) FROM game.invitations WHERE id = $1 AND game_id IS NULL", secondInvite, 1)
	assertCount(t, ctx, tx, "SELECT count(*) FROM game.games WHERE id = $1 AND result = 'draw'", gameID, 1)
	assertCount(t, ctx, tx, "SELECT count(*) FROM game.moves WHERE id = $1", moveID, 1)
	assertCount(t, ctx, tx, "SELECT count(*) FROM game.events WHERE game_id = $1", gameID, 1)
	assertCount(t, ctx, tx, "SELECT count(*) FROM game.games WHERE id = $1 AND status = 'active'", otherGame, 1)
}

func assertCount(t *testing.T, ctx context.Context, tx pgx.Tx, sql string, arg any, want int) {
	t.Helper()
	var got int
	if err := tx.QueryRow(ctx, sql, arg).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s: got %d, want %d", sql, got, want)
	}
}
