package artifacts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReaderExposesPrettyProductArtifactAliases(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "case_1")
	for _, dir := range []string{
		filepath.Join(root, "report_bundle", "report"),
		filepath.Join(root, "report_bundle", "poc"),
		filepath.Join(root, "report_bundle", "evidence"),
		filepath.Join(root, "report_bundle", "visuals"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "report", "REPORT.md"), []byte("# Incident report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "RCA.md"), []byte("# RCA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "report", "RCA.md"), []byte("# Bundle RCA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "README.md"), []byte("# Bundle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "manifest.json"), []byte(`{"schema":"lumoskit-report-bundle-v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "report", "run_summary.json"), []byte(`{"status":"pass"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "poc", "LumosPoCBase.sol"), []byte("abstract contract LumosPoCBase {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "evidence", "asset_deltas.json"), []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "visuals", "asset_deltas.dot"), []byte("digraph G {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "poc", "PoC.t.sol"), []byte("contract PoC {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "artifacts", "agent_poc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "agent_poc", "attack_flow.md"), []byte("# Attack Flow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "agent_poc", "multi_leg_reconciliation.md"), []byte("# Multi-leg Reconciliation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "agent_poc", "multi_leg_reconciliation.json"), []byte(`{"rows":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "summary.json"), []byte(`{"status":"pass"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "secret.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	reader, err := NewReader(base, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := caseWithRoot(root)

	report, err := reader.Read(c, "REPORT.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Path != "REPORT.md" || report.Text != "# Incident report\n" {
		t.Fatalf("unexpected report read result: %+v", report)
	}
	rca, err := reader.Read(c, "RCA.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rca.Path != "RCA.md" || rca.Text != "# RCA\n" {
		t.Fatalf("unexpected RCA read result: %+v", rca)
	}
	poc, err := reader.Read(c, "PoC.t.sol", 0)
	if err != nil {
		t.Fatal(err)
	}
	if poc.Path != "PoC.t.sol" || poc.Text != "contract PoC {}\n" {
		t.Fatalf("unexpected PoC read result: %+v", poc)
	}
	attackFlow, err := reader.Read(c, "attack_flow.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if attackFlow.Text != "# Attack Flow\n" {
		t.Fatalf("unexpected attack flow: %+v", attackFlow)
	}
	reconciliation, err := reader.Read(c, "multi_leg_reconciliation.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if reconciliation.Text != "# Multi-leg Reconciliation\n" {
		t.Fatalf("unexpected reconciliation artifact: %+v", reconciliation)
	}

	bundleReadme, err := reader.Read(c, "report_bundle/README.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if bundleReadme.Text != "# Bundle\n" {
		t.Fatalf("unexpected bundle README: %+v", bundleReadme)
	}
	runSummary, err := reader.Read(c, "report_bundle/report/run_summary.json", 0)
	if err != nil {
		t.Fatal(err)
	}
	if runSummary.Text != `{"status":"pass"}` {
		t.Fatalf("unexpected run summary: %+v", runSummary)
	}
	pocBase, err := reader.Read(c, "report_bundle/poc/LumosPoCBase.sol", 0)
	if err != nil {
		t.Fatal(err)
	}
	if pocBase.Text != "abstract contract LumosPoCBase {}\n" {
		t.Fatalf("unexpected PoC base: %+v", pocBase)
	}

	for _, path := range []string{"summary.json", "summary.md", "rca.md", "Report.md", "report_bundle/visuals/asset_deltas.png", "artifacts/secret.json", "../RCA.md", "/tmp/RCA.md"} {
		if _, err := reader.Read(c, path, 0); err == nil {
			t.Fatalf("Read(%q) succeeded; want rejected", path)
		}
	}
}

func TestReaderRejectsOutputRootOutsideBase(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "RCA.md"), []byte(`# RCA\n`), 0o644); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(base, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(caseWithRoot(outside), "RCA.md", 0); err == nil {
		t.Fatal("read outside HELIOS_OUTPUT_BASE succeeded; want rejected")
	}
}

func TestReaderRejectsFilesystemRootAsBase(t *testing.T) {
	root := filepath.VolumeName(os.TempDir()) + string(filepath.Separator)
	if _, err := NewReader(root, 0); err == nil {
		t.Fatal("NewReader accepted filesystem root as output base; want rejected")
	}
}

func TestReaderRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "case_1")
	outside := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "RCA.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	reader, err := NewReader(base, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(caseWithRoot(root), "RCA.md", 0); err == nil {
		t.Fatal("symlink escape succeeded; want rejected")
	}
}

func TestReaderRejectsAllowlistedSymlinkToExcludedInRootFile(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "case_1")
	if err := os.MkdirAll(filepath.Join(root, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prompts", "system.txt"), []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "prompts", "system.txt"), filepath.Join(root, "RCA.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	reader, err := NewReader(base, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(caseWithRoot(root), "RCA.md", 0); err == nil {
		t.Fatal("allowlisted symlink to excluded in-root file succeeded; want rejected")
	}
}

func TestReaderMaxBytes(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "case_1")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "RCA.md"), []byte("123456"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(base, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(caseWithRoot(root), "RCA.md", 0); err == nil {
		t.Fatal("oversized read succeeded; want rejected")
	}
}

func caseWithRoot(root string) CaseRef {
	return CaseRef{CaseID: "case_1", OutputRoot: &root}
}
