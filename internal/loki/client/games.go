package client

import (
	"context"
	"net/http"

	"onec/internal/protocol"
)

func (c *Client) Play(ctx context.Context, id string, request protocol.MoveRequest) error {
	if !protocol.IsUUID(id) || !request.Valid() {
		return ErrMoveRequest
	}
	c.operation.Lock()
	defer c.operation.Unlock()
	if !c.enabled {
		return ErrNotConnected
	}
	var result protocol.Game
	err := c.request(ctx, http.MethodPost, "/api/v1/loki/games/"+id+"/moves", request, &result)
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
