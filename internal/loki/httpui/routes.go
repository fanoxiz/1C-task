package httpui

import (
	"net/http"

	"onec/web"
)

func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", web.Handler("loki"))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	return mux
}
