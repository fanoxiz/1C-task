package httpjson

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalRejectsHostAndOriginSpoofing(t *testing.T) {
	called := false
	handler := Local(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	for _, tc := range []struct {
		name, host, origin, site string
		want                     int
	}{
		{"local_cli", "127.0.0.1:8082", "", "", 200},
		{"same_origin", "localhost:8082", "http://localhost:8082", "same-origin", 200},
		{"ipv6", "[::1]:8082", "http://[::1]:8082", "", 200},
		{"dns_rebinding", "evil.example:8082", "", "", 403},
		{"foreign_origin", "127.0.0.1:8082", "https://evil.example", "", 403},
		{"different_port", "127.0.0.1:8082", "http://127.0.0.1:8080", "", 403},
		{"null_origin", "127.0.0.1:8082", "null", "", 403},
		{"cross_site", "127.0.0.1:8082", "", "cross-site", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/connection", nil)
			r.Host = tc.host
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want || called != (tc.want == 200) {
				t.Fatalf("status=%d, handler called=%v", w.Code, called)
			}
		})
	}
}

func TestDecodeRejectsUnboundedAndAmbiguousBodies(t *testing.T) {
	for _, tc := range []struct{ name, body, contentType string }{
		{"form", "name=Loki", "application/x-www-form-urlencoded"},
		{"extra_object", `{"name":"Loki"}{"name":"Other"}`, "application/json"},
		{"unknown_field", `{"name":"Loki","token":"secret"}`, "application/json"},
		{"large_body", `{"name":"` + strings.Repeat("a", 5000) + `"}`, "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			var target struct {
				Name string `json:"name"`
			}
			if Decode(httptest.NewRecorder(), r, &target) {
				t.Fatal("invalid request was accepted")
			}
		})
	}
}
