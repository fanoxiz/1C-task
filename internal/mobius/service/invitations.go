package service

import (
	"context"
	"errors"
	"strings"

	"onec/internal/protocol"
)

var ErrInvalidRequest = errors.New("invalid invitation request")

func (s *Identity) Invite(ctx context.Context, owner, requestID, side string) (protocol.Invitation, error) {
	if !protocol.IsUUID(owner) || !protocol.IsUUID(requestID) || (side != "X" && side != "O") {
		return protocol.Invitation{}, ErrInvalidRequest
	}
	return s.store.Invite(ctx, strings.ToLower(owner), strings.ToLower(requestID), side)
}

func (s *Identity) Respond(ctx context.Context, token, id, decision string) (protocol.Invitation, error) {
	if !protocol.IsUUID(id) || (decision != "accept" && decision != "decline") {
		return protocol.Invitation{}, ErrInvalidRequest
	}
	hash, err := tokenHash(token)
	if err != nil {
		return protocol.Invitation{}, err
	}
	return s.store.Respond(ctx, hash, id, decision)
}

func (s *Identity) Invitations(ctx context.Context, owner string) ([]protocol.Invitation, error) {
	if !protocol.IsUUID(owner) {
		return nil, ErrInvalidRequest
	}
	return s.store.Invitations(ctx, owner)
}

func (s *Identity) Events(ctx context.Context, token string, limit int) (protocol.EventBatch, error) {
	if limit < 1 || limit > 100 {
		return protocol.EventBatch{}, ErrInvalidRequest
	}
	hash, err := tokenHash(token)
	if err != nil {
		return protocol.EventBatch{}, err
	}
	return s.store.Events(ctx, hash, limit)
}

func (s *Identity) Ack(ctx context.Context, token, id string) error {
	if !protocol.IsUUID(id) {
		return ErrInvalidRequest
	}
	hash, err := tokenHash(token)
	if err != nil {
		return err
	}
	return s.store.Ack(ctx, hash, id)
}
