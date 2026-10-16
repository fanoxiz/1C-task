package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"onec/internal/game"
	"onec/internal/httpjson"
	"onec/internal/mobius/postgres"
	"onec/internal/mobius/service"
	"onec/internal/protocol"
	"onec/web"
)

type Readiness func(context.Context) error

func Mobius(ready Readiness, identities *service.Identity) http.Handler {
	mux := http.NewServeMux()
	healthRoutes(mux, ready)
	mux.Handle("GET /{$}", web.Handler("mobius"))
	mux.Handle("GET /app.js", web.Handler("mobius"))
	mux.Handle("GET /game.js", web.Handler("common"))
	mux.Handle("GET /statistics.js", web.Handler("common"))
	mux.Handle("GET /statistics.css", web.Handler("common"))
	mux.Handle("GET /layout.css", web.Handler("common"))
	mux.HandleFunc("GET /api/v1/mobius/loki", func(w http.ResponseWriter, r *http.Request) {
		online := r.URL.Query().Get("connected")
		if online != "" && online != "true" && online != "false" {
			httpjson.Error(w, 400, "invalid_filter", "Invalid connected filter")
			return
		}
		list, err := identities.List(r.Context(), r.URL.Query().Get("cursor"), online == "true")
		if err != nil {
			identityError(w, err)
			return
		}
		httpjson.Write(w, http.StatusOK, list)
	})
	mux.HandleFunc("POST /api/v1/mobius/loki/{id}/invitations", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RequestID  string `json:"request_id"`
			MobiusSide string `json:"mobius_side"`
		}
		if !httpjson.Decode(w, r, &body) {
			return
		}
		invitation, err := identities.Invite(r.Context(), r.PathValue("id"), body.RequestID, body.MobiusSide)
		if err != nil {
			identityError(w, err)
			return
		}
		httpjson.Write(w, http.StatusOK, invitation)
	})
	mux.HandleFunc("GET /api/v1/mobius/loki/{id}/invitations", func(w http.ResponseWriter, r *http.Request) {
		items, err := identities.Invitations(r.Context(), r.PathValue("id"))
		if err != nil {
			identityError(w, err)
			return
		}
		httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
	})
	mux.HandleFunc("GET /api/v1/mobius/loki/{id}/games", func(w http.ResponseWriter, r *http.Request) {
		items, err := identities.GameHistory(r.Context(), r.PathValue("id"), r.URL.Query().Get("cursor"))
		if err != nil {
			identityError(w, err)
			return
		}
		httpjson.Write(w, http.StatusOK, items)
	})
	mux.HandleFunc("GET /api/v1/mobius/games", func(w http.ResponseWriter, r *http.Request) {
		if status := r.URL.Query().Get("status"); status != "" && status != "active" {
			identityError(w, service.ErrInvalidRequest)
			return
		}
		items, err := identities.ActiveGames(r.Context(), r.URL.Query().Get("cursor"))
		if err != nil {
			identityError(w, err)
			return
		}
		httpjson.Write(w, http.StatusOK, items)
	})
	gameRoutes(mux, "/api/v1/mobius/games", identities, false)
	return httpjson.Local(mux)
}

func Loki(ready Readiness, identities *service.Identity) http.Handler {
	mux := http.NewServeMux()
	healthRoutes(mux, ready)
	mux.HandleFunc("POST /api/v1/loki/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DisplayName string `json:"display_name"`
		}
		if !httpjson.Decode(w, r, &body) {
			return
		}
		registration, err := identities.Register(r.Context(), body.DisplayName)
		if err != nil {
			identityError(w, err)
			return
		}
		httpjson.Write(w, http.StatusCreated, registration)
	})
	for _, route := range []struct{ method, path, action string }{
		{"POST", "/api/v1/loki/connect", "connect"},
		{"POST", "/api/v1/loki/disconnect", "disconnect"},
		{"GET", "/api/v1/loki/state", "state"},
	} {
		mux.HandleFunc(route.method+" "+route.path, func(w http.ResponseWriter, r *http.Request) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" {
				httpjson.Error(w, 401, "unauthorized", "Token required")
				return
			}
			if route.action == "disconnect" {
				if err := identities.Disconnect(r.Context(), token); err != nil {
					identityError(w, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			fn := identities.State
			if route.action == "connect" {
				fn = identities.Connect
			}
			state, err := fn(r.Context(), token)
			if err != nil {
				identityError(w, err)
				return
			}
			httpjson.Write(w, http.StatusOK, state)
		})
	}
	mux.HandleFunc("POST /api/v1/loki/invitations/{id}/response", func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(w, r)
		if !ok {
			return
		}
		var body struct {
			Decision string `json:"decision"`
		}
		if !httpjson.Decode(w, r, &body) {
			return
		}
		invitation, err := identities.Respond(r.Context(), token, r.PathValue("id"), body.Decision)
		if err != nil {
			identityError(w, err)
			return
		}
		httpjson.Write(w, http.StatusOK, invitation)
	})
	mux.HandleFunc("GET /api/v1/loki/events", func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(w, r)
		if !ok {
			return
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil {
				identityError(w, service.ErrInvalidRequest)
				return
			}
		}
		batch, err := identities.Events(r.Context(), token, limit)
		if err != nil {
			identityError(w, err)
			return
		}
		httpjson.Write(w, http.StatusOK, batch)
	})
	mux.HandleFunc("POST /api/v1/loki/events/{id}/ack", func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(w, r)
		if !ok {
			return
		}
		if err := identities.Ack(r.Context(), token, r.PathValue("id")); err != nil {
			identityError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	gameRoutes(mux, "/api/v1/loki/games", identities, true)
	return mux
}

func gameRoutes(mux *http.ServeMux, prefix string, identities *service.Identity, authenticate bool) {
	tokenFor := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		if authenticate {
			return bearer(w, r)
		}
		return "", true
	}
	mux.HandleFunc("GET "+prefix+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		token, ok := tokenFor(w, r)
		if !ok {
			return
		}
		result, err := identities.ReadGame(r.Context(), token, r.PathValue("id"))
		if err != nil {
			identityError(w, err)
			return
		}
		httpjson.Write(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST "+prefix+"/{id}/moves", func(w http.ResponseWriter, r *http.Request) {
		token, ok := tokenFor(w, r)
		if !ok {
			return
		}
		var request protocol.MoveRequest
		if !httpjson.Decode(w, r, &request) {
			return
		}
		result, err := identities.Play(r.Context(), token, r.PathValue("id"), request)
		if err != nil {
			identityError(w, err)
			return
		}
		httpjson.Write(w, http.StatusOK, result)
	})
}

func bearer(w http.ResponseWriter, r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		httpjson.Error(w, 401, "unauthorized", "Token required")
		return "", false
	}
	return token, true
}

func identityError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, game.ErrWrongTurn):
		httpjson.Error(w, 409, "wrong_turn", "It is the other player's turn")
	case errors.Is(err, game.ErrCellOccupied):
		httpjson.Error(w, 409, "cell_occupied", "Cell is occupied")
	case errors.Is(err, game.ErrFinished):
		httpjson.Error(w, 409, "game_finished", "Game is finished")
	case errors.Is(err, postgres.ErrStaleMove):
		httpjson.Error(w, 409, "stale_move", "Move number changed; synchronize game state")
	case errors.Is(err, service.ErrInvalidRequest):
		httpjson.Error(w, 400, "invalid_request", "Invalid identifier, side, decision or limit")
	case errors.Is(err, postgres.ErrNotFound):
		httpjson.Error(w, 404, "not_found", "Object not found")
	case errors.Is(err, postgres.ErrExpired):
		httpjson.Error(w, 410, "invitation_expired", "Invitation expired")
	case errors.Is(err, postgres.ErrConflict):
		httpjson.Error(w, 409, "conflict", "Request conflicts with an earlier decision")
	case errors.Is(err, postgres.ErrBusy):
		httpjson.Error(w, 409, "client_busy", "Client already has an invitation or game")
	case errors.Is(err, postgres.ErrOffline):
		httpjson.Error(w, 409, "client_offline", "Client is offline")
	case errors.Is(err, service.ErrInvalidName):
		httpjson.Error(w, 400, "invalid_name", "Display name is required")
	case errors.Is(err, postgres.ErrInvalidCursor):
		httpjson.Error(w, 400, "invalid_cursor", "Invalid pagination cursor")
	case errors.Is(err, postgres.ErrUnknownIdentity):
		httpjson.Error(w, 401, "unauthorized", "Invalid token")
	case errors.Is(err, postgres.ErrReconnect):
		httpjson.Error(w, 409, "reconnect_required", "Connect before sending commands")
	default:
		httpjson.Error(w, 503, "unavailable", "Server temporarily unavailable")
	}
}

func healthRoutes(mux *http.ServeMux, ready Readiness) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			respond(w, http.StatusServiceUnavailable, "not_ready")
			return
		}
		respond(w, http.StatusOK, "ready")
	})
}

func respond(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}
