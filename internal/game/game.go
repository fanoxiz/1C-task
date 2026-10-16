package game

import (
	"errors"
	"slices"
)

type Side string

const (
	X Side = "X"
	O Side = "O"
)

type Actor string

const (
	Mobius Actor = "mobius"
	Loki   Actor = "loki"
)

type Result string

const (
	MobiusWin Result = "mobius_win"
	LokiWin   Result = "loki_win"
	Draw      Result = "draw"
)

var (
	ErrInvalidSide  = errors.New("side must be X or O")
	ErrInvalidActor = errors.New("actor must be mobius or loki")
	ErrFinished     = errors.New("game is already finished")
	ErrWrongTurn    = errors.New("it is the other player's turn")
	ErrOutOfBounds  = errors.New("coordinates must be between 0 and 2")
	ErrCellOccupied = errors.New("cell is already occupied")
)

type Move struct {
	Number int
	Actor  Actor
	Side   Side
	Row    int
	Column int
}

// Game contains only game rules; persistence and delivery are handled by the server.
// A game must be initialized with New and used by one caller at a time.
type Game struct {
	mobiusSide Side
	board      [3][3]Side
	moves      []Move
	result     Result
}

func New(mobiusSide Side) (*Game, error) {
	if mobiusSide != X && mobiusSide != O {
		return nil, ErrInvalidSide
	}
	return &Game{mobiusSide: mobiusSide}, nil
}

func (g *Game) Board() [3][3]Side { return g.board }
func (g *Game) Moves() []Move     { return slices.Clone(g.moves) }
func (g *Game) Result() Result    { return g.result }

// NextActor returns an empty actor when the game has finished or is uninitialized.
func (g *Game) NextActor() Actor {
	if g.result != "" || (g.mobiusSide != X && g.mobiusSide != O) {
		return ""
	}
	side := X
	if len(g.moves)%2 == 1 {
		side = O
	}
	if side == g.mobiusSide {
		return Mobius
	}
	return Loki
}

// Play validates a move before changing state. Rejected moves leave the game unchanged.
func (g *Game) Play(actor Actor, row, column int) (Move, error) {
	if g.mobiusSide != X && g.mobiusSide != O {
		return Move{}, ErrInvalidSide
	}
	if actor != Mobius && actor != Loki {
		return Move{}, ErrInvalidActor
	}
	if g.result != "" {
		return Move{}, ErrFinished
	}
	if row < 0 || row >= 3 || column < 0 || column >= 3 {
		return Move{}, ErrOutOfBounds
	}
	if actor != g.NextActor() {
		return Move{}, ErrWrongTurn
	}
	if g.board[row][column] != "" {
		return Move{}, ErrCellOccupied
	}
	side := X
	if len(g.moves)%2 == 1 {
		side = O
	}
	move := Move{Number: len(g.moves) + 1, Actor: actor, Side: side, Row: row, Column: column}
	g.board[row][column] = side
	g.moves = append(g.moves, move)
	if g.won(side) {
		g.result = LokiWin
		if actor == Mobius {
			g.result = MobiusWin
		}
	} else if len(g.moves) == 9 {
		g.result = Draw
	}
	return move, nil
}

func (g *Game) won(side Side) bool {
	for i := range 3 {
		if g.board[i][0] == side && g.board[i][1] == side && g.board[i][2] == side {
			return true
		}
		if g.board[0][i] == side && g.board[1][i] == side && g.board[2][i] == side {
			return true
		}
	}
	return (g.board[0][0] == side && g.board[1][1] == side && g.board[2][2] == side) ||
		(g.board[0][2] == side && g.board[1][1] == side && g.board[2][0] == side)
}
