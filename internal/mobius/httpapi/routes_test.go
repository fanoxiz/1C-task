package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListenerIsolation(t *testing.T) {
	ready := func(context.Context) error { return nil }
	for name, handler := range map[string]http.Handler{"mobius": Mobius(ready), "loki": Loki(ready)} {
		t.Run(name, func(t *testing.T) {
			for _, path := range []string{"/", "/app.js", "/api/v1/mobius/loki"} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				want := http.StatusNotFound
				if name == "mobius" && path != "/api/v1/mobius/loki" {
					want = http.StatusOK
				}
				if response.Code != want {
					t.Errorf("%s: got %d, want %d", path, response.Code, want)
				}
			}
		})
	}
}

func TestReadinessTracksDependencyWithoutLeakingErrors(t *testing.T) {
	for _, public := range []bool{false, true} {
		var dependencyErr error
		ready := func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("readiness has no timeout")
			}
			return dependencyErr
		}
		handler := Mobius(ready)
		if public {
			handler = Loki(ready)
		}
		for _, broken := range []bool{false, true, false} {
			dependencyErr = nil
			want := http.StatusOK
			if broken {
				dependencyErr = errors.New("secret database connection details")
				want = http.StatusServiceUnavailable
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			body, _ := io.ReadAll(response.Result().Body)
			if response.Code != want || strings.Contains(string(body), "secret") {
				t.Fatalf("unexpected readiness: %d %s", response.Code, body)
			}
			live := httptest.NewRecorder()
			handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if live.Code != http.StatusOK {
				t.Fatal("dependency failure broke liveness")
			}
		}
	}
}
