//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"onec/internal/loki/client"
	"onec/internal/loki/httpui"
	"onec/internal/loki/profile"
	"onec/internal/mobius/httpapi"
	"onec/internal/mobius/postgres"
	"onec/internal/mobius/service"
	"onec/internal/protocol"
	"onec/migrations"
)

func jsonRequest(t *testing.T, method, url, token string, body any, want int) []byte {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := http.NewRequest(method, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 4 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != want {
		t.Fatalf("%s %s: got %d %s (%v), want %d", method, url, response.StatusCode, result, err, want)
	}
	return result
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestIdentityLifecycle(t *testing.T) {
	ctx, pool := testDatabase(t)
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := postgres.NewIdentityStore(pool)
	identities := service.NewIdentity(store)
	ready := func(ctx context.Context) error { return migrations.Check(ctx, pool) }
	public := httptest.NewServer(httpapi.Loki(ready, identities))
	defer public.Close()
	admin := httptest.NewServer(httpapi.Mobius(ready, identities))
	defer admin.Close()
	register := func() protocol.Registration {
		return decode[protocol.Registration](t, jsonRequest(t, "POST", public.URL+"/api/v1/loki/register", "", map[string]string{"display_name": " Same Loki "}, 201))
	}
	first, second := register(), register()
	if first.Token == second.Token || first.State.Loki.ID == second.State.Loki.ID || first.State.Loki.DisplayName != "Same Loki" {
		t.Fatal("equal display names did not produce distinct identities")
	}
	var savedHash []byte
	if err := pool.QueryRow(ctx, "SELECT token_hash FROM game.loki WHERE id = $1", first.State.Loki.ID).Scan(&savedHash); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte(first.Token))
	if !bytes.Equal(savedHash, expected[:]) {
		t.Fatal("database did not store the token hash")
	}
	stateBody := jsonRequest(t, "GET", public.URL+"/api/v1/loki/state", first.Token, nil, 200)
	state := decode[protocol.State](t, stateBody)
	if state.Loki.ID != first.State.Loki.ID || strings.Contains(string(stateBody), second.State.Loki.ID) || strings.Contains(string(stateBody), "token") {
		t.Fatal("own-state endpoint exposed another identity or a credential")
	}
	badToken := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	jsonRequest(t, "GET", public.URL+"/api/v1/loki/state", badToken, nil, 401)
	jsonRequest(t, "GET", public.URL+"/api/v1/loki/state", "", nil, 401)
	jsonRequest(t, "GET", public.URL+"/api/v1/mobius/loki", first.Token, nil, 404)
	jsonRequest(t, "POST", public.URL+"/api/v1/loki/register", "", map[string]string{"display_name": " "}, 400)
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM game.loki").Scan(&count); err != nil || count != 2 {
		t.Fatal("invalid request created an identity")
	}
	if _, err := pool.Exec(ctx, "UPDATE game.loki SET last_seen_at = clock_timestamp() - interval '31 seconds' WHERE id = $1", first.State.Loki.ID); err != nil {
		t.Fatal(err)
	}
	failure := decode[protocol.ErrorResponse](t, jsonRequest(t, "GET", public.URL+"/api/v1/loki/state", first.Token, nil, 409))
	if failure.Error.Code != "reconnect_required" {
		t.Fatal("late poll did not request reconnect")
	}
	var connected bool
	if err := pool.QueryRow(ctx, "SELECT connected FROM game.loki WHERE id = $1", first.State.Loki.ID).Scan(&connected); err != nil || connected {
		t.Fatal("late poll rolled back offline state")
	}
	reconnected := decode[protocol.State](t, jsonRequest(t, "POST", public.URL+"/api/v1/loki/connect", first.Token, nil, 200))
	if reconnected.Loki.ID != first.State.Loki.ID || !reconnected.Loki.Connected {
		t.Fatal("reconnect changed identity")
	}
	jsonRequest(t, "POST", public.URL+"/api/v1/loki/disconnect", first.Token, nil, 204)
	jsonRequest(t, "POST", public.URL+"/api/v1/loki/disconnect", first.Token, nil, 204)
	jsonRequest(t, "GET", public.URL+"/api/v1/loki/state", first.Token, nil, 409)
	jsonRequest(t, "POST", public.URL+"/api/v1/loki/connect", first.Token, nil, 200)

	if err := identities.ResetPresence(ctx); err != nil {
		t.Fatal(err)
	}
	jsonRequest(t, "GET", public.URL+"/api/v1/loki/state", second.Token, nil, 409)
	jsonRequest(t, "POST", public.URL+"/api/v1/loki/connect", first.Token, nil, 200)
	online := decode[protocol.LokiList](t, jsonRequest(t, "GET", admin.URL+"/api/v1/mobius/loki?connected=true", "", nil, 200))
	if len(online.Items) != 1 || online.Items[0].ID != first.State.Loki.ID {
		t.Fatal("online filter is incorrect")
	}
	jsonRequest(t, "GET", admin.URL+"/api/v1/mobius/loki?cursor=invalid", "", nil, 400)
	jsonRequest(t, "GET", admin.URL+"/api/v1/mobius/loki?connected=invalid", "", nil, 400)
	if _, err := pool.Exec(ctx, `INSERT INTO game.loki (display_name, token_hash)
		SELECT 'Page Loki', sha256(convert_to('page-' || n::text, 'UTF8')) FROM generate_series(1, 55) n`); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		body := jsonRequest(t, "GET", admin.URL+"/api/v1/mobius/loki?cursor="+cursor, "", nil, 200)
		if strings.Contains(string(body), first.Token) || strings.Contains(string(body), "token_hash") {
			t.Fatal("admin list exposes credentials")
		}
		page := decode[protocol.LokiList](t, body)
		if len(page.Items) > 50 {
			t.Fatal("page exceeds limit")
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("pagination repeated an identity")
			}
			seen[item.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 57 {
		t.Fatalf("pagination returned %d identities, want 57", len(seen))
	}

	if _, err := pool.Exec(ctx, "UPDATE game.loki SET last_seen_at = clock_timestamp() - interval '31 seconds' WHERE id = $1", first.State.Loki.ID); err != nil {
		t.Fatal(err)
	}
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		identities.RunPresence(workerCtx, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	defer func() { stop(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := pool.QueryRow(ctx, "SELECT connected FROM game.loki WHERE id = $1", first.State.Loki.ID).Scan(&connected); err != nil {
			t.Fatal(err)
		}
		if !connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("presence worker did not expire the client")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLocalClientKeepsTokenOutOfBrowser(t *testing.T) {
	ctx, pool := testDatabase(t)
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	identities := service.NewIdentity(postgres.NewIdentityStore(pool))
	remote := httptest.NewServer(httpapi.Loki(func(context.Context) error { return nil }, identities))
	defer remote.Close()
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connection := client.New(store)
	local := httptest.NewServer(httpui.Handler(connection))
	defer local.Close()
	body := jsonRequest(t, "POST", local.URL+"/api/v1/connection", "", map[string]string{"server_address": remote.URL, "display_name": "Loki"}, 200)
	saved := store.Data().Profiles[remote.URL]
	if saved.Token == "" || strings.Contains(string(body), saved.Token) || strings.Contains(string(body), "token") {
		t.Fatal("token was not saved privately")
	}
	id := connection.State().Identity.ID
	var before, after time.Time
	if err := pool.QueryRow(ctx, "SELECT last_seen_at FROM game.loki WHERE id = $1", id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		body = jsonRequest(t, "GET", local.URL+"/api/v1/state", "", nil, 200)
		if strings.Contains(string(body), saved.Token) {
			t.Fatal("polling browser received token")
		}
	}
	if err := pool.QueryRow(ctx, "SELECT last_seen_at FROM game.loki WHERE id = $1", id).Scan(&after); err != nil || !after.Equal(before) {
		t.Fatal("browser state reads issued remote heartbeat requests")
	}
	jsonRequest(t, "DELETE", local.URL+"/api/v1/connection", "", nil, 200)
	if store.Data().Reconnect {
		t.Fatal("explicit disconnect will reconnect after restart")
	}
	jsonRequest(t, "POST", local.URL+"/api/v1/connection", "", map[string]string{"server_address": remote.URL}, 200)
	if connection.State().Identity.ID != id {
		t.Fatal("local reconnection changed identity")
	}
}
