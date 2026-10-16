package httpjson

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"onec/internal/protocol"
)

func Write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func Error(w http.ResponseWriter, status int, code, message string) {
	var body protocol.ErrorResponse
	body.Error.Code, body.Error.Message = code, message
	Write(w, status, body)
}

func Decode(w http.ResponseWriter, r *http.Request, value any) bool {
	typeName, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typeName != "application/json" {
		Error(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Expected application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		Error(w, http.StatusBadRequest, "invalid_json", "Invalid JSON request")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		Error(w, http.StatusBadRequest, "invalid_json", "Expected one JSON object")
		return false
	}
	return true
}

func Timeout(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 7*time.Second)
		defer cancel()
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Local rejects DNS rebinding and cross-origin requests without requiring UI credentials.
// JSON-only mutation handlers also reject cross-origin simple form submissions.
func Local(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if strings.Contains(host, ":") {
			var err error
			host, _, err = net.SplitHostPort(host)
			if err != nil {
				Error(w, http.StatusForbidden, "invalid_host", "Local host required")
				return
			}
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			Error(w, http.StatusForbidden, "invalid_host", "Local host required")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Scheme != "http" || parsed.Host != r.Host || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				Error(w, http.StatusForbidden, "invalid_origin", "Same origin required")
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			Error(w, http.StatusForbidden, "invalid_origin", "Same origin required")
			return
		}
		handler.ServeHTTP(w, r)
	})
}
