package protocol

import "time"

type Loki struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"display_name"`
	Connected   bool       `json:"connected"`
	CreatedAt   time.Time  `json:"created_at"`
	LastSeenAt  *time.Time `json:"last_seen_at"`
}

type State struct {
	Loki       Loki        `json:"loki"`
	ServerTime time.Time   `json:"server_time"`
	Invitation *Invitation `json:"invitation"`
	Game       *Game       `json:"game"`
	Statistics Statistics  `json:"statistics"`
}

type Registration struct {
	Token string `json:"token"`
	State State  `json:"state"`
}

type LokiList struct {
	Items      []LokiStatus `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
	ServerTime time.Time    `json:"server_time"`
}

type LokiStatus struct {
	Loki
	Invitation *Invitation  `json:"invitation"`
	Game       *GameSummary `json:"game"`
	Statistics Score        `json:"statistics"`
}

type Score struct {
	Wins    int64    `json:"wins"`
	Draws   int64    `json:"draws"`
	Losses  int64    `json:"losses"`
	Played  int64    `json:"played"`
	WinRate *float64 `json:"win_rate"`
}

type ErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
