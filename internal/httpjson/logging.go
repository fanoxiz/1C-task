package httpjson

import (
	"crypto/rand"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"onec/internal/protocol"
)

// Log matched API routes, never credentials, request bodies or arbitrary URLs.
func LogRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		id := rand.Text()
		w.Header().Set("X-Request-ID", id)
		response := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(response, r)
		operation := r.Pattern
		if operation == "" {
			operation = "unmatched"
		}
		objectID := r.PathValue("id")
		if !protocol.IsUUID(objectID) {
			objectID = ""
		}
		logger.Info("http request", "request_id", id, "operation", operation, "object_id", objectID,
			"status", response.status, "duration_ms", time.Since(started).Milliseconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status, w.wrote = status, true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
