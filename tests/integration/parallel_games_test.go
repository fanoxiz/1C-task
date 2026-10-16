//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"onec/internal/mobius/postgres"
	"onec/internal/protocol"
)

func TestOtherGameProgressesWhileOwnerTransactionWaits(t *testing.T) {
	f := invitationSetup(t)
	a, b := f.register(), f.register()
	ga, gb := f.start(a, "X"), f.start(b, "X")
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(f.ctx, "SELECT id FROM game.loki WHERE id=$1 FOR UPDATE", a.State.Loki.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	request := moveRequest(f.id(), 1, 8)
	done := make(chan error, 1)
	go func() { _, err := f.service.Play(ctx, "", ga.ID, request); done <- err }()
	eventually(t, func() bool {
		return f.count(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock'
			AND query LIKE '%FROM game.loki WHERE id = $1 FOR UPDATE%'`) == 1
	})
	// Complete B while A is still waiting on a real PostgreSQL row lock.
	for n, cell := range []int{0, 3, 1, 4, 2} {
		actor := "mobius"
		if n%2 == 1 {
			actor = "loki"
		}
		f.move(b, gb.ID, actor, moveRequest(f.id(), n+1, cell), 200)
	}
	if got := f.state(b.Token).Game; got == nil || got.Result != "mobius_win" {
		t.Fatal("unrelated game could not finish while A was locked")
	}
	select {
	case err := <-done:
		t.Fatalf("A did not remain blocked: %v", err)
	default:
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := f.state(a.Token).Game; got == nil || len(got.Moves) != 1 || got.Board[2][2] != "X" {
		t.Fatal("waiting move did not resume in its own game")
	}
}

func TestParallelGamesWithUnacknowledgedMovesAndDisconnect(t *testing.T) {
	f := invitationSetup(t)
	a, b := f.register(), f.register()
	ga, gb := f.start(a, "X"), f.start(b, "X")
	// The same request UUID is valid independently in different games.
	shared := f.id()
	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	for _, g := range []protocol.Game{ga, gb} {
		go func() {
			<-start
			_, err := f.service.Play(f.ctx, "", g.ID, moveRequest(shared, 1, 0))
			errorsCh <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
	var delayed protocol.Event
	for _, event := range f.events(a.Token).Items {
		if event.Type == "mobius_move" {
			delayed = event
		}
	}
	if delayed.ID == "" {
		t.Fatal("missing delayed event")
	}
	// B receives and acknowledges its own moves while A never ACKs.
	for n, cell := range []int{3, 1, 4, 2} {
		actor := "loki"
		if n%2 == 1 {
			actor = "mobius"
		}
		f.move(b, gb.ID, actor, moveRequest(f.id(), n+2, cell), 200)
		for _, event := range f.events(b.Token).Items {
			jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/events/"+event.ID+"/ack", b.Token, nil, 204)
		}
	}
	if f.count("SELECT count(*) FROM game.moves WHERE game_id=$1 AND delivered_at IS NOT NULL", ga.ID) != 0 || f.count("SELECT count(*) FROM game.events WHERE id=$1 AND acked_at IS NULL", delayed.ID) != 1 {
		t.Fatal("B's ACK confirmed A's event")
	}
	finished := f.state(b.Token).Game
	if finished == nil || finished.Result != "mobius_win" || len(f.events(b.Token).Items) != 0 {
		t.Fatal("unacknowledged A prevented B from finishing/draining its queue")
	}
	// Disconnecting A must preserve B's new active game and completed history.
	bNext := f.start(b, "O")
	f.move(b, bNext.ID, "loki", moveRequest(f.id(), 1, 8), 200)
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/disconnect", a.Token, nil, 204)
	if f.count("SELECT count(*) FROM game.games WHERE id=$1", ga.ID) != 0 || f.count("SELECT count(*) FROM game.events WHERE id=$1", delayed.ID) != 0 {
		t.Fatal("A's unfinished game and pending move were not deleted")
	}
	state := f.state(b.Token)
	if state.Game == nil || state.Game.ID != bNext.ID || state.Game.Board[2][2] != "X" || state.Game.NextActor != "mobius" || !state.Loki.Connected || state.Statistics.Played != 1 {
		t.Fatal("A's disconnect damaged B's active game or statistics")
	}
	if history := f.history(b.State.Loki.ID, ""); len(history.Items) != 1 || history.Items[0].ID != gb.ID {
		t.Fatal("A's disconnect damaged B's completed history")
	}
	f.move(b, bNext.ID, "mobius", moveRequest(f.id(), 2, 4), 200)
}

func TestCompetingInvitationsAndDecisionsKeepOneActivity(t *testing.T) {
	f := invitationSetup(t)
	a := f.register()
	type reply struct {
		invitation protocol.Invitation
		err        error
	}
	start := make(chan struct{})
	replies := make(chan reply, 8)
	var wg sync.WaitGroup
	for range 8 {
		request := f.id()
		wg.Go(func() {
			<-start
			i, err := f.service.Invite(f.ctx, a.State.Loki.ID, request, "X")
			replies <- reply{i, err}
		})
	}
	close(start)
	wg.Wait()
	close(replies)
	var winner protocol.Invitation
	for reply := range replies {
		if reply.err == nil {
			if winner.ID != "" {
				t.Fatal("distinct concurrent requests created multiple pending invitations")
			}
			winner = reply.invitation
		} else if !errors.Is(reply.err, postgres.ErrBusy) {
			t.Fatal(reply.err)
		}
	}
	if winner.ID == "" {
		t.Fatal("no invitation won")
	}
	start = make(chan struct{})
	replies = make(chan reply, 2)
	for _, decision := range []string{"accept", "decline"} {
		wg.Go(func() {
			<-start
			i, err := f.service.Respond(f.ctx, a.Token, winner.ID, decision)
			replies <- reply{i, err}
		})
	}
	close(start)
	wg.Wait()
	close(replies)
	accepted, success := false, 0
	for reply := range replies {
		if reply.err == nil {
			success++
			accepted = reply.invitation.Status == "accepted"
		} else if !errors.Is(reply.err, postgres.ErrConflict) {
			t.Fatal(reply.err)
		}
	}
	if success != 1 || f.count("SELECT count(*) FROM game.invitations WHERE status='pending'") != 0 {
		t.Fatal("conflicting decisions were both applied")
	}
	wantGames := 0
	if accepted {
		wantGames = 1
	}
	if f.count("SELECT count(*) FROM game.games") != wantGames || f.count("SELECT count(*) FROM game.events WHERE type='invitation_closed'") != 1 {
		t.Fatal("competing decisions produced an inconsistent game/event")
	}
	if !accepted {
		winner = decode[protocol.Invitation](t, f.invite(a.State.Loki.ID, f.id(), "X", 200))
	}
	// An acceptance racing with another invitation must always leave just one active game.
	start = make(chan struct{})
	errs := make(chan error, 9)
	wg.Go(func() { <-start; _, err := f.service.Respond(f.ctx, a.Token, winner.ID, "accept"); errs <- err })
	for range 8 {
		request := f.id()
		wg.Go(func() { <-start; _, err := f.service.Invite(f.ctx, a.State.Loki.ID, request, "O"); errs <- err })
	}
	close(start)
	wg.Wait()
	close(errs)
	success = 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, postgres.ErrBusy) {
			t.Fatal(err)
		}
	}
	if success != 1 || f.count("SELECT count(*) FROM game.games WHERE status='active'") != 1 || f.count("SELECT count(*) FROM game.invitations WHERE status='pending'") != 0 {
		t.Fatal("acceptance/invitation race violated the single-activity invariant")
	}
}

func TestPublicObjectsAndEventSequencesArePrivate(t *testing.T) {
	f := invitationSetup(t)
	a, b := f.register(), f.register()
	for range 3 {
		i := decode[protocol.Invitation](t, f.invite(a.State.Loki.ID, f.id(), "X", 200))
		f.respond(a.Token, i.ID, "decline", 200)
	}
	i := decode[protocol.Invitation](t, f.invite(b.State.Loki.ID, f.id(), "X", 200))
	if events := f.events(b.Token).Items; len(events) != 1 || events[0].Sequence != 1 {
		t.Fatal("another Loki's activity leaked through a global sequence")
	}
	f.respond(a.Token, i.ID, "accept", 404)
	f.respond(a.Token, i.ID, "decline", 404)
	f.respond(b.Token, i.ID, "accept", 200)
	g := f.state(b.Token).Game
	if g == nil {
		t.Fatal("game missing")
	}
	f.move(b, g.ID, "mobius", moveRequest(f.id(), 1, 0), 200)
	foreignEvents := f.events(b.Token).Items
	for n, event := range foreignEvents {
		if event.Sequence != int64(n+1) {
			t.Fatal("event sequence includes another client's activity")
		}
		jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/events/"+event.ID+"/ack", a.Token, nil, 404)
	}
	jsonRequest(t, "GET", f.public.URL+"/api/v1/loki/games/"+g.ID, a.Token, nil, 404)
	f.move(a, g.ID, "loki", moveRequest(f.id(), 2, 1), 404)
	for _, path := range []string{
		"/api/v1/mobius/loki", "/api/v1/mobius/games", "/api/v1/mobius/games/" + g.ID,
		"/api/v1/mobius/loki/" + b.State.Loki.ID + "/invitations", "/api/v1/mobius/loki/" + b.State.Loki.ID + "/games",
	} {
		jsonRequest(t, "GET", f.public.URL+path, a.Token, nil, 404)
	}
	jsonRequest(t, "POST", f.public.URL+"/api/v1/mobius/games/"+g.ID+"/moves", a.Token, moveRequest(f.id(), 2, 1), 404)
	jsonRequest(t, "POST", f.public.URL+"/api/v1/mobius/loki/"+b.State.Loki.ID+"/invitations", a.Token, map[string]string{"request_id": f.id(), "mobius_side": "O"}, 404)
	for _, path := range []string{"/api/v1/loki/state", "/api/v1/loki/events"} {
		body := jsonRequest(t, "GET", f.public.URL+path+"?loki_id="+b.State.Loki.ID, a.Token, nil, 200)
		for _, foreign := range []string{b.State.Loki.ID, g.ID, i.ID, foreignEvents[0].ID} {
			if strings.Contains(string(body), foreign) {
				t.Fatal("caller-supplied identity exposed a foreign object")
			}
		}
	}
	if f.count("SELECT count(*) FROM game.events WHERE loki_id=$1 AND acked_at IS NOT NULL", b.State.Loki.ID) != 0 || f.count("SELECT count(*) FROM game.moves WHERE game_id=$1", g.ID) != 1 || f.count("SELECT count(*) FROM game.moves WHERE game_id=$1 AND delivered_at IS NOT NULL", g.ID) != 0 {
		t.Fatal("foreign commands changed the victim's game or acknowledgements")
	}
}
