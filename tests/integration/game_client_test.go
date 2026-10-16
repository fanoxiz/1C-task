//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"onec/internal/loki/client"
	"onec/internal/loki/profile"
	"onec/internal/mobius/httpapi"
	"onec/internal/protocol"
)

func TestMoveDeliveryAndFinalAckAfterClientRestart(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "ack_request_lost"
		if committed {
			name = "ack_response_lost"
		}
		t.Run(name, func(t *testing.T) { checkMoveDelivery(t, committed) })
	}
}

func checkMoveDelivery(t *testing.T, committed bool) {
	f := invitationSetup(t)
	delegate := httpapi.Loki(func(context.Context) error { return nil }, f.service)
	var current atomic.Pointer[client.Client]
	var moveAcks, reconnects atomic.Int32
	var appliedBeforeAck, finalApplied, holdFinal atomic.Bool
	holdFinal.Store(true)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loki/connect" {
			reconnects.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/ack") {
			parts := strings.Split(r.URL.Path, "/")
			id := parts[len(parts)-2]
			var kind string
			var payload []byte
			if err := f.pool.QueryRow(f.ctx, "SELECT type,payload FROM game.events WHERE id=$1", id).Scan(&kind, &payload); err != nil {
				http.Error(w, "missing event", 404)
				return
			}
			if kind == "mobius_move" {
				var event protocol.MoveEvent
				if json.Unmarshal(payload, &event) != nil {
					http.Error(w, "invalid event", 500)
					return
				}
				state := current.Load().State()
				applied := state.Game != nil && state.Game.ID == event.Game.ID && len(state.Game.Moves) >= event.Move.Number && state.Game.Moves[event.Move.Number-1].ID == event.Move.ID
				if event.Move.Number == 5 {
					finalApplied.Store(applied)
					if holdFinal.Load() {
						http.Error(w, "delay final acknowledgement", 503)
						return
					}
				}
				if moveAcks.Add(1) == 1 {
					appliedBeforeAck.Store(applied)
					if committed {
						delegate.ServeHTTP(httptest.NewRecorder(), r)
					}
					http.Error(w, "lose acknowledgement", 503)
					return
				}
			}
		}
		delegate.ServeHTTP(w, r)
	}))
	defer remote.Close()
	dir := t.TempDir()
	store, err := profile.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	c := client.New(store)
	current.Store(c)
	if err := c.Connect(f.ctx, remote.URL, "Loki"); err != nil {
		t.Fatal(err)
	}
	player := protocol.Registration{Token: store.Data().Profiles[remote.URL].Token, State: protocol.State{Loki: *c.State().Identity}}
	i := decode[protocol.Invitation](t, f.invite(player.State.Loki.ID, f.id(), "X", 200))
	if err := c.Respond(f.ctx, i.ID, "accept"); err != nil {
		t.Fatal(err)
	}
	gameID := c.State().Game.ID
	runCtx, stop := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { c.Run(runCtx); close(done) }()
	defer func() { stop(); <-done }()
	f.move(player, gameID, "mobius", moveRequest(f.id(), 1, 0), 200)
	eventually(t, func() bool {
		return reconnects.Load() >= 1 && c.State().Status == "connected" && f.count("SELECT count(*) FROM game.moves WHERE game_id=$1 AND delivered_at IS NOT NULL", gameID) == 1
	})
	if !appliedBeforeAck.Load() {
		t.Fatal("client acknowledged before processing move")
	}
	if !committed && moveAcks.Load() < 2 {
		t.Fatal("lost ACK was not retried")
	}
	if err := c.Play(f.ctx, gameID, moveRequest(f.id(), 2, 3)); err != nil {
		t.Fatal(err)
	}
	f.move(player, gameID, "mobius", moveRequest(f.id(), 3, 1), 200)
	eventually(t, func() bool { return c.State().Game != nil && len(c.State().Game.Moves) == 3 })
	if err := c.Play(f.ctx, gameID, moveRequest(f.id(), 4, 4)); err != nil {
		t.Fatal(err)
	}
	f.move(player, gameID, "mobius", moveRequest(f.id(), 5, 2), 200)
	eventually(t, func() bool {
		return finalApplied.Load() && c.State().Game != nil && c.State().Game.Status == "completed"
	})
	if f.count("SELECT count(*) FROM game.games WHERE id=$1 AND status='completed'", gameID) != 1 || f.count("SELECT count(*) FROM game.moves WHERE game_id=$1 AND number=5 AND delivered_at IS NOT NULL", gameID) != 0 {
		t.Fatal("result incorrectly depends on final ACK")
	}
	stop()
	<-done
	if err := f.service.ResetPresence(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = profile.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c = client.New(store)
	current.Store(c)
	holdFinal.Store(false)
	runCtx, stop = context.WithCancel(f.ctx)
	done = make(chan struct{})
	go func() { c.Run(runCtx); close(done) }()
	eventually(t, func() bool {
		return f.count("SELECT count(*) FROM game.moves WHERE game_id=$1 AND number=5 AND delivered_at IS NOT NULL", gameID) == 1
	})
	state := c.State()
	if state.Game == nil || state.Game.ID != gameID || state.Game.Result != "mobius_win" || len(state.Game.Moves) != 5 || !finalApplied.Load() {
		t.Fatal("client restart lost final move or result")
	}
	if f.count("SELECT count(*) FROM game.loki") != 1 || f.count("SELECT count(*) FROM game.games WHERE status='completed'") != 1 {
		t.Fatal("delivery retry duplicated identity or result")
	}
}

func TestOldFinalMoveDoesNotReplaceNewActiveGame(t *testing.T) {
	f := invitationSetup(t)
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := client.New(store)
	if err := c.Connect(f.ctx, f.public.URL, "Loki"); err != nil {
		t.Fatal(err)
	}
	player := protocol.Registration{Token: store.Data().Profiles[f.public.URL].Token, State: protocol.State{Loki: *c.State().Identity}}
	old := f.start(player, "X")
	for n, cell := range []int{0, 3, 1, 4, 2} {
		actor := "mobius"
		if n%2 == 1 {
			actor = "loki"
		}
		f.move(player, old.ID, actor, moveRequest(f.id(), n+1, cell), 200)
	}
	fresh := f.start(player, "O")
	runCtx, stop := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { c.Run(runCtx); close(done) }()
	defer func() { stop(); <-done }()
	eventually(t, func() bool { return f.count("SELECT count(*) FROM game.events WHERE acked_at IS NULL") == 0 })
	state := c.State()
	if state.Game == nil || state.Game.ID != fresh.ID || state.Game.Status != "active" || len(state.Game.Moves) != 0 || state.Game.NextActor != "loki" {
		t.Fatal("old final events overwrote current party")
	}
	if f.count("SELECT count(*) FROM game.moves WHERE game_id=$1 AND actor='mobius' AND delivered_at IS NOT NULL", old.ID) != 3 {
		t.Fatal("old final moves were not acknowledged")
	}
}
