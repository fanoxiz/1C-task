package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"onec/internal/config"
	"onec/internal/httpserver"
	"onec/internal/loki/httpui"
)

func main() {
	if err := run(); err != nil {
		slog.Error("loki stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) == 3 && os.Args[1] == "healthcheck" {
		return httpserver.Check(os.Args[2])
	}
	if len(os.Args) > 1 && (len(os.Args) != 2 || os.Args[1] != "serve") {
		return fmt.Errorf("usage: loki [serve|healthcheck URL]")
	}
	cfg, err := config.LoadLoki()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return httpserver.Run(ctx, logger, httpserver.Endpoint{Name: "loki-ui", Addr: cfg.UIAddr, Handler: httpui.Handler()})
}
