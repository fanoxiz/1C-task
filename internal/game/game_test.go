package game_test

import (
	"errors"
	"reflect"
	"testing"

	"onec/internal/game"
)

func actorFor(mobiusSide, side game.Side) game.Actor {
	if mobiusSide == side {
		return game.Mobius
	}
	return game.Loki
}

func play(t *testing.T, mobiusSide game.Side, cells []int) *game.Game {
	t.Helper()
	g, err := game.New(mobiusSide)
	if err != nil {
		t.Fatal(err)
	}
	for i, cell := range cells {
		side := game.X
		if i%2 == 1 {
			side = game.O
		}
		actor := actorFor(mobiusSide, side)
		if g.NextActor() != actor {
			t.Fatalf("move %d: next actor %q, want %q", i+1, g.NextActor(), actor)
		}
		move, err := g.Play(actor, cell/3, cell%3)
		if err != nil {
			t.Fatalf("move %d: %v", i+1, err)
		}
		want := game.Move{Number: i + 1, Actor: actor, Side: side, Row: cell / 3, Column: cell % 3}
		if move != want {
			t.Fatalf("move: got %+v, want %+v", move, want)
		}
		if i < len(cells)-1 && g.Result() != "" {
			t.Fatalf("game finished too early after move %d: %s", i+1, g.Result())
		}
	}
	return g
}

func TestWinningLines(t *testing.T) {
	lines := []struct {
		name    string
		winning [3]int
		other   [3]int
	}{
		{"top_row", [3]int{0, 1, 2}, [3]int{3, 4, 8}},
		{"middle_row", [3]int{3, 4, 5}, [3]int{0, 1, 8}},
		{"bottom_row", [3]int{6, 7, 8}, [3]int{0, 1, 5}},
		{"left_column", [3]int{0, 3, 6}, [3]int{1, 2, 8}},
		{"middle_column", [3]int{1, 4, 7}, [3]int{0, 2, 6}},
		{"right_column", [3]int{2, 5, 8}, [3]int{0, 1, 6}},
		{"main_diagonal", [3]int{0, 4, 8}, [3]int{1, 2, 3}},
		{"other_diagonal", [3]int{2, 4, 6}, [3]int{0, 1, 3}},
	}
	for _, line := range lines {
		for _, winner := range []game.Side{game.X, game.O} {
			for _, mobiusSide := range []game.Side{game.X, game.O} {
				t.Run(line.name+"/winner_"+string(winner)+"/mobius_"+string(mobiusSide), func(t *testing.T) {
					cells := []int{line.winning[0], line.other[0], line.winning[1], line.other[1], line.winning[2]}
					if winner == game.O {
						cells = []int{line.other[0], line.winning[0], line.other[1], line.winning[1], line.other[2], line.winning[2]}
					}
					g := play(t, mobiusSide, cells)
					want := game.LokiWin
					if winner == mobiusSide {
						want = game.MobiusWin
					}
					if g.Result() != want || g.NextActor() != "" {
						t.Fatalf("got result %q, next %q; want %q, no next actor", g.Result(), g.NextActor(), want)
					}
				})
			}
		}
	}
}

func TestDrawAndNinthMoveWin(t *testing.T) {
	for _, mobiusSide := range []game.Side{game.X, game.O} {
		t.Run("draw/mobius_"+string(mobiusSide), func(t *testing.T) {
			g := play(t, mobiusSide, []int{0, 1, 2, 4, 3, 5, 7, 6, 8})
			if g.Result() != game.Draw || g.NextActor() != "" || len(g.Moves()) != 9 {
				t.Fatalf("unexpected draw state: result=%s next=%s moves=%d", g.Result(), g.NextActor(), len(g.Moves()))
			}
		})
		t.Run("ninth_move_win/mobius_"+string(mobiusSide), func(t *testing.T) {
			g := play(t, mobiusSide, []int{0, 1, 2, 3, 4, 5, 7, 6, 8})
			want := game.MobiusWin
			if mobiusSide == game.O {
				want = game.LokiWin
			}
			if g.Result() != want {
				t.Fatalf("ninth move: got %s, want %s", g.Result(), want)
			}
		})
	}
}

func TestRejectedMovesDoNotChangeState(t *testing.T) {
	cases := []struct {
		name   string
		cells  []int
		actor  game.Actor
		row    int
		column int
		want   error
	}{
		{"unknown_actor", nil, "stranger", 0, 0, game.ErrInvalidActor},
		{"wrong_turn", nil, game.Loki, 0, 0, game.ErrWrongTurn},
		{"negative_row", nil, game.Mobius, -1, 0, game.ErrOutOfBounds},
		{"row_too_large", nil, game.Mobius, 3, 0, game.ErrOutOfBounds},
		{"negative_column", nil, game.Mobius, 0, -1, game.ErrOutOfBounds},
		{"column_too_large", nil, game.Mobius, 0, 3, game.ErrOutOfBounds},
		{"occupied", []int{0}, game.Loki, 0, 0, game.ErrCellOccupied},
		{"finished_win", []int{0, 3, 1, 4, 2}, game.Loki, 2, 2, game.ErrFinished},
		{"finished_draw", []int{0, 1, 2, 4, 3, 5, 7, 6, 8}, game.Loki, 0, 0, game.ErrFinished},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := play(t, game.X, tc.cells)
			board, moves, result, next := g.Board(), g.Moves(), g.Result(), g.NextActor()
			if _, err := g.Play(tc.actor, tc.row, tc.column); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if g.Board() != board || !reflect.DeepEqual(g.Moves(), moves) || g.Result() != result || g.NextActor() != next {
				t.Fatal("rejected move changed state")
			}
		})
	}
}

func TestInvalidSideAndZeroValue(t *testing.T) {
	for _, side := range []game.Side{"", "bad"} {
		if _, err := game.New(side); !errors.Is(err, game.ErrInvalidSide) {
			t.Fatalf("invalid side %q: %v", side, err)
		}
	}
	var g game.Game
	if _, err := g.Play(game.Mobius, 0, 0); !errors.Is(err, game.ErrInvalidSide) {
		t.Fatal("uninitialized game accepted a move")
	}
}

func TestBoardAndHistoryAreCopies(t *testing.T) {
	g := play(t, game.O, []int{0, 4})
	board, moves := g.Board(), g.Moves()
	board[0][0] = game.O
	moves[0].Row = 2
	if g.Board()[0][0] != game.X || g.Moves()[0].Row != 0 {
		t.Fatal("caller can modify internal state through a snapshot")
	}
}
