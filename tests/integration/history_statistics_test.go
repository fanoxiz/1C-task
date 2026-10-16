//go:build integration

package integration

import (
	"bytes"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"onec/internal/loki/client"
	"onec/internal/loki/httpui"
	"onec/internal/loki/profile"
	"onec/internal/protocol"
)

func (f *invitationFixture) finish(player protocol.Registration, side string, cells []int) protocol.Game {
	f.t.Helper()
	board := f.start(player, side)
	for n, cell := range cells {
		actor := "mobius"
		if (n%2 == 0) != (side == "X") {
			actor = "loki"
		}
		board = decode[protocol.Game](f.t, f.move(player, board.ID, actor, moveRequest(f.id(), n+1, cell), 200))
	}
	if board.Status != "completed" {
		f.t.Fatal("test sequence did not finish the game")
	}
	return board
}

func (f *invitationFixture) history(owner, cursor string) protocol.GameHistory {
	f.t.Helper()
	return decode[protocol.GameHistory](f.t, jsonRequest(f.t, "GET", f.admin.URL+"/api/v1/mobius/loki/"+owner+"/games?cursor="+cursor, "", nil, 200))
}

func TestLiveStatisticsFromPlayedGamesAndFixedInvitation(t *testing.T) {
	f := invitationSetup(t)
	a, b, c, newbie := f.register(), f.register(), f.register(), f.register()
	if score := a.State.Statistics; score.Wins != 0 || score.Played != 0 || score.WinRate != nil || score.BetterPercent != nil || !strings.Contains(score.Message, "ещё не сыграли") {
		t.Fatalf("incorrect registration statistics: %+v", score)
	}
	xWin, draw := []int{0, 3, 1, 4, 2}, []int{0, 1, 2, 4, 3, 5, 7, 6, 8}
	win := f.finish(a, "O", xWin)
	if score := f.state(a.Token).Statistics; score.BetterPercent != nil || score.Message != "" {
		t.Fatalf("unplayed opponents included: %+v", score)
	}
	f.finish(a, "X", xWin)
	f.finish(a, "X", draw)
	f.finish(b, "O", xWin)
	f.finish(b, "X", xWin)
	f.finish(c, "O", xWin)
	f.finish(c, "O", draw)
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/disconnect", c.Token, nil, 204)
	assertScore := func(wins, played int64, better int) protocol.State {
		t.Helper()
		state := f.state(a.Token)
		score := state.Statistics
		if score.Wins != wins || score.Played != played || score.WinRate == nil || *score.WinRate != float64(wins)/float64(played) || score.BetterPercent == nil || *score.BetterPercent != better {
			t.Fatalf("unexpected own aggregate: %+v", score)
		}
		var sqlWins, sqlPlayed int64
		if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FILTER (WHERE result='loki_win'), count(*)
			FROM game.games WHERE loki_id=$1 AND status='completed'`, a.State.Loki.ID).Scan(&sqlWins, &sqlPlayed); err != nil {
			t.Fatal(err)
		}
		if score.Wins != sqlWins || score.Played != sqlPlayed {
			t.Fatal("state differs from completed SQL results")
		}
		list := decode[protocol.LokiList](t, jsonRequest(t, "GET", f.admin.URL+"/api/v1/mobius/loki", "", nil, 200))
		found := false
		for _, item := range list.Items {
			if item.ID == a.State.Loki.ID {
				found = true
				if item.Statistics.Wins != wins || item.Statistics.Played != played || item.Statistics.WinRate == nil || *item.Statistics.WinRate != *score.WinRate {
					t.Fatal("Mobius list differs from own statistics")
				}
			}
		}
		if !found {
			t.Fatal("owner missing from Mobius list")
		}
		return state
	}
	assertScore(1, 3, 100)
	request := f.id()
	invitation := decode[protocol.Invitation](t, f.invite(a.State.Loki.ID, request, "X", 200))
	var original protocol.Event
	for _, event := range f.events(a.Token).Items {
		if event.Type == "invitation_created" && decode[protocol.Invitation](t, event.Payload).ID == invitation.ID {
			original = event
		}
	}
	if original.ID == "" {
		t.Fatal("invitation event missing")
	}
	// Another Loki's new result changes the live comparison, but not the stored invitation.
	f.finish(b, "X", xWin)
	state := assertScore(1, 3, 50)
	if state.Invitation == nil || !reflect.DeepEqual(state.Invitation.Statistics, invitation.Statistics) || !reflect.DeepEqual(
		decode[protocol.Invitation](t, f.invite(a.State.Loki.ID, request, "X", 200)).Statistics, invitation.Statistics) {
		t.Fatal("live statistics replaced the invitation snapshot")
	}
	for range 2 {
		found := false
		for _, event := range f.events(a.Token).Items {
			if event.ID == original.ID {
				found = true
				if !bytes.Equal(event.Payload, original.Payload) {
					t.Fatal("replayed invitation payload changed")
				}
			}
			if strings.Contains(string(event.Payload), b.State.Loki.ID) || strings.Contains(string(event.Payload), c.State.Loki.ID) {
				t.Fatal("event exposes another Loki")
			}
		}
		if !found {
			t.Fatal("unacknowledged invitation lost on replay")
		}
	}
	f.respond(a.Token, invitation.ID, "decline", 200)
	f.finish(a, "O", xWin)
	assertScore(2, 4, 0) // 2/4 and the disconnected opponent's 1/2 are tied.
	unfinished := f.start(a, "X")
	f.move(a, unfinished.ID, "mobius", moveRequest(f.id(), 1, 0), 200)
	assertScore(2, 4, 0)
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/disconnect", a.Token, nil, 204)
	jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/connect", a.Token, nil, 200)
	assertScore(2, 4, 0)
	history := f.history(a.State.Loki.ID, "")
	if len(history.Items) != 4 {
		t.Fatalf("unfinished game entered history: %+v", history)
	}
	for _, item := range history.Items {
		if item.Status != "completed" || item.ID == unfinished.ID || item.LokiID != a.State.Loki.ID || item.NextActor != "" || item.FinishedAt.IsZero() {
			t.Fatalf("invalid history item: %+v", item)
		}
	}
	stored := decode[protocol.Game](t, jsonRequest(t, "GET", f.admin.URL+"/api/v1/mobius/games/"+win.ID, "", nil, 200))
	if !reflect.DeepEqual(stored, win) {
		t.Fatal("selected historical game lost its board, result or ordered moves")
	}
	if score := f.state(newbie.Token).Statistics; score.Played != 0 || score.WinRate != nil || score.BetterPercent != nil || !strings.Contains(score.Message, "ещё не сыграли") {
		t.Fatal("newbie was compared against played opponents")
	}
	// Verify the Go client and its local browser API carry the current statistics.
	store, err := profile.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	data := store.Data()
	data.Profiles[f.public.URL] = profile.Profile{Token: a.Token}
	if err := store.Save(data); err != nil {
		t.Fatal(err)
	}
	connection := client.New(store)
	if err := connection.Connect(f.ctx, f.public.URL, ""); err != nil {
		t.Fatal(err)
	}
	ui := httptest.NewServer(httpui.Handler(connection))
	defer ui.Close()
	view := decode[client.View](t, jsonRequest(t, "GET", ui.URL+"/api/v1/state", "", nil, 200))
	if !reflect.DeepEqual(view.Statistics, f.state(a.Token).Statistics) {
		t.Fatal("Loki browser API lost the current statistics")
	}
	jsonRequest(t, "GET", f.public.URL+"/api/v1/mobius/loki/"+a.State.Loki.ID+"/games", a.Token, nil, 404)
}

func TestCompletedHistoryPaginationAndCursorScope(t *testing.T) {
	f := invitationSetup(t)
	a, b, empty := f.register(), f.register(), f.register()
	if history := f.history(empty.State.Loki.ID, ""); len(history.Items) != 0 || history.Items == nil || history.NextCursor != "" {
		t.Fatal("empty history must be an empty JSON array")
	}
	f.scores(a.State.Loki.ID, 20, 53)
	f.scores(b.State.Loki.ID, 1, 1)
	// All timestamps tied: pagination must use the UUID as a second ordering key.
	f.exec("UPDATE game.games SET finished_at=statement_timestamp() + interval '1 second'")
	first := f.history(a.State.Loki.ID, "")
	if len(first.Items) != 50 || first.NextCursor != first.Items[49].ID {
		t.Fatalf("invalid first page: %+v", first)
	}
	// A newly completed game ahead of the cursor must not repeat/skip existing games.
	f.scores(a.State.Loki.ID, 0, 1)
	f.exec("UPDATE game.games SET finished_at=(SELECT max(finished_at) FROM game.games)+interval '1 second' WHERE loki_id=$1 AND finished_at<(SELECT max(finished_at) FROM game.games)", a.State.Loki.ID)
	second := f.history(a.State.Loki.ID, strings.ToUpper(first.NextCursor))
	if len(second.Items) != 3 || second.NextCursor != "" {
		t.Fatalf("invalid final page: %+v", second)
	}
	seen, previous := map[string]bool{}, ""
	for _, item := range append(first.Items, second.Items...) {
		if seen[item.ID] || (previous != "" && item.ID >= previous) || item.LokiID != a.State.Loki.ID {
			t.Fatal("history has duplicates, wrong order or a foreign game")
		}
		seen[item.ID], previous = true, item.ID
	}
	newFirst := f.history(a.State.Loki.ID, "")
	if len(newFirst.Items) != 50 || seen[newFirst.Items[0].ID] {
		t.Fatal("new results damaged first page")
	}
	base := f.admin.URL + "/api/v1/mobius/loki/"
	jsonRequest(t, "GET", base+"bad/games", "", nil, 400)
	jsonRequest(t, "GET", base+f.id()+"/games", "", nil, 404)
	for _, cursor := range []string{"bad", f.id(), f.history(b.State.Loki.ID, "").Items[0].ID} {
		jsonRequest(t, "GET", base+a.State.Loki.ID+"/games?cursor="+cursor, "", nil, 400)
	}
	active := f.start(a, "X")
	jsonRequest(t, "GET", base+a.State.Loki.ID+"/games?cursor="+active.ID, "", nil, 400)
}
