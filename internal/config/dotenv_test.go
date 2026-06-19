package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReadsDotEnvLocal(t *testing.T) {
	dir := t.TempDir()
	testChdir(t, dir)
	restoreEnv(t,
		"HELIOS_API_TOKEN",
		"HELIOS_DB_PATH",
		"HELIOS_OUTPUT_ROOT",
		"GH_TOKEN",
		"GITHUB_TOKEN",
		"HELIOS_GITHUB_PUBLISH_BRANCH",
	)
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(`
HELIOS_API_TOKEN=api-token
HELIOS_DB_PATH=/tmp/helios.db
HELIOS_OUTPUT_ROOT=/tmp/helios-outputs
GH_TOKEN=github-token
HELIOS_GITHUB_PUBLISH_BRANCH=feature/test
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
}

func TestDotEnvLocalOverridesDotEnvButNotProcessEnv(t *testing.T) {
	dir := t.TempDir()
	testChdir(t, dir)
	restoreEnv(t, "GH_TOKEN", "HELIOS_GITHUB_PUBLISH_REPO")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GH_TOKEN=base-token\nHELIOS_GITHUB_PUBLISH_REPO=base-repo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte("GH_TOKEN=local-token\nHELIOS_GITHUB_PUBLISH_REPO=local-repo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("GH_TOKEN", "process-token"); err != nil {
		t.Fatal(err)
	}

	loadDotEnvFiles(".env", ".env.local")
	if got := os.Getenv("GH_TOKEN"); got != "process-token" {
		t.Fatalf("GH_TOKEN = %q, want process env precedence", got)
	}
	if got := os.Getenv("HELIOS_GITHUB_PUBLISH_REPO"); got != "local-repo" {
		t.Fatalf("HELIOS_GITHUB_PUBLISH_REPO = %q, want .env.local override", got)
	}
}

func TestLoadRejectsEnabledPreLumosWithoutSeedRoot(t *testing.T) {
	dir := t.TempDir()
	testChdir(t, dir)
	restoreEnv(t,
		"HELIOS_API_TOKEN",
		"HELIOS_DB_PATH",
		"HELIOS_OUTPUT_ROOT",
		"HELIOS_PRE_LUMOS_ENABLED",
		"HELIOS_PRE_LUMOS_SEED_ROOT",
		"OPENAI_API_KEY",
	)
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(`
HELIOS_API_TOKEN=api-token
HELIOS_DB_PATH=/tmp/helios.db
HELIOS_OUTPUT_ROOT=/tmp/helios-outputs
HELIOS_PRE_LUMOS_ENABLED=true
OPENAI_API_KEY=test-key
`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil || err.Error() != "HELIOS_PRE_LUMOS_SEED_ROOT is required when HELIOS_PRE_LUMOS_ENABLED=true" {
		t.Fatalf("Load error = %v, want missing seed root", err)
	}
}

func TestLoadRejectsECWExportTokenEqualAPIToken(t *testing.T) {
	dir := t.TempDir()
	testChdir(t, dir)
	restoreEnv(t,
		"HELIOS_API_TOKEN",
		"HELIOS_ECW_EXPORT_TOKEN",
		"HELIOS_DB_PATH",
		"HELIOS_OUTPUT_ROOT",
	)
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(`
HELIOS_API_TOKEN=same-token
HELIOS_ECW_EXPORT_TOKEN=same-token
HELIOS_DB_PATH=/tmp/helios.db
HELIOS_OUTPUT_ROOT=/tmp/helios-outputs
`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil || err.Error() != "HELIOS_ECW_EXPORT_TOKEN must differ from HELIOS_API_TOKEN" {
		t.Fatalf("Load error = %v, want distinct ECW token requirement", err)
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
