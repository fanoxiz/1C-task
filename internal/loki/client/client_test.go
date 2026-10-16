package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"onec/internal/httpjson"
	"onec/internal/loki/profile"
	"onec/internal/protocol"
)

type fakeServer struct {
	token       string
	id          string
	registered  atomic.Int32
	connections atomic.Int32
	requests    atomic.Int32
	connected   atomic.Bool
	unavailable atomic.Bool
	redirect    atomic.Bool
	target      string
}

func newFake(t *testing.T, secret byte) (*fakeServer, *httptest.Server) {
	t.Helper()
	f := &fakeServer{token: base64.RawURLEncoding.EncodeToString(bytesOf(secret, 32)), id: "identity-" + string(secret)}
	s := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(s.Close)
	return f, s
}

func bytesOf(value byte, length int) []byte {
	b := make([]byte, length)
	for i := range b {
		b[i] = value
	}
	return b
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	if f.redirect.Load() {
		http.Redirect(w, r, f.target, http.StatusTemporaryRedirect)
		return
	}
	if f.unavailable.Load() {
		httpjson.Error(w, 503, "unavailable", "do not show this upstream secret")
		return
	}
	state := func() protocol.State {
		return protocol.State{Loki: protocol.Loki{ID: f.id, DisplayName: "Loki", Connected: f.connected.Load()}, ServerTime: time.Now()}
	}
	if r.URL.Path == "/api/v1/loki/register" {
		if r.Header.Get("Authorization") != "" {
			httpjson.Error(w, 400, "leaked_token", "Unexpected token during registration")
			return
		}
		f.registered.Add(1)
		f.connected.Store(true)
		httpjson.Write(w, 201, protocol.Registration{Token: f.token, State: state()})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		httpjson.Error(w, 401, "unauthorized", "Invalid token")
		return
	}
	switch r.URL.Path {
	case "/api/v1/loki/connect":
		f.connections.Add(1)
		f.connected.Store(true)
		httpjson.Write(w, 200, state())
	case "/api/v1/loki/state":
		if !f.connected.Load() {
			httpjson.Error(w, 409, "reconnect_required", "Connect again")
		} else {
			httpjson.Write(w, 200, state())
		}
	case "/api/v1/loki/disconnect":
		f.connected.Store(false)
		w.WriteHeader(204)
	case "/api/v1/loki/events":
		httpjson.Write(w, 200, protocol.EventBatch{Items: []protocol.Event{}})
	default:
		http.NotFound(w, r)
	}
}

func openStore(t *testing.T, dir string) *profile.Store {
	t.Helper()
	s, err := profile.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRestartReusesSavedIdentity(t *testing.T) {
	f, server := newFake(t, 'a')
	dir := t.TempDir()
	func() {
		store, err := profile.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		c := New(store)
		if err := c.Connect(context.Background(), server.URL+"/", "Loki"); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(c.State())
		if strings.Contains(string(body), f.token) || strings.Contains(string(body), "token") {
			t.Fatal("browser-visible state leaks a credential")
		}
		c.Shutdown(context.Background())
		if !store.Data().Reconnect {
			t.Fatal("normal process exit disabled restart recovery")
		}
	}()
	store := openStore(t, dir)
	c := New(store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(time.Second)
	for c.State().Status != "connected" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if state := c.State(); state.Identity == nil || state.Identity.ID != f.id || f.registered.Load() != 1 {
		t.Fatalf("identity was not restored: %+v, registrations=%d", state, f.registered.Load())
	}
}

func TestUnknownTokenNeverRegistersAgain(t *testing.T) {
	f, server := newFake(t, 'a')
	store := openStore(t, t.TempDir())
	data := store.Data()
	data.Profiles[server.URL] = profile.Profile{Token: "unknown-token"}
	if err := store.Save(data); err != nil {
		t.Fatal(err)
	}
	c := New(store)
	if err := c.Connect(context.Background(), server.URL, "Loki"); !errors.Is(err, ErrToken) {
		t.Fatal(err)
	}
	c.poll(context.Background())
	if f.registered.Load() != 0 || c.State().Status != "error" || store.Data().Profiles[server.URL].Token != "unknown-token" {
		t.Fatal("invalid credential was replaced or hidden")
	}
}

func TestRetryAndExplicitDisconnect(t *testing.T) {
	f, server := newFake(t, 'a')
	store := openStore(t, t.TempDir())
	c := New(store)
	ctx := context.Background()
	if err := c.Connect(ctx, server.URL, "Loki"); err != nil {
		t.Fatal(err)
	}
	f.unavailable.Store(true)
	c.poll(ctx)
	if c.State().Status != "error" || strings.Contains(c.State().Error, "secret") {
		t.Fatal("network error was not handled safely")
	}
	f.unavailable.Store(false)
	c.poll(ctx)
	if c.State().Status != "connected" || f.registered.Load() != 1 {
		t.Fatal("network recovery did not reuse identity")
	}
	f.connected.Store(false)
	c.poll(ctx)
	if c.State().Status != "connected" || !f.connected.Load() {
		t.Fatal("server restart did not trigger reconnect")
	}
	if err := c.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	requests := f.requests.Load()
	for range 5 {
		c.poll(ctx)
		_ = c.State()
	}
	if f.requests.Load() != requests || store.Data().Reconnect || c.State().Status != "disconnected" {
		t.Fatal("explicit disconnect did not stop requests")
	}
}

func TestCredentialsStayBoundToTheirServer(t *testing.T) {
	a, first := newFake(t, 'a')
	b, second := newFake(t, 'b')
	c := New(openStore(t, t.TempDir()))
	for _, address := range []string{first.URL, second.URL, first.URL} {
		if err := c.Connect(context.Background(), address, "Loki"); err != nil {
			t.Fatal(err)
		}
	}
	if a.registered.Load() != 1 || b.registered.Load() != 1 || c.State().Identity.ID != a.id {
		t.Fatal("changing servers lost or leaked an identity")
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	a.target = target.URL
	a.redirect.Store(true)
	c.poll(context.Background())
	if redirected.Load() != 0 || c.State().Status != "error" {
		t.Fatal("redirect followed with an authentication token")
	}
}

func TestFailedServerSwitchDoesNotResumePreviousServer(t *testing.T) {
	a, first := newFake(t, 'a')
	b, second := newFake(t, 'b')
	store := openStore(t, t.TempDir())
	c := New(store)
	if err := c.Connect(context.Background(), first.URL, "Loki"); err != nil {
		t.Fatal(err)
	}
	b.unavailable.Store(true)
	if err := c.Connect(context.Background(), second.URL, "Loki"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	data := store.Data()
	if data.LastServer != second.URL || data.Reconnect || c.enabled || a.connected.Load() || b.registered.Load() != 0 {
		t.Fatal("failed switch will resume the previous server or retry registration")
	}
	if data.Profiles[first.URL].Token != a.token {
		t.Fatal("switch lost the previous saved identity")
	}
}

func TestTruncatedPollResponsesRecoverWithoutRegisteringAgain(t *testing.T) {
	for _, path := range []string{"/api/v1/loki/state", "/api/v1/loki/events"} {
		t.Run(path, func(t *testing.T) {
			f, _ := newFake(t, 'a')
			var truncate atomic.Bool
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == path && truncate.CompareAndSwap(true, false) {
					w.Header().Set("Content-Length", "1000")
					_, _ = w.Write([]byte(`{"items":[`))
					return
				}
				f.handle(w, r)
			}))
			defer remote.Close()
			c := New(openStore(t, t.TempDir()))
			if err := c.Connect(context.Background(), remote.URL, "Loki"); err != nil {
				t.Fatal(err)
			}
			truncate.Store(true)
			c.poll(context.Background())
			if c.State().Status != "error" || !c.enabled {
				t.Fatal("incomplete transport response permanently disabled polling")
			}
			c.poll(context.Background())
			if c.State().Status != "connected" || f.registered.Load() != 1 || c.State().Identity.ID != f.id {
				t.Fatal("polling did not recover with the saved identity")
			}
		})
	}
}
