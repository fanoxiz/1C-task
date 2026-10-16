//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"onec/internal/loki/client"
	"onec/internal/loki/profile"
	"onec/internal/mobius/httpapi"
	"onec/internal/protocol"
)

func TestSecondClientPlaysWhileFirstClientAckIsHeld(t *testing.T) {
	f := invitationSetup(t)
	var heldID atomic.Value
	heldID.Store("")
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	delegate := httpapi.Loki(func(context.Context) error { return nil }, f.service)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loki/events/"+heldID.Load().(string)+"/ack" {
			enteredOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		delegate.ServeHTTP(w, r)
	}))
	defer remote.Close()
	connect := func() (*client.Client, protocol.Registration) {
		t.Helper()
		store, err := profile.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		c := client.New(store)
		if err := c.Connect(f.ctx, remote.URL, "Loki"); err != nil {
			t.Fatal(err)
		}
		return c, protocol.Registration{Token: store.Data().Profiles[remote.URL].Token, State: protocol.State{Loki: *c.State().Identity}}
	}
	aClient, a := connect()
	bClient, b := connect()
	ga, gb := f.start(a, "X"), f.start(b, "X")
	f.move(a, ga.ID, "mobius", moveRequest(f.id(), 1, 8), 200)
	for _, event := range f.events(a.Token).Items {
		if event.Type == "mobius_move" {
			heldID.Store(event.ID)
		}
	}
	if heldID.Load().(string) == "" {
		t.Fatal("missing move event")
	}
	runCtx, stop := context.WithCancel(f.ctx)
	var workers sync.WaitGroup
	for _, c := range []*client.Client{aClient, bClient} {
		workers.Go(func() { c.Run(runCtx) })
	}
	defer func() { unblock(); stop(); workers.Wait() }()
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal("client did not reach the delayed ACK")
	}
	for n, cell := range []int{0, 3, 1, 4, 2} {
		request := moveRequest(f.id(), n+1, cell)
		if n%2 == 0 {
			f.move(b, gb.ID, "mobius", request, 200)
		} else if err := bClient.Play(f.ctx, gb.ID, request); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, func() bool {
		view := bClient.State()
		return view.Game != nil && view.Game.ID == gb.ID && view.Game.Result == "mobius_win" &&
			f.count("SELECT count(*) FROM game.events WHERE loki_id=$1 AND acked_at IS NULL", b.State.Loki.ID) == 0
	})
	if f.count("SELECT count(*) FROM game.events WHERE id=$1 AND acked_at IS NULL", heldID.Load().(string)) != 1 {
		t.Fatal("B consumed the held ACK of A")
	}
	if view := aClient.State(); view.Game == nil || view.Game.ID != ga.ID || view.Game.Board[2][2] != "X" || view.Game.Status != "active" {
		t.Fatal("clients mixed their game state")
	}
	unblock()
	eventually(t, func() bool {
		return f.count("SELECT count(*) FROM game.moves WHERE game_id=$1 AND delivered_at IS NOT NULL", ga.ID) == 1
	})
}
