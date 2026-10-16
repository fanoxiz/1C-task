package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"onec/web"
)

type Readiness func(context.Context) error

func Mobius(ready Readiness) http.Handler {
	mux := http.NewServeMux()
	healthRoutes(mux, ready)
	mux.Handle("GET /{$}", web.Handler("mobius"))
	mux.Handle("GET /app.js", web.Handler("mobius"))
	return mux
}

func Loki(ready Readiness) http.Handler {
	mux := http.NewServeMux()
	healthRoutes(mux, ready)
	return mux
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
