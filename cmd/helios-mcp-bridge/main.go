package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/mcpbridge"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := mcpbridge.Open(ctx, cfg.DBPath)
	if err != nil {
		logger.Error("bridge store open failed", "err", err, "db_path", cfg.DBPath)
		os.Exit(2)
	}
	defer st.Close()

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           (&mcpbridge.Server{Store: st, Token: cfg.Token, Logger: logger}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("backlight-mcp-bridge listening", "addr", cfg.ListenAddr, "db_path", cfg.DBPath, "auth_required", cfg.Token != "")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server exited", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown failed", "err", err)
	}
	logger.Info("backlight-mcp-bridge stopped")
}

type config struct {
	ListenAddr    string
	DBPath        string
	Token         string
	AllowInsecure bool
}

func loadConfig() (*config, error) {
	cfg := &config{
		ListenAddr:    envDefault("HELIOS_MCP_BRIDGE_LISTEN_ADDR", "127.0.0.1:9090"),
		DBPath:        os.Getenv("HELIOS_MCP_BRIDGE_DB_PATH"),
		Token:         os.Getenv("HELIOS_MCP_BRIDGE_TOKEN"),
		AllowInsecure: envBool("HELIOS_MCP_BRIDGE_ALLOW_INSECURE", false),
	}
	if cfg.DBPath == "" {
		return nil, fmt.Errorf("HELIOS_MCP_BRIDGE_DB_PATH is required")
	}
	if cfg.Token == "" && !cfg.AllowInsecure {
		return nil, fmt.Errorf("HELIOS_MCP_BRIDGE_TOKEN is required unless HELIOS_MCP_BRIDGE_ALLOW_INSECURE=true")
	}
	return cfg, nil
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	switch os.Getenv(key) {
	case "1", "true", "TRUE", "yes", "YES", "on", "ON":
		return true
	case "0", "false", "FALSE", "no", "NO", "off", "OFF":
		return false
	default:
		return def
	}
}
