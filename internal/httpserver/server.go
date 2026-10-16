package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type Endpoint struct {
	Name    string
	Addr    string
	Handler http.Handler
}

func Run(ctx context.Context, logger *slog.Logger, endpoints ...Endpoint) error {
	var servers []*http.Server
	var listeners []net.Listener
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	// Bind every address before serving, so a port conflict cannot leave half a server running.
	for _, endpoint := range endpoints {
		listener, err := net.Listen("tcp", endpoint.Addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", endpoint.Name, err)
		}
		listeners = append(listeners, listener)
		servers = append(servers, &http.Server{
			Handler:           endpoint.Handler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    16 << 10,
			BaseContext:       func(net.Listener) context.Context { return ctx },
		})
	}

	results := make(chan error, len(servers))
	for i, server := range servers {
		logger.Info("http listening", "name", endpoints[i].Name, "address", listeners[i].Addr().String())
		go func() { results <- server.Serve(listeners[i]) }()
	}
	var runErr error
	select {
	case <-ctx.Done():
	case err := <-results:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, server := range servers {
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			runErr = errors.Join(runErr, err)
		}
	}
	return runErr
}

// Check is also used by container health checks; the runtime image needs no shell or curl.
func Check(url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("health request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned HTTP %d", response.StatusCode)
	}
	return nil
}
