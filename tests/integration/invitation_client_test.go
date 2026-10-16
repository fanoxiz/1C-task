//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"onec/internal/loki/client"
	"onec/internal/loki/profile"
	"onec/internal/mobius/httpapi"
	"onec/internal/protocol"
)

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestClientInvitationReplayAndLostAck(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "request_lost_before_commit"
		if committed {
			name = "response_lost_after_commit"
		}
		t.Run(name, func(t *testing.T) { checkClientInvitationReplay(t, committed) })
	}
}

func checkClientInvitationReplay(t *testing.T, committed bool) {
	f := invitationSetup(t)
	delegate := httpapi.Loki(func(context.Context) error { return nil }, f.service)
	var connection atomic.Pointer[client.Client]
	var ackAttempts atomic.Int32
	var reconnects atomic.Int32
	var appliedBeforeAck atomic.Bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/loki/connect" {
			reconnects.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/ack") {
			state := connection.Load().State()
			if state.Invitation != nil {
				appliedBeforeAck.Store(true)
			}
			if ackAttempts.Add(1) == 1 {
				if committed {
					delegate.ServeHTTP(httptest.NewRecorder(), r)
				}
				http.Error(w, "lost acknowledgement", 503)
				return
			}
		}
		delegate.ServeHTTP(w, r)
	}))
	defer remote.Close()
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := client.New(store)
	connection.Store(c)
	if err := c.Connect(f.ctx, remote.URL, "Loki"); err != nil {
		t.Fatal(err)
	}
	owner := c.State().Identity.ID
	i := decode[protocol.Invitation](t, f.invite(owner, f.id(), "O", 200))
	runCtx, stop := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { c.Run(runCtx); close(done) }()
	defer func() { stop(); <-done }()
	eventually(t, func() bool {
		return reconnects.Load() >= 1 && c.State().Status == "connected" && f.count("SELECT count(*) FROM game.events WHERE acked_at IS NULL") == 0
	})
	if !committed && ackAttempts.Load() < 2 {
		t.Fatal("uncommitted ACK was not retried")
	}
	if !appliedBeforeAck.Load() || c.State().Invitation == nil || c.State().Invitation.ID != i.ID {
		t.Fatal("invitation was not applied before ACK or was lost on retry")
	}
	if f.count("SELECT count(*) FROM game.loki") != 1 {
		t.Fatal("ACK retry registered a new identity")
	}
	if err := c.Respond(f.ctx, i.ID, "accept"); err != nil {
		t.Fatal(err)
	}
	if game := c.State().Game; game == nil || game.NextActor != "loki" {
		t.Fatal("accept did not refresh game snapshot")
	}
	eventually(t, func() bool { return f.count("SELECT count(*) FROM game.events WHERE acked_at IS NULL") == 0 })
	if err := c.Disconnect(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(f.ctx, remote.URL, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return strings.Contains(c.State().Notice, "удалена") })
	if c.State().Game != nil || c.State().Invitation != nil {
		t.Fatal("old event resurrected deleted activity")
	}
}

func TestClientDrainsEventPagesWithoutShowingOldInvitations(t *testing.T) {
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
	owner := c.State().Identity.ID
	token := store.Data().Profiles[f.public.URL].Token
	for range 50 {
		invitation, err := f.service.Invite(f.ctx, owner, f.id(), "X")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.Respond(f.ctx, token, invitation.ID, "decline"); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := f.service.Invite(f.ctx, owner, f.id(), "O")
	if err != nil {
		t.Fatal(err)
	}
	if count := f.count("SELECT count(*) FROM game.events WHERE acked_at IS NULL"); count != 101 {
		t.Fatalf("pending events=%d, want 101", count)
	}
	runCtx, stop := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { c.Run(runCtx); close(done) }()
	defer func() { stop(); <-done }()
	eventually(t, func() bool { return f.count("SELECT count(*) FROM game.events WHERE acked_at IS NULL") == 0 })
	if state := c.State(); state.Status != "connected" || state.Invitation == nil || state.Invitation.ID != latest.ID || state.Notice != "" {
		t.Fatal("old event replaced the current pending invitation")
	}
}
