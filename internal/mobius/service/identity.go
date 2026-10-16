package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"time"

	"onec/internal/mobius/postgres"
	"onec/internal/protocol"
)

var ErrInvalidName = errors.New("display name is required")

type Identity struct{ store *postgres.IdentityStore }

func NewIdentity(store *postgres.IdentityStore) *Identity { return &Identity{store: store} }

func (s *Identity) Register(ctx context.Context, name string) (protocol.Registration, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return protocol.Registration{}, ErrInvalidName
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return protocol.Registration{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(secret[:])
	hash := sha256.Sum256([]byte(token))
	state, err := s.store.Register(ctx, name, hash[:])
	return protocol.Registration{Token: token, State: state}, err
}

func tokenHash(token string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return nil, postgres.ErrUnknownIdentity
	}
	hash := sha256.Sum256([]byte(token))
	return hash[:], nil
}

func (s *Identity) Connect(ctx context.Context, token string) (protocol.State, error) {
	hash, err := tokenHash(token)
	if err != nil {
		return protocol.State{}, err
	}
	return s.store.Connect(ctx, hash)
}

func (s *Identity) State(ctx context.Context, token string) (protocol.State, error) {
	hash, err := tokenHash(token)
	if err != nil {
		return protocol.State{}, err
	}
	return s.store.State(ctx, hash)
}

func (s *Identity) Disconnect(ctx context.Context, token string) error {
	hash, err := tokenHash(token)
	if err != nil {
		return err
	}
	return s.store.Disconnect(ctx, hash)
}

func (s *Identity) List(ctx context.Context, cursor string, onlineOnly bool) (protocol.LokiList, error) {
	return s.store.List(ctx, cursor, onlineOnly)
}

func (s *Identity) ResetPresence(ctx context.Context) error { return s.store.ResetPresence(ctx) }

func (s *Identity) RunPresence(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := s.store.CheckServer(checkCtx)
			if err == nil {
				err = s.store.Expire(checkCtx)
			}
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.Error("presence check failed")
			}
		}
	}
}
