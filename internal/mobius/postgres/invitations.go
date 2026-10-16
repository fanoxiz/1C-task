package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"onec/internal/protocol"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrExpired  = errors.New("invitation expired")
	ErrOffline  = errors.New("client offline")
	ErrBusy     = errors.New("client busy")
)

const invitationColumns = `id::text, loki_id::text, mobius_side, status, created_at, expires_at, responded_at, game_id::text, statistics_snapshot`

func scanInvitation(row pgx.Row) (protocol.Invitation, error) {
	var invitation protocol.Invitation
	err := row.Scan(&invitation.ID, &invitation.LokiID, &invitation.MobiusSide, &invitation.Status,
		&invitation.CreatedAt, &invitation.ExpiresAt, &invitation.RespondedAt, &invitation.GameID, &invitation.Statistics)
	return invitation, err
}

func emit(ctx context.Context, tx pgx.Tx, owner, kind string, invitationID, gameID *string, payload any) error {
	return emitWithMove(ctx, tx, owner, kind, invitationID, gameID, nil, payload)
}

func emitWithMove(ctx context.Context, tx pgx.Tx, owner, kind string, invitationID, gameID, moveID *string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `WITH counter AS (
		UPDATE game.loki SET event_sequence = event_sequence + 1 WHERE id = $1 RETURNING event_sequence
	) INSERT INTO game.events (loki_id, sequence, type, invitation_id, game_id, move_id, payload)
		SELECT $1, event_sequence, $2, $3, $4, $5, $6 FROM counter`, owner, kind, invitationID, gameID, moveID, data)
	return err
}

// The caller must hold the owner's row lock before changing invitations or games.
func expireInvitation(ctx context.Context, tx pgx.Tx, owner string, now time.Time) error {
	invitation, err := scanInvitation(tx.QueryRow(ctx, `UPDATE game.invitations
		SET status = 'expired', responded_at = $2 WHERE loki_id = $1 AND status = 'pending' AND expires_at <= $2
		RETURNING `+invitationColumns, owner, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return emit(ctx, tx, owner, "invitation_closed", &invitation.ID, nil, invitation)
}

func disconnectActivity(ctx context.Context, tx pgx.Tx, owner string, now time.Time) error {
	invitation, err := scanInvitation(tx.QueryRow(ctx, `UPDATE game.invitations SET status = 'cancelled', responded_at = $2
		WHERE loki_id = $1 AND status = 'pending' RETURNING `+invitationColumns, owner, now))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		if err := emit(ctx, tx, owner, "invitation_closed", &invitation.ID, nil, invitation); err != nil {
			return err
		}
	}
	var gameID string
	err = tx.QueryRow(ctx, "DELETE FROM game.games WHERE loki_id = $1 AND status = 'active' RETURNING id::text", owner).Scan(&gameID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return emit(ctx, tx, owner, "game_cancelled", nil, nil, map[string]string{"game_id": gameID})
}

func snapshot(ctx context.Context, tx pgx.Tx, state *protocol.State) error {
	var gameID *string
	err := tx.QueryRow(ctx, `SELECT
		(SELECT to_jsonb(i) FROM game.invitations i WHERE loki_id = $1 AND status = 'pending'),
		(SELECT id::text FROM game.games WHERE loki_id = $1
		 ORDER BY (status='active') DESC, finished_at DESC NULLS LAST, id DESC LIMIT 1)`, state.Loki.ID).
		Scan(&state.Invitation, &gameID)
	if err == nil && gameID != nil {
		var game protocol.Game
		game, _, err = loadGame(ctx, tx, *gameID, state.Loki.ID)
		state.Game = &game
	}
	return err
}

func fillSides(game *protocol.GameSummary) {
	game.LokiSide, game.NextActor = "O", "mobius"
	if game.MobiusSide == "O" {
		game.LokiSide, game.NextActor = "X", "loki"
	}
}

func fillTurn(game *protocol.GameSummary, count int) {
	fillSides(game)
	if count%2 == 1 {
		if game.NextActor == "mobius" {
			game.NextActor = "loki"
		} else {
			game.NextActor = "mobius"
		}
	}
	if game.Status == "completed" {
		game.NextActor = ""
	}
}

func statistics(ctx context.Context, tx pgx.Tx, owner string) (protocol.Statistics, error) {
	var result protocol.Statistics
	err := tx.QueryRow(ctx, `WITH scores AS (
		SELECT loki_id, count(*)::bigint played, count(*) FILTER (WHERE result = 'loki_win')::bigint wins
		FROM game.games WHERE status = 'completed' GROUP BY loki_id
	), mine AS (
		SELECT COALESCE(s.played, 0) played, COALESCE(s.wins, 0) wins
		FROM game.loki l LEFT JOIN scores s ON s.loki_id = l.id WHERE l.id = $1
	) SELECT m.wins, m.played,
		CASE WHEN m.played > 0 THEN m.wins::double precision / m.played END,
		CASE WHEN m.played > 0 AND count(s.loki_id) > 0 THEN
			round(100.0 * count(s.loki_id) FILTER (WHERE s.wins::numeric * m.played > m.wins::numeric * s.played) / count(s.loki_id))::integer END
		FROM mine m LEFT JOIN scores s ON s.loki_id <> $1 GROUP BY m.wins, m.played`, owner).
		Scan(&result.Wins, &result.Played, &result.WinRate, &result.BetterPercent)
	if result.Played == 0 {
		result.Message = "Вы ещё не сыграли ни одной партии. Самое время начать!"
	} else if result.BetterPercent == nil {
		result.Message = "Пока недостаточно данных для сравнения"
	} else {
		result.Message = fmt.Sprintf("Вы хуже %d %% своих версий. Вы вообще стараетесь?", *result.BetterPercent)
	}
	return result, err
}

func (s *IdentityStore) Invite(ctx context.Context, owner, requestID, side string) (protocol.Invitation, error) {
	var result protocol.Invitation
	_, err := s.withLoki(ctx, "id", owner, "inspect", func(tx pgx.Tx, state *protocol.State) error {
		previous, err := scanInvitation(tx.QueryRow(ctx, "SELECT "+invitationColumns+" FROM game.invitations WHERE request_id = $1", requestID))
		checkPrevious := func(previous protocol.Invitation) error {
			if previous.LokiID != owner || previous.MobiusSide != side {
				return ErrConflict
			}
			result = previous
			return nil
		}
		if err == nil {
			return checkPrevious(previous)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !state.Loki.Connected {
			return ErrOffline
		}
		var busy bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM game.games WHERE loki_id = $1 AND status = 'active')
			OR EXISTS(SELECT 1 FROM game.invitations WHERE loki_id = $1 AND status = 'pending')`, owner).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return ErrBusy
		}
		score, err := statistics(ctx, tx, owner)
		if err != nil {
			return err
		}
		data, err := json.Marshal(score)
		if err != nil {
			return err
		}
		result, err = scanInvitation(tx.QueryRow(ctx, `INSERT INTO game.invitations
			(loki_id, request_id, mobius_side, created_at, expires_at, statistics_snapshot)
			VALUES ($1, $2, $3, $4, $4::timestamptz + interval '30 seconds', $5)
			ON CONFLICT (request_id) DO NOTHING RETURNING `+invitationColumns, owner, requestID, side, state.ServerTime, data))
		if errors.Is(err, pgx.ErrNoRows) {
			previous, err = scanInvitation(tx.QueryRow(ctx, "SELECT "+invitationColumns+" FROM game.invitations WHERE request_id = $1", requestID))
			if err != nil {
				return err
			}
			return checkPrevious(previous)
		}
		if err != nil {
			return err
		}
		return emit(ctx, tx, owner, "invitation_created", &result.ID, nil, result)
	})
	return result, err
}

func (s *IdentityStore) Respond(ctx context.Context, hash []byte, invitationID, decision string) (protocol.Invitation, error) {
	var result protocol.Invitation
	_, err := s.visit(ctx, hash, "state", func(tx pgx.Tx, state *protocol.State) error {
		var err error
		result, err = scanInvitation(tx.QueryRow(ctx, "SELECT "+invitationColumns+" FROM game.invitations WHERE id = $1 AND loki_id = $2 FOR UPDATE", invitationID, state.Loki.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		wanted := "declined"
		if decision == "accept" {
			wanted = "accepted"
		}
		if result.Status == wanted {
			return nil
		}
		if result.Status == "expired" {
			return ErrExpired
		}
		if result.Status != "pending" {
			return ErrConflict
		}
		// The invitation row could have waited on a lock; check the deadline again.
		if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&state.ServerTime); err != nil {
			return err
		}
		if !state.ServerTime.Before(result.ExpiresAt) {
			if err := expireInvitation(ctx, tx, state.Loki.ID, state.ServerTime); err != nil {
				return err
			}
			return ErrExpired
		}
		result.Status, result.RespondedAt = wanted, &state.ServerTime
		if decision == "accept" {
			var gameID string
			if err := tx.QueryRow(ctx, `INSERT INTO game.games (loki_id, invitation_id, mobius_side)
				VALUES ($1, $2, $3) RETURNING id::text`, state.Loki.ID, result.ID, result.MobiusSide).Scan(&gameID); err != nil {
				return err
			}
			result.GameID = &gameID
		}
		if _, err := tx.Exec(ctx, `UPDATE game.invitations SET status = $2, responded_at = $3, game_id = $4 WHERE id = $1`, result.ID, result.Status, result.RespondedAt, result.GameID); err != nil {
			return err
		}
		if err := emit(ctx, tx, state.Loki.ID, "invitation_closed", &result.ID, nil, result); err != nil {
			return err
		}
		if result.GameID != nil {
			game := protocol.GameSummary{ID: *result.GameID, LokiID: state.Loki.ID, MobiusSide: result.MobiusSide, Status: "active"}
			fillSides(&game)
			return emit(ctx, tx, state.Loki.ID, "game_started", &result.ID, result.GameID, game)
		}
		return nil
	})
	return result, err
}

func (s *IdentityStore) Invitations(ctx context.Context, owner string) ([]protocol.Invitation, error) {
	items := []protocol.Invitation{}
	_, err := s.withLoki(ctx, "id", owner, "inspect", func(tx pgx.Tx, _ *protocol.State) error {
		rows, err := tx.Query(ctx, "SELECT "+invitationColumns+" FROM game.invitations WHERE loki_id = $1 ORDER BY created_at DESC, id DESC LIMIT 50", owner)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanInvitation(rows)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (s *IdentityStore) Events(ctx context.Context, hash []byte, limit int) (protocol.EventBatch, error) {
	result := protocol.EventBatch{Items: []protocol.Event{}}
	_, err := s.visit(ctx, hash, "state", func(tx pgx.Tx, state *protocol.State) error {
		rows, err := tx.Query(ctx, `SELECT id::text, sequence, type, created_at, payload FROM game.events
			WHERE loki_id = $1 AND acked_at IS NULL ORDER BY sequence LIMIT $2`, state.Loki.ID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event protocol.Event
			if err := rows.Scan(&event.ID, &event.Sequence, &event.Type, &event.CreatedAt, &event.Payload); err != nil {
				return err
			}
			result.Items = append(result.Items, event)
		}
		return rows.Err()
	})
	return result, err
}

func (s *IdentityStore) Ack(ctx context.Context, hash []byte, id string) error {
	_, err := s.visit(ctx, hash, "state", func(tx pgx.Tx, state *protocol.State) error {
		var moveID *string
		err := tx.QueryRow(ctx, `UPDATE game.events SET acked_at = COALESCE(acked_at, $3)
			WHERE id = $1 AND loki_id = $2 RETURNING move_id::text`, id, state.Loki.ID, state.ServerTime).Scan(&moveID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if moveID != nil {
			_, err = tx.Exec(ctx, "UPDATE game.moves SET delivered_at = COALESCE(delivered_at, $2) WHERE id = $1 AND actor = 'mobius'", moveID, state.ServerTime)
		}
		return err
	})
	return err
}
