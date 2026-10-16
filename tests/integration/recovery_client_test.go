//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"onec/internal/loki/client"
	"onec/internal/loki/httpui"
	"onec/internal/loki/profile"
	"onec/internal/mobius/httpapi"
	"onec/internal/protocol"
)

func recoveryClient(t *testing.T, f *invitationFixture, address string) (*client.Client, protocol.Registration) {
	t.Helper()
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	c := client.New(store)
	if err := c.Connect(f.ctx, address, "Loki"); err != nil {
		t.Fatal(err)
	}
	return c, protocol.Registration{Token: store.Data().Profiles[address].Token, State: protocol.State{Loki: *c.State().Identity}}
}

func runRecoveryClient(t *testing.T, f *invitationFixture, c *client.Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestLostFinalMoveResponseRetriesSameRequestThroughLocalAPI(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "before_commit"
		if committed {
			name = "after_commit"
		}
		t.Run(name, func(t *testing.T) {
			f := invitationSetup(t)
			var lose atomic.Bool
			delegate := httpapi.Loki(func(context.Context) error { return nil }, f.service)
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/moves") && lose.CompareAndSwap(true, false) {
					if committed {
						delegate.ServeHTTP(httptest.NewRecorder(), r)
					}
					w.Header().Set("Content-Length", "1000")
					_, _ = w.Write([]byte(`{"id":"`))
					return
				}
				delegate.ServeHTTP(w, r)
			}))
			defer remote.Close()
			c, player := recoveryClient(t, f, remote.URL)
			board := f.start(player, "O")
			for n, cell := range []int{0, 3, 1, 4} {
				actor := "loki"
				if n%2 == 1 {
					actor = "mobius"
				}
				f.move(player, board.ID, actor, moveRequest(f.id(), n+1, cell), 200)
			}
			local := httptest.NewServer(httpui.Handler(c))
			defer local.Close()
			request := moveRequest(f.id(), 5, 2)
			lose.Store(true)
			url := local.URL + "/api/v1/games/" + board.ID + "/moves"
			jsonRequest(t, "POST", url, "", request, 502)
			want := 4
			if committed {
				want = 5
			}
			if c.State().Game == nil || len(c.State().Game.Moves) != want {
				t.Fatal("uncertain command did not synchronize the authoritative board")
			}
			for range 2 {
				view := decode[client.View](t, jsonRequest(t, "POST", url, "", request, 200))
				if view.Game == nil || view.Game.Result != "loki_win" || len(view.Game.Moves) != 5 || view.Statistics.Played != 1 || view.Statistics.Wins != 1 {
					t.Fatal("retry lost or duplicated the final move/result")
				}
			}
			if f.count("SELECT count(*) FROM game.events WHERE game_id=$1 AND type='game_finished'", board.ID) != 1 {
				t.Fatal("final result emitted more than once")
			}
		})
	}
}

func TestLostEventResponseIsReplayedAndAcknowledged(t *testing.T) {
	f := invitationSetup(t)
	var lose atomic.Bool
	var attempts atomic.Int32
	delegate := httpapi.Loki(func(context.Context) error { return nil }, f.service)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loki/events" {
			attempts.Add(1)
			if lose.CompareAndSwap(true, false) {
				delegate.ServeHTTP(httptest.NewRecorder(), r)
				w.Header().Set("Content-Length", "1000")
				_, _ = w.Write([]byte(`{"items":[`))
				return
			}
		}
		delegate.ServeHTTP(w, r)
	}))
	defer remote.Close()
	c, player := recoveryClient(t, f, remote.URL)
	g := f.start(player, "X")
	f.move(player, g.ID, "mobius", moveRequest(f.id(), 1, 0), 200)
	lose.Store(true)
	runRecoveryClient(t, f, c)
	eventually(t, func() bool { return !lose.Load() && c.State().Status == "error" })
	if f.count("SELECT count(*) FROM game.events WHERE game_id=$1 AND type='mobius_move' AND acked_at IS NULL", g.ID) != 1 {
		t.Fatal("lost GET acknowledged an event without delivery")
	}
	eventually(t, func() bool {
		return c.State().Status == "connected" && f.count("SELECT count(*) FROM game.moves WHERE game_id=$1 AND delivered_at IS NOT NULL", g.ID) == 1
	})
	if attempts.Load() < 2 || c.State().Game == nil || len(c.State().Game.Moves) != 1 || f.count("SELECT count(*) FROM game.loki") != 1 {
		t.Fatal("event replay duplicated state or identity")
	}
}

func TestDeletedEventDuringAckDoesNotResurrectGameOrLoop(t *testing.T) {
	f := invitationSetup(t)
	var target atomic.Value
	target.Store("")
	var deleted atomic.Bool
	var player protocol.Registration
	delegate := httpapi.Loki(func(context.Context) error { return nil }, f.service)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loki/events/"+target.Load().(string)+"/ack" && deleted.CompareAndSwap(false, true) {
			if err := f.service.Disconnect(r.Context(), player.Token); err != nil {
				http.Error(w, "disconnect failed", 503)
				return
			}
		}
		delegate.ServeHTTP(w, r)
	}))
	defer remote.Close()
	c, registration := recoveryClient(t, f, remote.URL)
	player = registration
	g := f.start(player, "X")
	f.move(player, g.ID, "mobius", moveRequest(f.id(), 1, 0), 200)
	for _, event := range f.events(player.Token).Items {
		if event.Type == "mobius_move" {
			target.Store(event.ID)
		}
	}
	runRecoveryClient(t, f, c)
	eventually(t, func() bool {
		return deleted.Load() && c.State().Status == "connected" && c.State().Game == nil &&
			f.count("SELECT count(*) FROM game.events WHERE loki_id=$1 AND acked_at IS NULL", player.State.Loki.ID) == 0
	})
	if f.count("SELECT count(*) FROM game.games") != 0 || f.count("SELECT count(*) FROM game.moves") != 0 || c.State().Statistics.Played != 0 || !strings.Contains(c.State().Notice, "удалена") {
		t.Fatal("deleted event revived the game or lost the cancellation")
	}
}

func TestClientRecoversAcrossShortAndExpiredPresenceOutages(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "short"
		if expired {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			f := invitationSetup(t)
			var offline atomic.Bool
			delegate := httpapi.Loki(func(context.Context) error { return nil }, f.service)
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if offline.Load() {
					http.Error(w, "offline", 503)
					return
				}
				delegate.ServeHTTP(w, r)
			}))
			defer remote.Close()
			c, player := recoveryClient(t, f, remote.URL)
			g := f.start(player, "X")
			f.move(player, g.ID, "mobius", moveRequest(f.id(), 1, 0), 200)
			offline.Store(true)
			runRecoveryClient(t, f, c)
			eventually(t, func() bool { return c.State().Status == "error" })
			age := "5 seconds"
			if expired {
				age = "31 seconds"
			}
			f.exec("UPDATE game.loki SET last_seen_at=clock_timestamp()-$2::interval WHERE id=$1", player.State.Loki.ID, age)
			if err := f.store.Expire(f.ctx); err != nil {
				t.Fatal(err)
			}
			offline.Store(false)
			eventually(t, func() bool {
				return c.State().Status == "connected" && f.count("SELECT count(*) FROM game.events WHERE acked_at IS NULL") == 0
			})
			view := c.State()
			if view.Identity.ID != player.State.Loki.ID || view.Statistics.Played != 0 || f.count("SELECT count(*) FROM game.loki") != 1 {
				t.Fatal("network recovery changed identity or statistics")
			}
			if expired {
				if view.Game != nil || !strings.Contains(view.Notice, "удалена") {
					t.Fatal("expired game resurrected on reconnect")
				}
			} else {
				// State was fetched before ACK; query the authoritative game for its delivery mark.
				stored := f.state(player.Token).Game
				if view.Game == nil || view.Game.ID != g.ID || len(view.Game.Moves) != 1 || stored == nil || stored.Moves[0].DeliveredAt == nil {
					t.Fatal("short outage lost the game or pending delivery")
				}
			}
		})
	}
}
