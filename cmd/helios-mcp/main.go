package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/artifacts"
	"github.com/UPside-Lumos-V2/helios/internal/heliosclient"
	"github.com/UPside-Lumos-V2/helios/internal/mcpbridge"
	"github.com/UPside-Lumos-V2/helios/internal/mcpserver"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reader, err := artifacts.NewReader(cfg.OutputBase, cfg.MaxBytes)
	if err != nil {
		logger.Error("artifact reader init failed", "err", err)
		os.Exit(2)
	}
	client, closeClient, err := buildClient(ctx, cfg)
	if err != nil {
		logger.Error("mcp client init failed", "err", err)
		os.Exit(2)
	}
	if closeClient != nil {
		defer closeClient()
	}

	logger.Info("helios-mcp starting", "source", cfg.Source(), "base_url", cfg.HeliosBaseURL, "bridge_db", cfg.BridgeDBPath, "output_base", cfg.OutputBase, "max_bytes", cfg.MaxBytes)
	srv := &mcpserver.Server{Client: client, Artifacts: reader, Logger: logger}
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		logger.Error("mcp server exited", "err", err)
		os.Exit(1)
	}
}

type config struct {
	HeliosBaseURL  string
	HeliosAPIToken string
	BridgeDBPath   string
	OutputBase     string
	MaxBytes       int64
	HTTPTimeout    time.Duration
}

func loadConfig() (*config, error) {
	cfg := &config{
		HeliosBaseURL:  os.Getenv("HELIOS_BASE_URL"),
		HeliosAPIToken: os.Getenv("HELIOS_API_TOKEN"),
		BridgeDBPath:   os.Getenv("HELIOS_MCP_BRIDGE_DB_PATH"),
		OutputBase:     os.Getenv("HELIOS_OUTPUT_BASE"),
		MaxBytes:       envInt64("HELIOS_MCP_MAX_BYTES", artifacts.DefaultMaxBytes),
		HTTPTimeout:    time.Duration(envInt64("HELIOS_MCP_HTTP_TIMEOUT_SECONDS", 30)) * time.Second,
	}
	if cfg.BridgeDBPath == "" {
		if cfg.HeliosBaseURL == "" {
			return nil, fmt.Errorf("HELIOS_BASE_URL is required unless HELIOS_MCP_BRIDGE_DB_PATH is set")
		}
		if cfg.HeliosAPIToken == "" {
			return nil, fmt.Errorf("HELIOS_API_TOKEN is required unless HELIOS_MCP_BRIDGE_DB_PATH is set")
		}
	}
	if cfg.OutputBase == "" {
		return nil, fmt.Errorf("HELIOS_OUTPUT_BASE is required")
	}
	return cfg, nil
}

func (c *config) Source() string {
	if c.BridgeDBPath != "" {
		return "bridge"
	}
	return "helios-api"
}

func buildClient(ctx context.Context, cfg *config) (mcpserver.HeliosClient, func() error, error) {
	if cfg.BridgeDBPath != "" {
		st, err := mcpbridge.OpenReadOnly(ctx, cfg.BridgeDBPath)
		if err != nil {
			return nil, nil, err
		}
		return st, st.Close, nil
	}
	httpClient := &http.Client{Timeout: cfg.HTTPTimeout}
	hc, err := heliosclient.New(cfg.HeliosBaseURL, cfg.HeliosAPIToken, httpClient)
	if err != nil {
		return nil, nil, err
	}
	return hc, nil, nil
}

func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}
