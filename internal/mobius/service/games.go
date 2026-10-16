package service

import (
	"context"
	"strings"

	"onec/internal/protocol"
)

func (s *Identity) ReadGame(ctx context.Context, token, id string) (protocol.Game, error) {
	if !protocol.IsUUID(id) {
		return protocol.Game{}, ErrInvalidRequest
	}
	var hash []byte
	var err error
	if token != "" {
		hash, err = tokenHash(token)
		if err != nil {
			return protocol.Game{}, err
		}
	}
	return s.store.ReadGame(ctx, hash, strings.ToLower(id))
}

func (s *Identity) Play(ctx context.Context, token, id string, request protocol.MoveRequest) (protocol.Game, error) {
	if !protocol.IsUUID(id) || !request.Valid() {
		return protocol.Game{}, ErrInvalidRequest
	}
	actor := "mobius"
	var hash []byte
	var err error
	if token != "" {
		actor = "loki"
		hash, err = tokenHash(token)
		if err != nil {
			return protocol.Game{}, err
		}
	}
	request.RequestID = strings.ToLower(request.RequestID)
	return s.store.Play(ctx, hash, strings.ToLower(id), actor, request)
}

func (s *Identity) ActiveGames(ctx context.Context, cursor string) (protocol.GameList, error) {
	if cursor != "" && !protocol.IsUUID(cursor) {
		return protocol.GameList{}, ErrInvalidRequest
	}
	return s.store.ActiveGames(ctx, cursor)
}
