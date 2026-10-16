package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"onec/internal/loki/profile"
	"onec/internal/protocol"
)

var (
	ErrUnavailable = errors.New("сервер недоступен; проверьте адрес и соединение")
	ErrToken       = errors.New("сохранённый токен не принят сервером; новая личность не создавалась")
	ErrResponse    = errors.New("сервер вернул некорректный ответ")
	ErrStorage     = errors.New("не удалось сохранить профиль клиента")
	ErrName        = errors.New("укажите имя для первой регистрации")
)

type View struct {
	ServerAddress string         `json:"server_address"`
	Status        string         `json:"status"`
	Error         string         `json:"error,omitempty"`
	Identity      *protocol.Loki `json:"identity,omitempty"`
}

type Client struct {
	operation sync.Mutex
	mu        sync.RWMutex
	view      View
	store     *profile.Store
	http      *http.Client
	server    string
	token     string
	enabled   bool
}

func New(store *profile.Store) *Client {
	return &Client{
		store: store,
		view:  View{Status: "disconnected", ServerAddress: store.Data().LastServer},
		http: &http.Client{
			Timeout: 3 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *Client) State() View {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v := c.view
	if v.Identity != nil {
		identity := *v.Identity
		v.Identity = &identity
	}
	return v
}

func (c *Client) update(status string, state *protocol.State, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.view.ServerAddress, c.view.Status, c.view.Error = c.server, status, ""
	if state != nil {
		identity := state.Loki
		c.view.Identity = &identity
	}
	if err != nil {
		c.view.Error = err.Error()
	}
	if c.view.Identity != nil && status != "connected" {
		c.view.Identity.Connected = false
	}
}

func (c *Client) Connect(ctx context.Context, address, name string) error {
	server, err := profile.Normalize(address)
	if err != nil {
		return err
	}
	c.operation.Lock()
	defer c.operation.Unlock()
	return c.connect(ctx, server, name)
}

func (c *Client) connect(ctx context.Context, server, name string) error {
	data := c.store.Data()
	saved, exists := data.Profiles[server]
	if !exists && strings.TrimSpace(name) == "" {
		return ErrName
	}
	if c.enabled && c.server != server {
		_ = c.request(ctx, http.MethodPost, "/api/v1/loki/disconnect", nil, nil)
	}
	c.enabled = false
	if c.server != server {
		c.mu.Lock()
		c.view.Identity = nil
		c.mu.Unlock()
	}
	c.server, c.token = server, saved.Token
	c.update("connecting", nil, nil)
	var state protocol.State
	if !exists {
		// A failed first connection must not resume the previous server on restart.
		// Also check storage before creating a remote identity whose token must be saved.
		data.LastServer, data.Reconnect = server, false
		if err := c.store.Save(data); err != nil {
			c.update("error", nil, ErrStorage)
			return ErrStorage
		}
		var registration protocol.Registration
		if err := c.request(ctx, http.MethodPost, "/api/v1/loki/register", map[string]string{"display_name": name}, &registration); err != nil {
			c.update("error", nil, err)
			return err
		}
		decoded, err := base64.RawURLEncoding.DecodeString(registration.Token)
		if err != nil || len(decoded) != 32 || registration.State.Loki.ID == "" {
			c.update("error", nil, ErrResponse)
			return ErrResponse
		}
		c.token = registration.Token
		state = registration.State
		data.Profiles[server] = profile.Profile{Token: c.token, DisplayName: state.Loki.DisplayName}
	}
	data.LastServer, data.Reconnect = server, true
	if err := c.store.Save(data); err != nil {
		_ = c.request(ctx, http.MethodPost, "/api/v1/loki/disconnect", nil, nil)
		c.update("error", nil, ErrStorage)
		return ErrStorage
	}
	c.enabled = true
	if exists {
		if err := c.request(ctx, http.MethodPost, "/api/v1/loki/connect", nil, &state); err != nil {
			c.failed(err)
			return err
		}
	}
	if state.Loki.ID == "" || !state.Loki.Connected {
		c.failed(ErrResponse)
		return ErrResponse
	}
	c.update("connected", &state, nil)
	return nil
}

func (c *Client) failed(err error) {
	if errors.Is(err, ErrToken) || errors.Is(err, ErrResponse) {
		c.enabled = false
	}
	c.update("error", nil, err)
}

func (c *Client) Disconnect(ctx context.Context) error {
	c.operation.Lock()
	defer c.operation.Unlock()
	return c.disconnect(ctx, true)
}

func (c *Client) disconnect(ctx context.Context, explicit bool) error {
	c.enabled = false
	var remoteErr error
	if c.server != "" && c.token != "" {
		remoteErr = c.request(ctx, http.MethodPost, "/api/v1/loki/disconnect", nil, nil)
	}
	if explicit {
		data := c.store.Data()
		data.Reconnect = false
		if err := c.store.Save(data); err != nil {
			c.update("disconnected", nil, ErrStorage)
			return ErrStorage
		}
	}
	if remoteErr != nil {
		c.update("disconnected", nil, errors.New("опрос остановлен; сервер отметит отключение по таймауту"))
	} else {
		c.update("disconnected", nil, nil)
	}
	return nil
}

func (c *Client) Shutdown(ctx context.Context) {
	c.operation.Lock()
	defer c.operation.Unlock()
	_ = c.disconnect(ctx, false)
}

func (c *Client) Run(ctx context.Context) {
	c.operation.Lock()
	data := c.store.Data()
	if c.server == "" && data.Reconnect && data.LastServer != "" {
		if server, err := profile.Normalize(data.LastServer); err == nil {
			_ = c.connect(ctx, server, "")
		}
	}
	c.operation.Unlock()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.poll(ctx)
		}
	}
}

func (c *Client) poll(ctx context.Context) {
	c.operation.Lock()
	defer c.operation.Unlock()
	if !c.enabled {
		return
	}
	var state protocol.State
	method, path := http.MethodGet, "/api/v1/loki/state"
	if c.State().Status != "connected" {
		method, path = http.MethodPost, "/api/v1/loki/connect"
	}
	err := c.request(ctx, method, path, nil, &state)
	if errors.Is(err, errReconnect) {
		err = c.request(ctx, http.MethodPost, "/api/v1/loki/connect", nil, &state)
	}
	if err != nil {
		c.failed(err)
		return
	}
	if state.Loki.ID == "" || !state.Loki.Connected {
		c.failed(ErrResponse)
		return
	}
	c.update("connected", &state, nil)
}

var errReconnect = errors.New("reconnect required")

func (c *Client) request(ctx context.Context, method, path string, body, result any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return ErrResponse
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, c.server+path, bytes.NewReader(data))
	if err != nil {
		return ErrResponse
	}
	if c.token != "" {
		r.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(r)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return ErrToken
	}
	if response.StatusCode == http.StatusConflict {
		var failure protocol.ErrorResponse
		if json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&failure) == nil && failure.Error.Code == "reconnect_required" {
			return errReconnect
		}
	}
	if response.StatusCode >= 500 {
		return ErrUnavailable
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrResponse
	}
	if result != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(result); err != nil {
			return ErrResponse
		}
	}
	return nil
}

func IsInputError(err error) bool {
	return errors.Is(err, ErrName) || errors.Is(err, profile.ErrAddress)
}
