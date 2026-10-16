package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"onec/internal/config"
	"onec/internal/httpjson"
	"onec/internal/httpserver"
	"onec/internal/mobius/httpapi"
	"onec/internal/mobius/postgres"
	"onec/internal/mobius/service"
	"onec/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("mobius stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "healthcheck" && len(os.Args) == 3 {
		return httpserver.Check(os.Args[2])
	}
	if (command != "serve" && command != "migrate") || len(os.Args) > 2 {
		return fmt.Errorf("usage: mobius [serve|migrate|healthcheck URL]")
	}
	cfg, err := config.LoadMobius()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pool, err := postgres.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if command == "migrate" {
		if err := migrations.Apply(startupCtx, pool); err != nil {
			return err
		}
		logger.Info("migrations applied")
		return nil
	}
	if err := migrations.Check(startupCtx, pool); err != nil {
		return err
	}
	store := postgres.NewIdentityStore(pool)
	release, err := store.LockServer(startupCtx)
	if err != nil {
		return err
	}
	defer release()
	identities := service.NewIdentity(store)
	if err := identities.ResetPresence(startupCtx); err != nil {
		return err
	}
	cancel()
	workerCtx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	done := make(chan struct{})
	go func() {
		identities.RunPresence(workerCtx, logger)
		close(done)
	}()
	ready := func(ctx context.Context) error {
		if err := store.CheckServer(ctx); err != nil {
			return err
		}
		return migrations.Check(ctx, pool)
	}
	guard := func(handler http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				if err := store.CheckServer(r.Context()); err != nil {
					httpjson.Error(w, http.StatusServiceUnavailable, "unavailable", "Server temporarily unavailable")
					return
				}
			}
			handler.ServeHTTP(w, r)
		})
	}
	err = httpserver.Run(ctx, logger,
		httpserver.Endpoint{Name: "mobius-ui", Addr: cfg.UIAddr, Handler: guard(httpapi.Mobius(ready, identities))},
		httpserver.Endpoint{Name: "loki-api", Addr: cfg.APIAddr, Handler: guard(httpapi.Loki(ready, identities))},
	)
	cancelWorker()
	<-done
	return err
}
