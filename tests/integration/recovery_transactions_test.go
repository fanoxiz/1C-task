//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"onec/internal/mobius/postgres"
)

func TestCancelledMoveRollsBackAndSameRequestCanRetry(t *testing.T) {
	f := invitationSetup(t)
	a := f.register()
	g := f.start(a, "X")
	gate, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(context.Background())
	if _, err := gate.Exec(f.ctx, "SELECT pg_advisory_xact_lock(762018003)"); err != nil {
		t.Fatal(err)
	}
	f.exec(`CREATE FUNCTION game.hold_move() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(762018003); RETURN NEW; END; $$;
		CREATE TRIGGER hold_move BEFORE INSERT ON game.moves FOR EACH ROW EXECUTE FUNCTION game.hold_move()`)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	request := moveRequest(f.id(), 1, 0)
	done := make(chan error, 1)
	go func() { _, err := f.service.Play(ctx, "", g.ID, request); done <- err }()
	eventually(t, func() bool {
		return f.count("SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'INSERT INTO game.moves%'") == 1
	})
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled command succeeded")
	}
	if err := gate.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.exec("DROP TRIGGER hold_move ON game.moves")
	if f.count("SELECT count(*) FROM game.moves") != 0 || f.count("SELECT count(*) FROM game.events WHERE type='mobius_move'") != 0 {
		t.Fatal("cancelled transaction left a partial move/event")
	}
	f.move(a, g.ID, "mobius", request, 200)
	if f.count("SELECT count(*) FROM game.moves") != 1 || f.count("SELECT count(*) FROM game.events WHERE type='mobius_move'") != 1 {
		t.Fatal("retry did not create exactly one move/event")
	}
}

func TestFinalMoveAndDisconnectAreSerialized(t *testing.T) {
	for _, finalFirst := range []bool{false, true} {
		name := "disconnect_first"
		if finalFirst {
			name = "final_move_first"
		}
		t.Run(name, func(t *testing.T) {
			f := invitationSetup(t)
			a := f.register()
			previous := f.finish(a, "O", []int{0, 3, 1, 4, 2})
			g := f.start(a, "X")
			for n, cell := range []int{0, 3, 1, 4} {
				actor := "mobius"
				if n%2 == 1 {
					actor = "loki"
				}
				f.move(a, g.ID, actor, moveRequest(f.id(), n+1, cell), 200)
			}
			gate, err := f.pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err := gate.Exec(f.ctx, "SELECT id FROM game.games WHERE id=$1 FOR UPDATE", g.ID); err != nil {
				t.Fatal(err)
			}
			moveDone, disconnectDone := make(chan error, 1), make(chan error, 1)
			request := moveRequest(f.id(), 5, 2)
			play := func() { _, err := f.service.Play(f.ctx, "", g.ID, request); moveDone <- err }
			disconnect := func() { disconnectDone <- f.service.Disconnect(f.ctx, a.Token) }
			waitingQuery := "DELETE FROM game.games%"
			if finalFirst {
				go play()
				waitingQuery = "%FROM game.games WHERE id=$1 AND loki_id=$2 FOR UPDATE%"
			} else {
				go disconnect()
			}
			eventually(t, func() bool {
				return f.count("SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE $1", waitingQuery) == 1
			})
			if finalFirst {
				go disconnect()
			} else {
				go play()
			}
			eventually(t, func() bool {
				return f.count("SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FROM game.loki WHERE %FOR UPDATE%'") == 1
			})
			if err := gate.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			moveErr, disconnectErr := <-moveDone, <-disconnectDone
			if disconnectErr != nil {
				t.Fatal(disconnectErr)
			}
			wantPlayed := 1
			if finalFirst {
				wantPlayed = 2
				if moveErr != nil || f.count("SELECT count(*) FROM game.games WHERE id=$1 AND result='mobius_win'", g.ID) != 1 || f.count("SELECT count(*) FROM game.events WHERE game_id=$1 AND type='game_finished'", g.ID) != 1 {
					t.Fatal("committed final result lost on disconnect", moveErr)
				}
			} else if !errors.Is(moveErr, postgres.ErrNotFound) || f.count("SELECT count(*) FROM game.games WHERE id=$1", g.ID) != 0 || f.count("SELECT count(*) FROM game.moves WHERE game_id=$1", g.ID) != 0 || f.count("SELECT count(*) FROM game.events WHERE game_id=$1", g.ID) != 0 {
				t.Fatal("late final move resurrected an aborted game", moveErr)
			}
			jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/connect", a.Token, nil, 200)
			if state := f.state(a.Token); state.Statistics.Played != int64(wantPlayed) || state.Statistics.Wins != 1 {
				t.Fatal("race corrupted completed statistics")
			}
			if f.count("SELECT count(*) FROM game.games WHERE id=$1", previous.ID) != 1 {
				t.Fatal("disconnect removed previous completed history")
			}
		})
	}
}

func TestWaitingCommandRejectsLostServerOwnership(t *testing.T) {
	for _, action := range []string{"move", "startup_cleanup"} {
		t.Run(action, func(t *testing.T) {
			f := invitationSetup(t)
			release, err := f.store.LockServer(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			a := f.register()
			g := f.start(a, "X")
			gate, err := f.pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err := gate.Exec(f.ctx, "SELECT id FROM game.loki WHERE id=$1 FOR UPDATE", a.State.Loki.ID); err != nil {
				t.Fatal(err)
			}
			// The request passed the HTTP guard before losing ownership, then waited on its owner.
			if err := f.store.CheckServer(f.ctx); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			request := moveRequest(f.id(), 1, 0)
			go func() {
				if action == "startup_cleanup" {
					done <- f.service.ResetPresence(f.ctx)
					return
				}
				_, err := f.service.Play(f.ctx, "", g.ID, request)
				done <- err
			}()
			eventually(t, func() bool {
				return f.count("SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FROM game.loki %FOR UPDATE%'") == 1
			})
			f.exec(`SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND classid=0 AND objid=762018002
		AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`)
			other := postgres.NewIdentityStore(f.pool)
			secondRelease, err := other.LockServer(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer secondRelease()
			if err := gate.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("waiting command wrote after another server acquired ownership")
			}
			if f.count("SELECT count(*) FROM game.moves") != 0 || f.count("SELECT count(*) FROM game.events WHERE type='mobius_move'") != 0 {
				t.Fatal("old instance left a committed move/event")
			}
			if f.count("SELECT count(*) FROM game.games WHERE id=$1 AND status='active'", g.ID) != 1 || f.count("SELECT count(*) FROM game.loki WHERE id=$1 AND connected", a.State.Loki.ID) != 1 {
				t.Fatal("old startup cleanup damaged the new owner's activity")
			}
		})
	}
}

func TestFinalMoveChecksPresenceAfterWaitingForOwner(t *testing.T) {
	for _, actor := range []string{"mobius", "loki"} {
		t.Run(actor, func(t *testing.T) {
			f := invitationSetup(t)
			a := f.register()
			side, token, expected := "X", "", postgres.ErrNotFound
			if actor == "loki" {
				side, token, expected = "O", a.Token, postgres.ErrReconnect
			}
			g := f.start(a, side)
			for n, cell := range []int{0, 3, 1, 4} {
				turn := actor
				if n%2 == 1 {
					turn = "loki"
					if actor == "loki" {
						turn = "mobius"
					}
				}
				f.move(a, g.ID, turn, moveRequest(f.id(), n+1, cell), 200)
			}
			gate, err := f.pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err := gate.Exec(f.ctx, "SELECT id FROM game.loki WHERE id=$1 FOR UPDATE", a.State.Loki.ID); err != nil {
				t.Fatal(err)
			}
			request, done := moveRequest(f.id(), 5, 2), make(chan error, 1)
			go func() { _, err := f.service.Play(f.ctx, token, g.ID, request); done <- err }()
			eventually(t, func() bool {
				return f.count("SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FROM game.loki WHERE %FOR UPDATE%'") == 1
			})
			if _, err := gate.Exec(f.ctx, "UPDATE game.loki SET last_seen_at=clock_timestamp()-interval '30 seconds' WHERE id=$1", a.State.Loki.ID); err != nil {
				t.Fatal(err)
			}
			if err := gate.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, expected) {
				t.Fatal("expired final move was accepted after waiting", err)
			}
			if f.count("SELECT count(*) FROM game.games") != 0 || f.count("SELECT count(*) FROM game.moves") != 0 || f.count("SELECT count(*) FROM game.events WHERE type IN ('mobius_move','game_finished')") != 0 {
				t.Fatal("expired final move left a result, move or delivery")
			}
		})
	}
}
