package artifacts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReaderExposesPrettyProductArtifactAliases(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "case_1")
	if err := os.MkdirAll(filepath.Join(root, "report_bundle", "report"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "report_bundle", "poc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "report", "REPORT.md"), []byte("# Incident report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "RCA.md"), []byte("# RCA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "poc", "PoC.t.sol"), []byte("contract PoC {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o755); err != nil {
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

	for _, path := range []string{"summary.json", "summary.md", "rca.md", "Report.md", "report_bundle/report/REPORT.md", "report_bundle/poc/PoC.t.sol", "artifacts/secret.json", "../RCA.md", "/tmp/RCA.md"} {
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
