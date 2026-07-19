package main

import (
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaultsDirectModeFromBacklightLegacyEnv(t *testing.T) {
	t.Setenv("BACKLIGHT_LISTEN_ADDR", "127.0.0.1:18080")
	t.Setenv("BACKLIGHT_API_TOKEN", "test-token")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BacklightBaseURL != "http://127.0.0.1:18080" {
		t.Fatalf("BacklightBaseURL = %q, want derived listen addr", cfg.BacklightBaseURL)
	}
	if cfg.OutputBase != "" {
		t.Fatalf("OutputBase = %q, want empty for direct API artifact mode", cfg.OutputBase)
	}
}

func TestLoadConfigDefaultsBridgeOutputBaseFromBridgeDB(t *testing.T) {
	dataDir := t.TempDir()
	bridgeDB := filepath.Join(dataDir, "helios-mcp-bridge.db")
	t.Setenv("BACKLIGHT_MCP_BRIDGE_DB_PATH", bridgeDB)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dataDir, "outputs")
	if cfg.OutputBase != want {
		t.Fatalf("OutputBase = %q, want %q", cfg.OutputBase, want)
	}
}

func TestOutputBaseExplicitEnvWins(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "safe-output-base")
	outputRoot := filepath.Join(t.TempDir(), "helios-output-root")

	got := outputBaseFromEnv(explicit, outputRoot, "")
	if got != explicit {
		t.Fatalf("outputBaseFromEnv() = %q, want explicit base %q", got, explicit)
	}
}

func TestLoadConfigHTTPModeUsesAPITokenForMCPAuth(t *testing.T) {
	t.Setenv("BACKLIGHT_BASE_URL", "http://127.0.0.1:8080")
	t.Setenv("BACKLIGHT_API_TOKEN", "api-token")
	t.Setenv("BACKLIGHT_MCP_LISTEN_ADDR", "127.0.0.1:8090")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPListenAddr != "127.0.0.1:8090" {
		t.Fatalf("MCPListenAddr = %q", cfg.MCPListenAddr)
	}
	if cfg.MCPPath != "/mcp" {
		t.Fatalf("MCPPath = %q, want /mcp", cfg.MCPPath)
	}
	if cfg.MCPHTTPToken != "api-token" {
		t.Fatalf("MCPHTTPToken = %q, want API token fallback", cfg.MCPHTTPToken)
	}
}

func TestLoadConfigHTTPModeAllowsSeparateMCPToken(t *testing.T) {
	t.Setenv("BACKLIGHT_BASE_URL", "http://127.0.0.1:8080")
	t.Setenv("BACKLIGHT_API_TOKEN", "api-token")
	t.Setenv("BACKLIGHT_MCP_LISTEN_ADDR", "127.0.0.1:8090")
	t.Setenv("BACKLIGHT_MCP_HTTP_TOKEN", "mcp-token")
	t.Setenv("BACKLIGHT_MCP_PATH", "mcp")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPHTTPToken != "mcp-token" {
		t.Fatalf("MCPHTTPToken = %q, want MCP-specific token", cfg.MCPHTTPToken)
	}
	if cfg.MCPPath != "mcp" {
		t.Fatalf("MCPPath = %q, want raw configured path", cfg.MCPPath)
	}
}
