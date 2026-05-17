package main

import (
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaultsDirectModeFromHeliosEnv(t *testing.T) {
	t.Setenv("HELIOS_LISTEN_ADDR", "127.0.0.1:18080")
	t.Setenv("HELIOS_API_TOKEN", "test-token")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HeliosBaseURL != "http://127.0.0.1:18080" {
		t.Fatalf("HeliosBaseURL = %q, want derived listen addr", cfg.HeliosBaseURL)
	}
	if cfg.OutputBase != "" {
		t.Fatalf("OutputBase = %q, want empty for direct API artifact mode", cfg.OutputBase)
	}
}

func TestLoadConfigDefaultsBridgeOutputBaseFromBridgeDB(t *testing.T) {
	dataDir := t.TempDir()
	bridgeDB := filepath.Join(dataDir, "helios-mcp-bridge.db")
	t.Setenv("HELIOS_MCP_BRIDGE_DB_PATH", bridgeDB)

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
