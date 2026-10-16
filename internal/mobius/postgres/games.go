package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"onec/internal/game"
	"onec/internal/protocol"
)

var ErrStaleMove = errors.New("stale move number")

func loadGame(ctx context.Context, tx pgx.Tx, id, owner string) (protocol.Game, *game.Game, error) {
	result := protocol.Game{Moves: []protocol.Move{}}
	err := tx.QueryRow(ctx, `SELECT id::text, loki_id::text, mobius_side, status, COALESCE(result,''), created_at, finished_at
		FROM game.games WHERE id=$1 AND loki_id=$2 FOR UPDATE`, id, owner).
		Scan(&result.ID, &result.LokiID, &result.MobiusSide, &result.Status, &result.Result, &result.CreatedAt, &result.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil, ErrNotFound
	}
	if err != nil {
		return result, nil, err
	}
	engine, err := game.New(game.Side(result.MobiusSide))
	if err != nil {
		return result, nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text, request_id::text, number, actor, row_index, column_index, created_at, delivered_at
		FROM game.moves WHERE game_id=$1 ORDER BY number`, id)
	if err != nil {
		return result, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var move protocol.Move
		if err := rows.Scan(&move.ID, &move.RequestID, &move.Number, &move.Actor, &move.Row, &move.Column, &move.CreatedAt, &move.DeliveredAt); err != nil {
			return result, nil, err
		}
		if move.Number != len(result.Moves)+1 {
			return result, nil, fmt.Errorf("nonsequential stored moves")
		}
		played, err := engine.Play(game.Actor(move.Actor), move.Row, move.Column)
		if err != nil {
			return result, nil, fmt.Errorf("invalid stored move: %v", err)
		}
		move.Side = string(played.Side)
		result.Moves = append(result.Moves, move)
	}
	if err := rows.Err(); err != nil {
		return result, nil, err
	}
	fillSides(&result.GameSummary)
	board := engine.Board()
	for row := range 3 {
		for column := range 3 {
			result.Board[row][column] = string(board[row][column])
		}
	}
	result.NextActor = string(engine.NextActor())
	if result.Status == "completed" {
		result.NextActor = ""
	}
	return result, engine, nil
}

func (s *IdentityStore) gameOwner(ctx context.Context, id string) (string, error) {
	var owner string
	err := s.pool.QueryRow(ctx, "SELECT loki_id::text FROM game.games WHERE id=$1", id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return owner, err
}

func (s *IdentityStore) ReadGame(ctx context.Context, hash []byte, id string) (protocol.Game, error) {
	var result protocol.Game
	operation := func(tx pgx.Tx, state *protocol.State) error {
		var err error
		result, _, err = loadGame(ctx, tx, id, state.Loki.ID)
		return err
	}
	if hash != nil {
		_, err := s.visit(ctx, hash, "state", operation)
		return result, err
	}
	owner, err := s.gameOwner(ctx, id)
	if err != nil {
		return result, err
	}
	_, err = s.withLoki(ctx, "id", owner, "inspect", operation)
	return result, err
}

func (s *IdentityStore) Play(ctx context.Context, hash []byte, id, actor string, request protocol.MoveRequest) (protocol.Game, error) {
	var result protocol.Game
	operation := func(tx pgx.Tx, state *protocol.State) error {
		var engine *game.Game
		var err error
		result, engine, err = loadGame(ctx, tx, id, state.Loki.ID)
		if err != nil {
			return err
		}
		// Resolve retries before checking turn, completion or the current move number.
		for _, move := range result.Moves {
			if move.RequestID != request.RequestID {
				continue
			}
			if move.Actor != actor || move.Number != request.ExpectedMoveNumber || move.Row != request.Row || move.Column != request.Column {
				return ErrConflict
			}
			return nil
		}
		if result.Status != "active" {
			return game.ErrFinished
		}
		if !state.Loki.Connected {
			return ErrOffline
		}
		if request.ExpectedMoveNumber != len(result.Moves)+1 {
			return ErrStaleMove
		}
		if _, err := engine.Play(game.Actor(actor), request.Row, request.Column); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO game.moves (game_id,number,actor,row_index,column_index,request_id,created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, request.ExpectedMoveNumber, actor, request.Row, request.Column, request.RequestID, state.ServerTime)
		if err != nil {
			return err
		}
		if engine.Result() != "" {
			if _, err := tx.Exec(ctx, "UPDATE game.games SET status='completed',result=$2,finished_at=clock_timestamp() WHERE id=$1", id, string(engine.Result())); err != nil {
				return err
			}
		}
		result, _, err = loadGame(ctx, tx, id, state.Loki.ID)
		if err != nil {
			return err
		}
		move := result.Moves[len(result.Moves)-1]
		if actor == "mobius" {
			if err := emitWithMove(ctx, tx, state.Loki.ID, "mobius_move", nil, &result.ID, &move.ID, protocol.MoveEvent{Game: result, Move: move}); err != nil {
				return err
			}
		}
		if result.Status == "completed" {
			return emit(ctx, tx, state.Loki.ID, "game_finished", nil, &result.ID, result)
		}
		return nil
	}
	if hash != nil {
		_, err := s.visit(ctx, hash, "state", operation)
		return result, err
	}
	owner, err := s.gameOwner(ctx, id)
	if err != nil {
		return result, err
	}
	_, err = s.withLoki(ctx, "id", owner, "inspect", operation)
	return result, err
}

func (s *IdentityStore) ActiveGames(ctx context.Context, cursor string) (protocol.GameList, error) {
	result := protocol.GameList{Items: []protocol.GameSummary{}}
	rows, err := s.pool.Query(ctx, `SELECT g.id::text,g.loki_id::text,g.mobius_side,g.status,
		(SELECT count(*) FROM game.moves m WHERE m.game_id=g.id) FROM game.games g
		WHERE g.status='active' AND (NULLIF($1,'')::uuid IS NULL OR g.id>NULLIF($1,'')::uuid) ORDER BY g.id LIMIT 51`, cursor)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item protocol.GameSummary
		var count int
		if err := rows.Scan(&item.ID, &item.LokiID, &item.MobiusSide, &item.Status, &count); err != nil {
			return result, err
		}
		fillTurn(&item, count)
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
