package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

type Move struct {
	ID          string     `json:"id"`
	RequestID   string     `json:"request_id"`
	Number      int        `json:"number"`
	Actor       string     `json:"actor"`
	Side        string     `json:"side"`
	Row         int        `json:"row"`
	Column      int        `json:"column"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at"`
}

type Game struct {
	GameSummary
	Board      [3][3]string `json:"board"`
	Moves      []Move       `json:"moves"`
	CreatedAt  time.Time    `json:"created_at"`
	FinishedAt *time.Time   `json:"finished_at"`
}

type GameList struct {
	Items      []GameSummary `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type GameHistoryItem struct {
	GameSummary
	CreatedAt  time.Time `json:"created_at"`
	FinishedAt time.Time `json:"finished_at"`
}

type GameHistory struct {
	Items      []GameHistoryItem `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type MoveRequest struct {
	RequestID          string `json:"request_id"`
	ExpectedMoveNumber int    `json:"expected_move_number"`
	Row                int    `json:"row"`
	Column             int    `json:"column"`
}

// Coordinates include zero, so omitted and null fields must be distinguished from zero.
func (r *MoveRequest) UnmarshalJSON(data []byte) error {
	var wire struct {
		RequestID          string `json:"request_id"`
		ExpectedMoveNumber *int   `json:"expected_move_number"`
		Row                *int   `json:"row"`
		Column             *int   `json:"column"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.ExpectedMoveNumber == nil || wire.Row == nil || wire.Column == nil {
		return fmt.Errorf("move number, row and column are required")
	}
	*r = MoveRequest{RequestID: wire.RequestID, ExpectedMoveNumber: *wire.ExpectedMoveNumber, Row: *wire.Row, Column: *wire.Column}
	return nil
}

func (r MoveRequest) Valid() bool {
	return IsUUID(r.RequestID) && r.ExpectedMoveNumber >= 1 && r.ExpectedMoveNumber <= 9 && r.Row >= 0 && r.Row <= 2 && r.Column >= 0 && r.Column <= 2
}

type MoveEvent struct {
	Game Game `json:"game"`
	Move Move `json:"move"`
}
