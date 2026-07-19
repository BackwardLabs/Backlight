package prelumos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerConfiguredRequiresEnabledAndSeedRoot(t *testing.T) {
	if (&Runner{}).Configured() {
		t.Fatal("empty runner should not be configured")
	}
	if (&Runner{Enabled: true}).Configured() {
		t.Fatal("runner without seed root should not be configured")
	}
	if !(&Runner{Enabled: true, SeedRoot: "/tmp/seed-root"}).Configured() {
		t.Fatal("enabled runner with seed root should be configured")
	}
}

func TestRunnerInvokesSidecarAndReadsStatus(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "sidecar.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
status=""
output=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--status-path" ]; then
    shift
    status="$1"
  fi
  if [ "$1" = "--output-path" ]; then
    shift
    output="$1"
  fi
  shift
done
printf '[{"slug":"demo-protocol"}]\n' > "$output"
printf '{"row_count":1,"slugs":["demo-protocol"],"target_files":["/tmp/import_2026.json"],"output_path":"%s","dry_run":false}\n' "$output" > "$status"
printf 'ok'
`), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Runner{
		Enabled:   true,
		PythonBin: "/bin/sh",
		Script:    script,
		SkillDir:  filepath.Join(dir, "skill"),
		SeedRoot:  filepath.Join(dir, "seed-root"),
	}
	res, err := r.Run(context.Background(), Case{
		CaseID:     "case_123",
		OutputRoot: filepath.Join(dir, "output"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RowCount != 1 {
		t.Fatalf("RowCount = %d, want 1", res.RowCount)
	}
	if got := strings.Join(res.Slugs, ","); got != "demo-protocol" {
		t.Fatalf("Slugs = %q, want demo-protocol", got)
	}
	if len(res.Stdout) == 0 || string(res.Stdout) != "ok" {
		t.Fatalf("Stdout = %q, want ok", string(res.Stdout))
	}
	if _, err := os.Stat(res.OutputPath); err != nil {
		t.Fatalf("OutputPath was not written: %v", err)
	}
}
