package lumoskit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerUsesLumoskitRepoRootForBinaryUnderBin(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin", "lumoskit")
	writeCwdLumoskit(t, bin)

	outputRoot := filepath.Join(t.TempDir(), "out")
	res := (&Runner{Binary: bin}).Run(context.Background(), "ethereum", strings.Repeat("0", 64), outputRoot)

	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.ExitCode, string(res.Stderr))
	}
	got := strings.TrimSpace(mustRead(t, filepath.Join(outputRoot, "cwd.txt")))
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat lumoskit cwd %q: %v", got, err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat repo root %q: %v", root, err)
	}
	if !os.SameFile(gotInfo, rootInfo) {
		t.Fatalf("lumoskit cwd = %q, want repo root %q", got, root)
	}
	if res.SummaryMissing {
		t.Fatal("summary should have been read")
	}
}

func TestRunnerReadsProductRunSummaryBeforeLegacySummary(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin", "lumoskit")
	writeBundleLumoskit(t, bin)

	outputRoot := filepath.Join(t.TempDir(), "out")
	res := (&Runner{Binary: bin}).Run(context.Background(), "ethereum", strings.Repeat("0", 64), outputRoot)

	wantPath := filepath.Join(outputRoot, "report_bundle", "report", "run_summary.json")
	if res.SummaryPath != wantPath {
		t.Fatalf("summary path = %q, want %q", res.SummaryPath, wantPath)
	}
	if got := strings.TrimSpace(string(res.SummaryBytes)); got != `{"status":"pass","poc":{"status":"verified"}}` {
		t.Fatalf("summary bytes = %s", got)
	}
	if res.SummaryMissing {
		t.Fatal("summary should have been read")
	}
}

func TestRunnerDoesNotChangeCwdForNonBinHelper(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "scripts", "fake-lumoskit.sh")
	writeCwdLumoskit(t, helper)

	processCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outputRoot := filepath.Join(t.TempDir(), "out")
	res := (&Runner{Binary: helper}).Run(context.Background(), "ethereum", strings.Repeat("0", 64), outputRoot)

	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.ExitCode, string(res.Stderr))
	}
	got := strings.TrimSpace(mustRead(t, filepath.Join(outputRoot, "cwd.txt")))
	if got != processCwd {
		t.Fatalf("helper cwd = %q, want inherited process cwd %q", got, processCwd)
	}
}

func TestRunnerPassesRCAStageFlag(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "scripts", "fake-lumoskit.sh")
	writeStageCaptureLumoskit(t, helper)

	outputRoot := filepath.Join(t.TempDir(), "out")
	res := (&Runner{Binary: helper}).RunWithOptions(context.Background(), "ethereum", strings.Repeat("0", 64), outputRoot, RunOptions{Stage: "rca"})

	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.ExitCode, string(res.Stderr))
	}
	got := strings.TrimSpace(mustRead(t, filepath.Join(outputRoot, "stages.txt")))
	if got != "rca" {
		t.Fatalf("stages = %q, want rca", got)
	}
}

func TestRunnerRunsAgentPoCThenRCAForPoCResume(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "scripts", "fake-lumoskit.sh")
	writeStageCaptureLumoskit(t, helper)

	outputRoot := filepath.Join(t.TempDir(), "out")
	res := (&Runner{Binary: helper}).RunWithOptions(context.Background(), "ethereum", strings.Repeat("0", 64), outputRoot, RunOptions{Stage: "agent_poc"})

	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.ExitCode, string(res.Stderr))
	}
	got := strings.TrimSpace(mustRead(t, filepath.Join(outputRoot, "stages.txt")))
	if got != "agent_poc\nrca" {
		t.Fatalf("stages = %q, want agent_poc then rca", got)
	}
}

func TestRunnerRunsAgentPoCRepairThenRCAForReachablePoCResume(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "scripts", "fake-lumoskit.sh")
	writeStageCaptureLumoskit(t, helper)

	outputRoot := filepath.Join(t.TempDir(), "out")
	res := (&Runner{Binary: helper}).RunWithOptions(context.Background(), "ethereum", strings.Repeat("0", 64), outputRoot, RunOptions{Stage: "agent_poc_repair"})

	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.ExitCode, string(res.Stderr))
	}
	got := strings.TrimSpace(mustRead(t, filepath.Join(outputRoot, "stages.txt")))
	if got != "agent_poc_repair\nrca" {
		t.Fatalf("stages = %q, want agent_poc_repair then rca", got)
	}
}

func writeCwdLumoskit(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-root) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$out"
pwd > "$out/cwd.txt"
printf '{"status":"pass"}\n' > "$out/summary.json"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeBundleLumoskit(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-root) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$out/report_bundle/report"
printf '{"status":"fail","poc":{"status":"missing"}}\n' > "$out/summary.json"
printf '{"status":"pass","poc":{"status":"verified"}}\n' > "$out/report_bundle/report/run_summary.json"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeStageCaptureLumoskit(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env sh
set -eu
out=""
stage="all"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --stage) stage="$2"; shift 2 ;;
    --output-root) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$out/report_bundle/report"
printf '%s\n' "$stage" >> "$out/stages.txt"
printf '{"status":"pass","poc":{"status":"verified"},"rca":{"status":"complete"}}\n' > "$out/report_bundle/report/run_summary.json"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
