package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"onec/internal/httpjson"
	"onec/internal/mobius/postgres"
	"onec/internal/mobius/service"
	"onec/web"
)

type Readiness func(context.Context) error

func Mobius(ready Readiness, identities *service.Identity) http.Handler {
	mux := http.NewServeMux()
	healthRoutes(mux, ready)
	mux.Handle("GET /{$}", web.Handler("mobius"))
	mux.Handle("GET /app.js", web.Handler("mobius"))
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
	return httpjson.Local(httpjson.Timeout(mux))
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
	return httpjson.Timeout(mux)
}

func identityError(w http.ResponseWriter, err error) {
	switch {
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
