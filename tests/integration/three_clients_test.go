//go:build integration

package integration

import (
	"sync"
	"testing"

	"onec/internal/loki/client"
	"onec/internal/protocol"
)

func TestThreeClientsInterleavedMovesAndIndependentResults(t *testing.T) {
	f := invitationSetup(t)
	var clients [3]*client.Client
	var players [3]protocol.Registration
	var games [3]protocol.Game
	sides := []string{"X", "O", "X"}
	cells := [][]int{{0, 3, 1, 4, 2}, {0, 3, 1, 4, 2}, {0, 1, 2, 4, 3, 5, 7, 6, 8}}
	for i := range clients {
		clients[i], players[i] = recoveryClient(t, f, f.public.URL)
		games[i] = f.start(players[i], sides[i])
		runRecoveryClient(t, f, clients[i])
	}
	orders := [][]int{{2, 0, 1}, {1, 2, 0}, {0, 1, 2}}
	sharedFirstRequest := f.id()
	for round := range 9 {
		var workers sync.WaitGroup
		results := make(chan error, 3)
		start := make(chan struct{})
		for _, i := range orders[round%len(orders)] {
			if round >= len(cells[i]) {
				continue
			}
			id := sharedFirstRequest
			if round != 0 {
				id = f.id()
			}
			request := moveRequest(id, round+1, cells[i][round])
			mobius := (round%2 == 0) == (sides[i] == "X")
			workers.Go(func() {
				<-start
				if mobius {
					_, err := f.service.Play(f.ctx, "", games[i].ID, request)
					results <- err
				} else {
					results <- clients[i].Play(f.ctx, games[i].ID, request)
				}
			})
		}
		close(start)
		workers.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatalf("interleaved round %d: %v", round+1, err)
			}
		}
		for i, game := range games {
			stored, err := f.service.ReadGame(f.ctx, players[i].Token, game.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := min(round+1, len(cells[i]))
			if stored.LokiID != players[i].State.Loki.ID || len(stored.Moves) != want {
				t.Fatal("interleaved commands changed another client's history")
			}
			for n, move := range stored.Moves {
				if move.Number != n+1 || move.Row*3+move.Column != cells[i][n] {
					t.Fatal("moves were mixed between boards")
				}
			}
		}
	}
	results := []string{"mobius_win", "loki_win", "draw"}
	counts := [][3]int64{{0, 0, 1}, {1, 0, 0}, {0, 1, 0}}
	for i, c := range clients {
		eventually(t, func() bool {
			view := c.State()
			return view.Game != nil && view.Game.ID == games[i].ID && view.Game.Result == results[i] &&
				f.count("SELECT count(*) FROM game.events WHERE loki_id=$1 AND acked_at IS NULL", players[i].State.Loki.ID) == 0
		})
		score := c.State().Statistics
		if score.Played != 1 || score.Draws == nil || score.Losses == nil || score.Wins != counts[i][0] || *score.Draws != counts[i][1] || *score.Losses != counts[i][2] {
			t.Fatalf("wrong win/draw/loss counts for client %d: %+v", i+1, score)
		}
		if f.count("SELECT count(*) FROM game.moves WHERE game_id=$1 AND actor='mobius' AND delivered_at IS NULL", games[i].ID) != 0 {
			t.Fatal("one client's events blocked delivery to another")
		}
		if history := f.history(players[i].State.Loki.ID, ""); len(history.Items) != 1 || history.Items[0].ID != games[i].ID {
			t.Fatal("completed history contains another client's game")
		}
	}
}
