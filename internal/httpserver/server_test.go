package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
)

func TestPortConflictClosesEarlierListeners(t *testing.T) {
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := first.Addr().String()
	_ = first.Close()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Run(context.Background(), logger,
		Endpoint{Name: "first", Addr: addr},
		Endpoint{Name: "occupied", Addr: occupied.Addr().String()},
	); err == nil {
		t.Fatal("port conflict was ignored")
	}
	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("first listener was leaked: %v", err)
	}
	_ = rebound.Close()
}
