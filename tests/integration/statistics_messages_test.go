//go:build integration

package integration

import (
	"testing"

	"onec/internal/protocol"
)

func TestStatisticsMessagesCompareOtherLoki(t *testing.T) {
	for _, tc := range []struct {
		name    string
		others  [][2]int
		better  int
		beaten  int
		message string
	}{
		{"own_half_beats_everyone", [][2]int{{0, 1}, {1, 3}}, 0, 100, "Вы лучше 100 % своих версий."},
		{"baseline", [][2]int{{0, 1}, {1, 1}}, 50, 50, "Вы бейзлайн"},
		{"worse", [][2]int{{1, 1}, {2, 3}}, 100, 0, "Вы хуже 100 % своих версий. Вы вообще стараетесь?"},
		{"ties_not_counted_as_beaten", [][2]int{{2, 4}, {0, 1}}, 0, 50, "Вы бейзлайн"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := invitationSetup(t)
			a := f.register()
			f.scores(a.State.Loki.ID, 1, 2)
			for _, counts := range tc.others {
				other := f.register()
				f.scores(other.State.Loki.ID, counts[0], counts[1])
				jsonRequest(t, "POST", f.public.URL+"/api/v1/loki/disconnect", other.Token, nil, 204)
			}
			score := f.state(a.Token).Statistics
			if score.BetterPercent == nil || *score.BetterPercent != tc.better || score.OutperformedPercent == nil || *score.OutperformedPercent != tc.beaten || score.Message != tc.message {
				t.Fatalf("incorrect relative message: %+v", score)
			}
			invitation := decode[protocol.Invitation](t, f.invite(a.State.Loki.ID, f.id(), "X", 200))
			if invitation.Statistics.Message != tc.message || invitation.Statistics.Draws == nil || invitation.Statistics.Losses == nil || invitation.Statistics.Wins+*invitation.Statistics.Draws+*invitation.Statistics.Losses != invitation.Statistics.Played {
				t.Fatal("invitation lost relative message or outcome counts")
			}
		})
	}
}
