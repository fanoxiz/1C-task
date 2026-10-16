package protocol

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type Statistics struct {
	Wins          int64    `json:"wins"`
	Played        int64    `json:"played"`
	WinRate       *float64 `json:"win_rate"`
	BetterPercent *int     `json:"better_percent"`
	Message       string   `json:"message"`
}

type Invitation struct {
	ID          string     `json:"id"`
	LokiID      string     `json:"loki_id"`
	MobiusSide  string     `json:"mobius_side"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	RespondedAt *time.Time `json:"responded_at"`
	GameID      *string    `json:"game_id"`
	Statistics  Statistics `json:"statistics_snapshot"`
}

type GameSummary struct {
	ID         string `json:"id"`
	MobiusSide string `json:"mobius_side"`
	LokiSide   string `json:"loki_side"`
	NextActor  string `json:"next_actor"`
	Status     string `json:"status"`
}

type Event struct {
	ID        string          `json:"id"`
	Sequence  int64           `json:"sequence"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Payload   json.RawMessage `json:"payload"`
}

type EventBatch struct {
	Items []Event `json:"items"`
}

func IsUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil
}
