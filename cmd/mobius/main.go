package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"onec/internal/config"
	"onec/internal/httpserver"
	"onec/internal/mobius/httpapi"
	"onec/internal/mobius/postgres"
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
	cancel()
	ready := func(ctx context.Context) error { return migrations.Check(ctx, pool) }
	return httpserver.Run(ctx, logger,
		httpserver.Endpoint{Name: "mobius-ui", Addr: cfg.UIAddr, Handler: httpapi.Mobius(ready)},
		httpserver.Endpoint{Name: "loki-api", Addr: cfg.APIAddr, Handler: httpapi.Loki(ready)},
	)
}
