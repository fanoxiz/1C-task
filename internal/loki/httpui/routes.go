package httpui

import (
	"errors"
	"net/http"

	"onec/internal/httpjson"
	"onec/internal/loki/client"
	"onec/internal/protocol"
	"onec/web"
)

func Handler(connection *client.Client) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", web.Handler("loki"))
	mux.Handle("GET /app.js", web.Handler("loki"))
	mux.Handle("GET /game.js", web.Handler("common"))
	mux.HandleFunc("GET /api/v1/state", func(w http.ResponseWriter, r *http.Request) {
		httpjson.Write(w, http.StatusOK, connection.State())
	})
	mux.HandleFunc("POST /api/v1/connection", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ServerAddress string `json:"server_address"`
			DisplayName   string `json:"display_name"`
		}
		if !httpjson.Decode(w, r, &body) {
			return
		}
		if err := connection.Connect(r.Context(), body.ServerAddress, body.DisplayName); err != nil {
			status := http.StatusBadGateway
			if client.IsInputError(err) {
				status = http.StatusBadRequest
			}
			httpjson.Error(w, status, "connection_failed", err.Error())
			return
		}
		httpjson.Write(w, http.StatusOK, connection.State())
	})
	mux.HandleFunc("DELETE /api/v1/connection", func(w http.ResponseWriter, r *http.Request) {
		if err := connection.Disconnect(r.Context()); err != nil {
			httpjson.Error(w, http.StatusInternalServerError, "profile_error", err.Error())
			return
		}
		httpjson.Write(w, http.StatusOK, connection.State())
	})
	mux.HandleFunc("POST /api/v1/invitations/{id}/response", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Decision string `json:"decision"`
		}
		if !httpjson.Decode(w, r, &body) {
			return
		}
		if err := connection.Respond(r.Context(), r.PathValue("id"), body.Decision); err != nil {
			status := http.StatusConflict
			if client.IsInputError(err) {
				status = http.StatusBadRequest
			}
			httpjson.Error(w, status, "response_failed", err.Error())
			return
		}
		httpjson.Write(w, http.StatusOK, connection.State())
	})
	mux.HandleFunc("POST /api/v1/games/{id}/moves", func(w http.ResponseWriter, r *http.Request) {
		var request protocol.MoveRequest
		if !httpjson.Decode(w, r, &request) {
			return
		}
		if err := connection.Play(r.Context(), r.PathValue("id"), request); err != nil {
			status := http.StatusConflict
			if client.IsInputError(err) {
				status = http.StatusBadRequest
			}
			if errors.Is(err, client.ErrUnavailable) || errors.Is(err, client.ErrResponse) || errors.Is(err, client.ErrToken) {
				status = http.StatusBadGateway
			}
			httpjson.Error(w, status, "move_failed", err.Error())
			return
		}
		httpjson.Write(w, http.StatusOK, connection.State())
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	return httpjson.Local(httpjson.Timeout(mux))
}
