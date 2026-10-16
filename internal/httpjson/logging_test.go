package httpjson

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLogsCorrelateErrorsWithoutExposingCredentials(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/games/{id}/moves", func(w http.ResponseWriter, r *http.Request) {
		Error(w, 503, "unavailable", "secret response body")
	})
	handler := Timeout(LogRequests(logger, Local(mux)))
	const gameID = "4a818d54-470f-4e87-868a-63876f99b6ea"
	r := httptest.NewRequest("POST", "http://localhost/api/v1/games/"+gameID+"/moves?token=query-secret", strings.NewReader(`{"token":"body-secret"}`))
	r.Header.Set("Authorization", "Bearer header-secret")
	r.Header.Set("X-Request-ID", "caller-secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var record struct {
		RequestID string `json:"request_id"`
		Operation string `json:"operation"`
		ObjectID  string `json:"object_id"`
		Status    int    `json:"status"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.RequestID == "" || record.RequestID != w.Header().Get("X-Request-ID") || record.Status != 503 || record.Operation != "POST /api/v1/games/{id}/moves" || record.ObjectID != gameID {
		t.Fatalf("invalid request log: %+v", record)
	}
	if strings.Contains(output.String(), "secret") {
		t.Fatal("request log exposed credentials or payload")
	}
	output.Reset()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://localhost/api/path-secret", nil))
	if strings.Contains(output.String(), "path-secret") || !strings.Contains(output.String(), "unmatched") {
		t.Fatal("unmatched path leaked into logs")
	}
	output.Reset()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "http://localhost/api/v1/games/path-secret/moves", nil))
	if strings.Contains(output.String(), "secret") {
		t.Fatal("invalid identifier leaked into logs")
	}
	output.Reset()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://localhost/readyz", nil))
	if output.Len() != 0 {
		t.Fatal("health check generated an API log")
	}
}
