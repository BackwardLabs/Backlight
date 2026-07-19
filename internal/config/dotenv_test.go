package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReadsDotEnvLocal(t *testing.T) {
	dir := t.TempDir()
	testChdir(t, dir)
	restoreEnv(t,
		"BACKLIGHT_API_TOKEN",
		"BACKLIGHT_DB_PATH",
		"BACKLIGHT_OUTPUT_ROOT",
		"GH_TOKEN",
		"GITHUB_TOKEN",
		"BACKLIGHT_GITHUB_PUBLISH_BRANCH",
		"BACKLIGHT_X_MCP_ENABLED",
		"BACKLIGHT_X_MCP_URL",
		"BACKLIGHT_X_MCP_BEARER_TOKEN",
		"X_BEARER_TOKEN",
		"BACKLIGHT_X_MCP_COMMAND",
		"BACKLIGHT_X_MCP_ARGS",
		"BACKLIGHT_X_MCP_TIMEOUT_SECONDS",
	)
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(`
BACKLIGHT_API_TOKEN=api-token
BACKLIGHT_DB_PATH=/tmp/helios.db
BACKLIGHT_OUTPUT_ROOT=/tmp/helios-outputs
GH_TOKEN=github-token
BACKLIGHT_GITHUB_PUBLISH_BRANCH=feature/test
BACKLIGHT_X_MCP_ENABLED=true
BACKLIGHT_X_MCP_URL=https://x-mcp.example/mcp
BACKLIGHT_X_MCP_BEARER_TOKEN=mcp-bearer
BACKLIGHT_X_MCP_COMMAND=/usr/local/bin/xurl
BACKLIGHT_X_MCP_ARGS="--app backlight mcp https://api.x.com/mcp"
BACKLIGHT_X_MCP_TIMEOUT_SECONDS=31
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubToken != "github-token" {
		t.Fatalf("GitHubToken = %q, want loaded token", cfg.GitHubToken)
	}
	if cfg.GitHubBranch != "feature/test" {
		t.Fatalf("GitHubBranch = %q, want feature/test", cfg.GitHubBranch)
	}
	if !cfg.XMCPEnabled || cfg.XMCPCommand != "/usr/local/bin/xurl" || cfg.XMCPTimeoutSeconds != 31 {
		t.Fatalf("X MCP config = enabled:%v command:%q timeout:%d", cfg.XMCPEnabled, cfg.XMCPCommand, cfg.XMCPTimeoutSeconds)
	}
	if cfg.XMCPURL != "https://x-mcp.example/mcp" || cfg.XMCPBearerToken != "mcp-bearer" {
		t.Fatalf("X MCP HTTP config = url:%q bearer_set:%v", cfg.XMCPURL, cfg.XMCPBearerToken != "")
	}
	if got := strings.Join(cfg.XMCPArgs, "|"); got != "--app|backlight|mcp|https://api.x.com/mcp" {
		t.Fatalf("XMCPArgs = %q", got)
	}
}

func TestDotEnvLocalOverridesDotEnvButNotProcessEnv(t *testing.T) {
	dir := t.TempDir()
	testChdir(t, dir)
	restoreEnv(t, "GH_TOKEN", "BACKLIGHT_GITHUB_PUBLISH_REPO")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GH_TOKEN=base-token\nBACKLIGHT_GITHUB_PUBLISH_REPO=base-repo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte("GH_TOKEN=local-token\nBACKLIGHT_GITHUB_PUBLISH_REPO=local-repo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("GH_TOKEN", "process-token"); err != nil {
		t.Fatal(err)
	}

	loadDotEnvFiles(".env", ".env.local")
	if got := os.Getenv("GH_TOKEN"); got != "process-token" {
		t.Fatalf("GH_TOKEN = %q, want process env precedence", got)
	}
	if got := os.Getenv("BACKLIGHT_GITHUB_PUBLISH_REPO"); got != "local-repo" {
		t.Fatalf("BACKLIGHT_GITHUB_PUBLISH_REPO = %q, want .env.local override", got)
	}
}

func TestLoadRejectsEnabledPreLumosWithoutSeedRoot(t *testing.T) {
	dir := t.TempDir()
	testChdir(t, dir)
	restoreEnv(t,
		"BACKLIGHT_API_TOKEN",
		"BACKLIGHT_DB_PATH",
		"BACKLIGHT_OUTPUT_ROOT",
		"BACKLIGHT_PRE_LUMOS_ENABLED",
		"BACKLIGHT_PRE_LUMOS_SEED_ROOT",
	)
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(`
BACKLIGHT_API_TOKEN=api-token
BACKLIGHT_DB_PATH=/tmp/helios.db
BACKLIGHT_OUTPUT_ROOT=/tmp/helios-outputs
BACKLIGHT_PRE_LUMOS_ENABLED=true
`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil || err.Error() != "BACKLIGHT_PRE_LUMOS_SEED_ROOT is required when BACKLIGHT_PRE_LUMOS_ENABLED=true" {
		t.Fatalf("Load error = %v, want missing seed root", err)
	}
}

func TestLoadAllowsPreLumosWithCodexAuthAndNoOpenAIAPIKey(t *testing.T) {
	dir := t.TempDir()
	testChdir(t, dir)
	restoreEnv(t,
		"BACKLIGHT_API_TOKEN",
		"BACKLIGHT_DB_PATH",
		"BACKLIGHT_OUTPUT_ROOT",
		"BACKLIGHT_PRE_LUMOS_ENABLED",
		"BACKLIGHT_PRE_LUMOS_SEED_ROOT",
		"OPENAI_API_KEY",
	)
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(`
BACKLIGHT_API_TOKEN=api-token
BACKLIGHT_DB_PATH=/tmp/helios.db
BACKLIGHT_OUTPUT_ROOT=/tmp/helios-outputs
BACKLIGHT_PRE_LUMOS_ENABLED=true
BACKLIGHT_PRE_LUMOS_SEED_ROOT=/tmp/pre-lumos-seed
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PreLumosEnabled || cfg.PreLumosSeedRoot != "/tmp/pre-lumos-seed" {
		t.Fatalf("Pre-Lumos config = enabled:%v seed_root:%q", cfg.PreLumosEnabled, cfg.PreLumosSeedRoot)
	}
}

func TestLoadRejectsECWExportTokenEqualAPIToken(t *testing.T) {
	dir := t.TempDir()
	testChdir(t, dir)
	restoreEnv(t,
		"BACKLIGHT_API_TOKEN",
		"BACKLIGHT_ECW_EXPORT_TOKEN",
		"BACKLIGHT_DB_PATH",
		"BACKLIGHT_OUTPUT_ROOT",
	)
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(`
BACKLIGHT_API_TOKEN=same-token
BACKLIGHT_ECW_EXPORT_TOKEN=same-token
BACKLIGHT_DB_PATH=/tmp/helios.db
BACKLIGHT_OUTPUT_ROOT=/tmp/helios-outputs
`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil || err.Error() != "BACKLIGHT_ECW_EXPORT_TOKEN must differ from BACKLIGHT_API_TOKEN" {
		t.Fatalf("Load error = %v, want distinct ECW token requirement", err)
	}
}

func TestLoadDoesNotAcceptLegacyHeliosEnv(t *testing.T) {
	dir := t.TempDir()
	testChdir(t, dir)
	restoreEnv(t,
		"BACKLIGHT_API_TOKEN",
		"BACKLIGHT_DB_PATH",
		"BACKLIGHT_OUTPUT_ROOT",
		"HELIOS_API_TOKEN",
		"HELIOS_DB_PATH",
		"HELIOS_OUTPUT_ROOT",
	)
	t.Setenv("HELIOS_API_TOKEN", "legacy-token")
	t.Setenv("HELIOS_DB_PATH", "/tmp/legacy.db")
	t.Setenv("HELIOS_OUTPUT_ROOT", "/tmp/legacy-outputs")

	_, err := Load()
	if err == nil || err.Error() != "BACKLIGHT_API_TOKEN is required" {
		t.Fatalf("Load error = %v, want legacy namespace rejected", err)
	}
}

func testChdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(old)
	})
}

func restoreEnv(t *testing.T, keys ...string) {
	t.Helper()
	type saved struct {
		value string
		ok    bool
	}
	original := make(map[string]saved, len(keys))
	for _, key := range keys {
		v, ok := os.LookupEnv(key)
		original[key] = saved{value: v, ok: ok}
		_ = os.Unsetenv(key)
	}
	t.Cleanup(func() {
		for _, key := range keys {
			if original[key].ok {
				_ = os.Setenv(key, original[key].value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	})
}
