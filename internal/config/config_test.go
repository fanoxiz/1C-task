package config

import (
	"log/slog"
	"testing"
)

func TestLocalListenersRejectPublicAddresses(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "[::]:8080", "192.168.1.2:8080", "127.0.0.1:0", "127.0.0.1:bad"} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv("MOBIUS_LISTEN_ADDR", addr)
			if _, err := LoadMobius(); err == nil {
				t.Fatal("public or invalid Mobius UI address accepted")
			}
			t.Setenv("LOKI_UI_LISTEN_ADDR", addr)
			if _, err := LoadLoki(); err == nil {
				t.Fatal("public or invalid Loki UI address accepted")
			}
		})
	}
}

func TestConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("MOBIUS_LISTEN_ADDR", "[::1]:8080")
	t.Setenv("LOKI_API_LISTEN_ADDR", "0.0.0.0:8081")
	t.Setenv("LOKI_UI_LISTEN_ADDR", "127.0.0.1:8082")
	t.Setenv("LOG_LEVEL", "DEBUG")
	mobius, err := LoadMobius()
	if err != nil {
		t.Fatal(err)
	}
	loki, err := LoadLoki()
	if err != nil {
		t.Fatal(err)
	}
	if mobius.LogLevel != slog.LevelDebug || loki.LogLevel != slog.LevelDebug {
		t.Fatal("log level was not applied")
	}
	t.Setenv("DATABASE_URL", "")
	if _, err := LoadMobius(); err == nil {
		t.Fatal("missing database URL accepted")
	}
	t.Setenv("LOG_LEVEL", "invalid")
	if _, err := LoadLoki(); err == nil {
		t.Fatal("invalid log level accepted")
	}
}
