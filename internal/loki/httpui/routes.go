package httpui

import (
	"net/http"

	"onec/internal/httpjson"
	"onec/internal/loki/client"
	"onec/web"
)

func Handler(connection *client.Client) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", web.Handler("loki"))
	mux.Handle("GET /app.js", web.Handler("loki"))
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
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	return httpjson.Local(httpjson.Timeout(mux))
}
