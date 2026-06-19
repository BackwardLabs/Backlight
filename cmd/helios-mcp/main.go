package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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

	client, closeClient, err := buildClient(ctx, cfg)
	if err != nil {
		logger.Error("mcp client init failed", "err", err)
		os.Exit(2)
	}
	if closeClient != nil {
		defer closeClient()
	}

	var reader mcpserver.ArtifactReader
	if cfg.OutputBase != "" {
		reader, err = artifacts.NewReader(cfg.OutputBase, cfg.MaxBytes)
		if err != nil {
			logger.Error("artifact reader init failed", "err", err)
			os.Exit(2)
		}
	}

	logger.Info("backlight-mcp starting", "source", cfg.Source(), "base_url", cfg.BacklightBaseURL, "bridge_db", cfg.BridgeDBPath, "output_base", cfg.OutputBase, "max_bytes", cfg.MaxBytes, "listen_addr", cfg.MCPListenAddr, "mcp_path", cfg.MCPPath)
	srv := &mcpserver.Server{Client: client, Artifacts: reader, Logger: logger}
	if cfg.MCPListenAddr != "" {
		httpSrv := &http.Server{
			Addr:    cfg.MCPListenAddr,
			Handler: srv.Handler(ctx, mcpserver.HTTPOptions{Path: cfg.MCPPath, BearerToken: cfg.MCPHTTPToken}),
		}
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutdownCtx)
		}()
		logger.Info("backlight-mcp http listening", "addr", cfg.MCPListenAddr, "path", cfg.MCPPath, "auth", cfg.MCPHTTPToken != "")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed && ctx.Err() == nil {
			logger.Error("mcp http server exited", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		logger.Error("mcp server exited", "err", err)
		os.Exit(1)
	}
}

type config struct {
	BacklightBaseURL  string
	BacklightAPIToken string
	BridgeDBPath      string
	OutputBase        string
	MaxBytes          int64
	HTTPTimeout       time.Duration
	MCPListenAddr     string
	MCPPath           string
	MCPHTTPToken      string
}

func loadConfig() (*config, error) {
	cfg := &config{
		BacklightBaseURL:  firstNonEmpty(os.Getenv("HELIOS_BASE_URL"), baseURLFromListenAddr(os.Getenv("HELIOS_LISTEN_ADDR"))),
		BacklightAPIToken: os.Getenv("HELIOS_API_TOKEN"),
		BridgeDBPath:      os.Getenv("HELIOS_MCP_BRIDGE_DB_PATH"),
		OutputBase:        outputBaseFromEnv(os.Getenv("HELIOS_OUTPUT_BASE"), os.Getenv("HELIOS_OUTPUT_ROOT"), os.Getenv("HELIOS_MCP_BRIDGE_DB_PATH")),
		MaxBytes:          envInt64("HELIOS_MCP_MAX_BYTES", artifacts.DefaultMaxBytes),
		HTTPTimeout:       time.Duration(envInt64("HELIOS_MCP_HTTP_TIMEOUT_SECONDS", 30)) * time.Second,
		MCPListenAddr:     strings.TrimSpace(os.Getenv("HELIOS_MCP_LISTEN_ADDR")),
		MCPPath:           envDefault("HELIOS_MCP_PATH", "/mcp"),
		MCPHTTPToken:      firstNonEmpty(os.Getenv("HELIOS_MCP_HTTP_TOKEN"), os.Getenv("HELIOS_API_TOKEN")),
	}
	if cfg.BridgeDBPath == "" {
		if cfg.BacklightBaseURL == "" {
			return nil, fmt.Errorf("HELIOS_BASE_URL is required unless HELIOS_MCP_BRIDGE_DB_PATH is set")
		}
		if cfg.BacklightAPIToken == "" {
			return nil, fmt.Errorf("HELIOS_API_TOKEN is required unless HELIOS_MCP_BRIDGE_DB_PATH is set")
		}
	}
	if cfg.BridgeDBPath != "" && cfg.OutputBase == "" {
		return nil, fmt.Errorf("HELIOS_OUTPUT_BASE or HELIOS_OUTPUT_ROOT is required for bridge mode")
	}
	if cfg.MCPListenAddr != "" && cfg.MCPHTTPToken == "" {
		return nil, fmt.Errorf("HELIOS_MCP_HTTP_TOKEN is required for HTTP mode unless HELIOS_API_TOKEN is set")
	}
	return cfg, nil
}

func (c *config) Source() string {
	if c.BridgeDBPath != "" {
		return "bridge"
	}
	return "backlight-api"
}

func buildClient(ctx context.Context, cfg *config) (mcpserver.BacklightClient, func() error, error) {
	if cfg.BridgeDBPath != "" {
		st, err := mcpbridge.OpenReadOnly(ctx, cfg.BridgeDBPath)
		if err != nil {
			return nil, nil, err
		}
		return st, st.Close, nil
	}
	httpClient := &http.Client{Timeout: cfg.HTTPTimeout}
	hc, err := heliosclient.New(cfg.BacklightBaseURL, cfg.BacklightAPIToken, httpClient)
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

func outputBaseFromEnv(explicitBase, outputRoot, bridgeDBPath string) string {
	if explicitBase = strings.TrimSpace(explicitBase); explicitBase != "" {
		return explicitBase
	}
	if outputRoot = strings.TrimSpace(outputRoot); outputRoot != "" {
		return outputRoot
	}
	if bridgeDBPath = strings.TrimSpace(bridgeDBPath); bridgeDBPath != "" {
		return filepath.Join(filepath.Dir(bridgeDBPath), "outputs")
	}
	return ""
}

func baseURLFromListenAddr(listenAddr string) string {
	listenAddr = strings.TrimSpace(listenAddr)
	if listenAddr == "" {
		return ""
	}
	if strings.HasPrefix(listenAddr, ":") {
		return "http://127.0.0.1" + listenAddr
	}
	return "http://" + listenAddr
}

func envDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
