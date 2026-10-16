//go:build integration

package integration

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"onec/internal/mobius/postgres"
	"onec/internal/protocol"
)

func (f *invitationFixture) start(player protocol.Registration, side string) protocol.Game {
	f.t.Helper()
	i := decode[protocol.Invitation](f.t, f.invite(player.State.Loki.ID, f.id(), side, 200))
	f.respond(player.Token, i.ID, "accept", 200)
	current := f.state(player.Token).Game
	if current == nil {
		f.t.Fatal("accepted invitation has no game")
	}
	return *current
}

func (f *invitationFixture) move(player protocol.Registration, id, actor string, request protocol.MoveRequest, status int) []byte {
	f.t.Helper()
	base, token := f.admin.URL+"/api/v1/mobius/games/", ""
	if actor == "loki" {
		base, token = f.public.URL+"/api/v1/loki/games/", player.Token
	}
	return jsonRequest(f.t, "POST", base+id+"/moves", token, request, status)
}

func moveRequest(id string, number, cell int) protocol.MoveRequest {
	return protocol.MoveRequest{RequestID: id, ExpectedMoveNumber: number, Row: cell / 3, Column: cell % 3}
}

func TestGamesCompleteAndRetry(t *testing.T) {
	f := invitationSetup(t)
	cases := []struct {
		name, side, result string
		cells              []int
	}{
		{"mobius_X_win", "X", "mobius_win", []int{0, 3, 1, 4, 2}},
		{"loki_X_win", "O", "loki_win", []int{0, 3, 1, 4, 2}},
		{"mobius_O_win", "O", "mobius_win", []int{0, 3, 1, 4, 8, 5}},
		{"loki_O_win", "X", "loki_win", []int{0, 3, 1, 4, 8, 5}},
		{"draw", "X", "draw", []int{0, 1, 2, 4, 3, 5, 7, 6, 8}},
		{"ninth_move_win", "X", "mobius_win", []int{0, 1, 2, 3, 4, 5, 7, 6, 8}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.t = t
			player := f.register()
			board := f.start(player, tc.side)
			var last protocol.MoveRequest
			var lastActor string
			for n, cell := range tc.cells {
				actor := "mobius"
				side := "X"
				if n%2 == 1 {
					side = "O"
				}
				if side != tc.side {
					actor = "loki"
				}
				request := moveRequest(f.id(), n+1, cell)
				board = decode[protocol.Game](t, f.move(player, board.ID, actor, request, 200))
				if len(board.Moves) != n+1 || board.Board[cell/3][cell%3] != side {
					t.Fatalf("move lost: %+v", board)
				}
				if n < len(tc.cells)-1 && (board.Status != "active" || board.Result != "") {
					t.Fatal("game finished early")
				}
				last, lastActor = request, actor
			}
			if board.Status != "completed" || board.Result != tc.result || board.NextActor != "" || board.FinishedAt == nil {
				t.Fatalf("wrong final state: %+v", board)
			}
			retry := decode[protocol.Game](t, f.move(player, board.ID, lastActor, last, 200))
			if retry.Result != tc.result || len(retry.Moves) != len(tc.cells) {
				t.Fatal("last move replay changed outcome")
			}
			if f.count("SELECT count(*) FROM game.games WHERE loki_id=$1 AND status='completed'", player.State.Loki.ID) != 1 {
				t.Fatal("result counted more than once")
			}
			failure := decode[protocol.ErrorResponse](t, f.move(player, board.ID, "mobius", moveRequest(f.id(), 9, 8), 409))
			if failure.Error.Code != "game_finished" {
				t.Fatal("new move after completion not rejected")
			}
			var finalEvent *protocol.Event
			for _, event := range f.events(player.Token).Items {
				if event.Type == "game_finished" {
					copy := event
					finalEvent = &copy
				}
			}
			if finalEvent == nil {
				t.Fatal("final result event missing")
			}
			jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/events/"+finalEvent.ID+"/ack", player.Token, nil, 204)
			stored := decode[protocol.Game](t, jsonRequest(t, "GET", f.admin.URL+"/api/v1/mobius/games/"+board.ID, "", nil, 200))
			for _, move := range stored.Moves {
				if move.Actor == "mobius" && move.DeliveredAt != nil {
					t.Fatal("result ACK incorrectly confirmed a move")
				}
			}
			for _, event := range f.events(player.Token).Items {
				if event.Type == "mobius_move" {
					jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/events/"+event.ID+"/ack", player.Token, nil, 204)
				}
			}
			stored = decode[protocol.Game](t, jsonRequest(t, "GET", f.admin.URL+"/api/v1/mobius/games/"+board.ID, "", nil, 200))
			for _, move := range stored.Moves {
				if move.Actor == "mobius" && move.DeliveredAt == nil {
					t.Fatal("move ACK not visible to Mobius")
				}
			}
			jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/disconnect", player.Token, nil, 204)
			jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/connect", player.Token, nil, 200)
			if restored := f.state(player.Token).Game; restored == nil || restored.Result != tc.result || len(restored.Moves) != len(tc.cells) {
				t.Fatal("completed game lost on reconnect")
			}
			invitation := decode[protocol.Invitation](t, f.invite(player.State.Loki.ID, f.id(), "X", 200))
			wins := int64(0)
			if tc.result == "loki_win" {
				wins = 1
			}
			if invitation.Statistics.Played != 1 || invitation.Statistics.Wins != wins {
				t.Fatal("completed game not reflected exactly once in statistics")
			}
			f.respond(player.Token, invitation.ID, "decline", 200)
		})
	}
}

func TestMoveValidationAndOwnership(t *testing.T) {
	f := invitationSetup(t)
	a, b := f.register(), f.register()
	g := f.start(a, "X")
	request := moveRequest(f.id(), 1, 0)
	wrong := decode[protocol.ErrorResponse](t, f.move(a, g.ID, "loki", request, 409))
	if wrong.Error.Code != "wrong_turn" {
		t.Fatal("wrong actor accepted")
	}
	stale := request
	stale.ExpectedMoveNumber = 2
	if failure := decode[protocol.ErrorResponse](t, f.move(a, g.ID, "mobius", stale, 409)); failure.Error.Code != "stale_move" {
		t.Fatal("stale version accepted")
	}
	for _, body := range []map[string]any{
		{"request_id": f.id(), "expected_move_number": 1, "column": 0},
		{"request_id": f.id(), "expected_move_number": 1, "row": 0, "column": 0, "actor": "mobius"},
		{"request_id": f.id(), "expected_move_number": 1, "row": 3, "column": 0},
		{"request_id": f.id(), "expected_move_number": 1, "row": 0, "column": -1},
	} {
		jsonRequest(t, "POST", f.admin.URL+"/api/v1/mobius/games/"+g.ID+"/moves", "", body, 400)
	}
	if f.count("SELECT count(*) FROM game.moves") != 0 {
		t.Fatal("invalid command modified game")
	}
	f.move(a, g.ID, "mobius", request, 200)
	f.move(a, g.ID, "mobius", request, 200)
	changed := request
	changed.Column = 1
	f.move(a, g.ID, "mobius", changed, 409)
	f.move(a, g.ID, "loki", request, 409)
	if failure := decode[protocol.ErrorResponse](t, f.move(a, g.ID, "loki", moveRequest(f.id(), 2, 0), 409)); failure.Error.Code != "cell_occupied" {
		t.Fatal("occupied cell accepted")
	}
	jsonRequest(t, "GET", f.public.URL+"/api/v1/loki/games/"+g.ID, b.Token, nil, 404)
	f.move(b, g.ID, "loki", moveRequest(f.id(), 2, 1), 404)
	jsonRequest(t, "GET", f.public.URL+"/api/v1/loki/games/"+g.ID, "", nil, 401)
	jsonRequest(t, "GET", f.public.URL+"/api/v1/mobius/games/"+g.ID, a.Token, nil, 404)
	if f.count("SELECT count(*) FROM game.moves") != 1 {
		t.Fatal("rejected or repeated commands changed history")
	}
	listed := decode[protocol.GameList](t, jsonRequest(t, "GET", f.admin.URL+"/api/v1/mobius/games?status=active", "", nil, 200))
	if len(listed.Items) != 1 || listed.Items[0].NextActor != "loki" {
		t.Fatal("active list has a stale turn")
	}
}

func TestConcurrentMoves(t *testing.T) {
	f := invitationSetup(t)
	a := f.register()
	g := f.start(a, "X")
	request := moveRequest(f.id(), 1, 0)
	type reply struct {
		game protocol.Game
		err  error
	}
	results := make(chan reply, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { g, err := f.service.Play(f.ctx, "", g.ID, request); results <- reply{g, err} })
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.err != nil || len(result.game.Moves) != 1 {
			t.Fatalf("duplicate move failed: %v", result.err)
		}
	}
	if f.count("SELECT count(*) FROM game.moves") != 1 || f.count("SELECT count(*) FROM game.events WHERE type='mobius_move'") != 1 {
		t.Fatal("same request was executed twice")
	}
	b := f.register()
	other := f.start(b, "X")
	results = make(chan reply, 8)
	requests := make([]protocol.MoveRequest, 8)
	for n := range requests {
		requests[n] = moveRequest(f.id(), 1, n)
	}
	for _, r := range requests {
		wg.Go(func() { g, err := f.service.Play(f.ctx, "", other.ID, r); results <- reply{g, err} })
	}
	wg.Wait()
	close(results)
	success := 0
	for result := range results {
		if result.err == nil {
			success++
		} else if !errors.Is(result.err, postgres.ErrStaleMove) {
			t.Fatal(result.err)
		}
	}
	if success != 1 || f.count("SELECT count(*) FROM game.moves WHERE game_id=$1", other.ID) != 1 {
		t.Fatal("competing moves were both applied")
	}
}

func TestMoveAndFinalResultRollBackOnEventFailure(t *testing.T) {
	f := invitationSetup(t)
	a := f.register()
	g := f.start(a, "X")
	f.exec(`CREATE FUNCTION game.fail_move_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'event failure'; END; $$;
		CREATE TRIGGER fail_move_event BEFORE INSERT ON game.events FOR EACH ROW WHEN (NEW.type='mobius_move') EXECUTE FUNCTION game.fail_move_event()`)
	first := moveRequest(f.id(), 1, 0)
	f.move(a, g.ID, "mobius", first, 503)
	if f.count("SELECT count(*) FROM game.moves") != 0 {
		t.Fatal("failed event left a move")
	}
	f.exec("DROP TRIGGER fail_move_event ON game.events")
	f.move(a, g.ID, "mobius", first, 200)
	f.move(a, g.ID, "loki", moveRequest(f.id(), 2, 3), 200)
	f.move(a, g.ID, "mobius", moveRequest(f.id(), 3, 1), 200)
	f.move(a, g.ID, "loki", moveRequest(f.id(), 4, 4), 200)
	f.exec(`CREATE TRIGGER fail_final_event BEFORE INSERT ON game.events FOR EACH ROW WHEN (NEW.type='game_finished') EXECUTE FUNCTION game.fail_move_event()`)
	last := moveRequest(f.id(), 5, 2)
	f.move(a, g.ID, "mobius", last, 503)
	state := f.state(a.Token)
	if state.Game.Status != "active" || state.Game.Result != "" || len(state.Game.Moves) != 4 || state.Game.Board[0][2] != "" {
		t.Fatal("final event failure left a partial result")
	}
	f.exec("DROP TRIGGER fail_final_event ON game.events")
	completed := decode[protocol.Game](t, f.move(a, g.ID, "mobius", last, 200))
	if completed.Result != "mobius_win" || f.count("SELECT count(*) FROM game.events WHERE type='game_finished'") != 1 {
		t.Fatal("final retry changed outcome")
	}
}

func TestFinishedMoveSurvivesRestartAndUnfinishedDoesNot(t *testing.T) {
	f := invitationSetup(t)
	a := f.register()
	g := f.start(a, "X")
	first := moveRequest(f.id(), 1, 0)
	f.move(a, g.ID, "mobius", first, 200)
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/disconnect", a.Token, nil, 204)
	if f.count("SELECT count(*) FROM game.moves WHERE game_id=$1", g.ID) != 0 || f.count("SELECT count(*) FROM game.events WHERE game_id=$1", g.ID) != 0 {
		t.Fatal("cancelled game kept moves or events")
	}
	f.move(a, g.ID, "mobius", first, 404)
	b := f.register()
	finished := f.start(b, "X")
	for n, cell := range []int{0, 3, 1, 4, 2} {
		actor := "mobius"
		if n%2 == 1 {
			actor = "loki"
		}
		f.move(b, finished.ID, actor, moveRequest(f.id(), n+1, cell), 200)
	}
	var lastEvent protocol.Event
	for _, event := range f.events(b.Token).Items {
		if event.Type == "mobius_move" {
			lastEvent = event
		}
	}
	if err := f.service.ResetPresence(f.ctx); err != nil {
		t.Fatal(err)
	}
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/connect", b.Token, nil, 200)
	state := f.state(b.Token)
	if state.Game == nil || state.Game.Result != "mobius_win" || len(state.Game.Moves) != 5 {
		t.Fatal("finished board was lost")
	}
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/events/"+lastEvent.ID+"/ack", b.Token, nil, 204)
	current := decode[protocol.Game](t, jsonRequest(t, "GET", f.admin.URL+"/api/v1/mobius/games/"+finished.ID, "", nil, 200))
	if current.Moves[4].DeliveredAt == nil {
		t.Fatal("final move cannot be acknowledged after restart")
	}
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/events/"+lastEvent.ID+"/ack", b.Token, nil, 204)
	updated := decode[protocol.Game](t, jsonRequest(t, "GET", f.admin.URL+"/api/v1/mobius/games/"+finished.ID, "", nil, 200))
	if !updated.Moves[4].DeliveredAt.Equal(*current.Moves[4].DeliveredAt) {
		t.Fatal("repeated ACK changed delivery time")
	}
	if strings.Contains(string(jsonRequest(t, "GET", f.public.URL+"/api/v1/loki/games/"+finished.ID, b.Token, nil, 200)), a.State.Loki.ID) {
		t.Fatal("history exposed another Loki")
	}
}
