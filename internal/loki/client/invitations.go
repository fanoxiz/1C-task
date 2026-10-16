package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"onec/internal/protocol"
)

func (c *Client) Respond(ctx context.Context, id, decision string) error {
	if !protocol.IsUUID(id) || (decision != "accept" && decision != "decline") {
		return ErrDecision
	}
	c.operation.Lock()
	defer c.operation.Unlock()
	if !c.enabled {
		return ErrNotConnected
	}
	var invitation protocol.Invitation
	err := c.request(ctx, http.MethodPost, "/api/v1/loki/invitations/"+id+"/response", map[string]string{"decision": decision}, &invitation)
	// Refresh even after a rejected/expired response, so the old invitation disappears.
	var state protocol.State
	if syncErr := c.request(ctx, http.MethodGet, "/api/v1/loki/state", nil, &state); syncErr != nil {
		c.failed(syncErr)
		if err == nil {
			return syncErr
		}
	} else {
		c.update("connected", &state, nil)
	}
	return err
}

func (c *Client) consumeEvents(ctx context.Context) error {
	for {
		var batch protocol.EventBatch
		if err := c.request(ctx, http.MethodGet, "/api/v1/loki/events?limit=100", nil, &batch); err != nil {
			return err
		}
		if len(batch.Items) == 0 {
			return nil
		}
		var sequence int64
		for _, event := range batch.Items {
			if !protocol.IsUUID(event.ID) || event.Sequence <= sequence {
				return ErrResponse
			}
			sequence = event.Sequence
			var object map[string]json.RawMessage
			if json.Unmarshal(event.Payload, &object) != nil || object == nil {
				return ErrResponse
			}
			switch event.Type {
			case "invitation_created", "invitation_closed", "game_started", "game_cancelled", "mobius_move", "game_finished":
			default:
				return ErrResponse
			}
		}
		// Events can be duplicated or stale. Apply the authoritative snapshot before ACK.
		var state protocol.State
		if err := c.request(ctx, http.MethodGet, "/api/v1/loki/state", nil, &state); err != nil {
			return err
		}
		if state.Loki.ID == "" || !state.Loki.Connected {
			return ErrResponse
		}
		var notice string
		for _, event := range batch.Items {
			if event.Type == "mobius_move" {
				if err := c.verifyMoveEvent(ctx, event, state); err != nil {
					return err
				}
			}
			text, err := eventNotice(event, state)
			if err != nil {
				return err
			}
			if text != "" {
				notice = text
			}
		}
		c.update("connected", &state, nil)
		if notice != "" {
			c.mu.Lock()
			c.view.Notice = notice
			c.mu.Unlock()
		}
		for _, event := range batch.Items {
			err := c.request(ctx, http.MethodPost, "/api/v1/loki/events/"+event.ID+"/ack", nil, nil)
			if errors.Is(err, ErrNotFound) {
				if err := c.request(ctx, http.MethodGet, "/api/v1/loki/state", nil, &state); err != nil {
					return err
				}
				c.update("connected", &state, nil)
			} else if err != nil {
				return err
			}
		}
		if len(batch.Items) < 100 {
			return nil
		}
	}
}

func eventNotice(event protocol.Event, state protocol.State) (string, error) {
	switch event.Type {
	case "invitation_created", "invitation_closed":
		var invitation protocol.Invitation
		if json.Unmarshal(event.Payload, &invitation) != nil || !protocol.IsUUID(invitation.ID) || invitation.LokiID != state.Loki.ID {
			return "", ErrResponse
		}
		if event.Type == "invitation_created" {
			if invitation.Status != "pending" {
				return "", ErrResponse
			}
			return "", nil
		}
		switch invitation.Status {
		case "accepted", "declined", "expired", "cancelled":
		default:
			return "", ErrResponse
		}
		if state.Invitation == nil && (state.Game == nil || state.Game.Status == "completed") {
			switch invitation.Status {
			case "declined":
				return "Приглашение отклонено.", nil
			case "expired":
				return "Время ответа на приглашение истекло.", nil
			case "cancelled":
				return "Приглашение отменено при отключении.", nil
			}
		}
	case "game_started":
		var game protocol.GameSummary
		if json.Unmarshal(event.Payload, &game) != nil || !protocol.IsUUID(game.ID) {
			return "", ErrResponse
		}
	case "game_finished":
		var game protocol.Game
		if json.Unmarshal(event.Payload, &game) != nil || !protocol.IsUUID(game.ID) || game.LokiID != state.Loki.ID || game.Status != "completed" ||
			(game.Result != "loki_win" && game.Result != "mobius_win" && game.Result != "draw") {
			return "", ErrResponse
		}
	case "game_cancelled":
		var payload struct {
			GameID string `json:"game_id"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil || !protocol.IsUUID(payload.GameID) {
			return "", ErrResponse
		}
		if state.Game == nil || state.Game.Status == "completed" {
			return "Незаконченная партия удалена и не учтена в статистике.", nil
		}
	}
	return "", nil
}

func (c *Client) verifyMoveEvent(ctx context.Context, event protocol.Event, state protocol.State) error {
	var payload protocol.MoveEvent
	if json.Unmarshal(event.Payload, &payload) != nil || !protocol.IsUUID(payload.Game.ID) || payload.Game.LokiID != state.Loki.ID ||
		!protocol.IsUUID(payload.Move.ID) || !protocol.IsUUID(payload.Move.RequestID) || payload.Move.Actor != "mobius" || payload.Move.Number < 1 || payload.Move.Number > 9 {
		return ErrResponse
	}
	current := state.Game
	if current == nil || current.ID != payload.Game.ID {
		var previous protocol.Game
		if err := c.request(ctx, http.MethodGet, "/api/v1/loki/games/"+payload.Game.ID, nil, &previous); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		current = &previous
	}
	if current.LokiID != state.Loki.ID || len(current.Moves) < payload.Move.Number {
		return ErrResponse
	}
	move := current.Moves[payload.Move.Number-1]
	if move.ID != payload.Move.ID || move.RequestID != payload.Move.RequestID || move.Actor != "mobius" || move.Number != payload.Move.Number || move.Side != payload.Move.Side || move.Row != payload.Move.Row || move.Column != payload.Move.Column {
		return ErrResponse
	}
	return nil
}
