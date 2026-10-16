package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
)

type Mobius struct {
	DatabaseURL string
	UIAddr      string
	APIAddr     string
	LogLevel    slog.Level
}

type Loki struct {
	UIAddr   string
	LogLevel slog.Level
}

func LoadMobius() (Mobius, error) {
	c := Mobius{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		UIAddr:      env("MOBIUS_LISTEN_ADDR", "127.0.0.1:8080"),
		APIAddr:     env("LOKI_API_LISTEN_ADDR", "0.0.0.0:8081"),
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if err := validateAddr(c.UIAddr, true); err != nil {
		return c, fmt.Errorf("MOBIUS_LISTEN_ADDR: %w", err)
	}
	if err := validateAddr(c.APIAddr, false); err != nil {
		return c, fmt.Errorf("LOKI_API_LISTEN_ADDR: %w", err)
	}
	err := c.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "INFO")))
	return c, err
}

func LoadLoki() (Loki, error) {
	c := Loki{UIAddr: env("LOKI_UI_LISTEN_ADDR", "127.0.0.1:8082")}
	if err := validateAddr(c.UIAddr, true); err != nil {
		return c, fmt.Errorf("LOKI_UI_LISTEN_ADDR: %w", err)
	}
	err := c.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "INFO")))
	return c, err
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func validateAddr(addr string, local bool) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("expected host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if local {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("UI must listen on a loopback IP address")
		}
	}
	return nil
}
