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
	Loki       Loki      `json:"loki"`
	ServerTime time.Time `json:"server_time"`
}

type Registration struct {
	Token string `json:"token"`
	State State  `json:"state"`
}

type LokiList struct {
	Items      []Loki `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type ErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
