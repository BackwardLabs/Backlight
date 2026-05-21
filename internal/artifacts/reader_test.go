package artifacts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReaderAllowsOnlyTopLevelProductArtifacts(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "case_1")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "summary.json"), []byte(`{"status":"pass"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Report.md"), []byte("# Incident report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o755); err != nil {
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

	got, err := reader.Read(c, "summary.json", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "summary.json" || got.Text == "" {
		t.Fatalf("unexpected read result: %+v", got)
	}
	report, err := reader.Read(c, "Report.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Path != "Report.md" || report.Text != "# Incident report\n" {
		t.Fatalf("unexpected report read result: %+v", report)
	}

	for _, path := range []string{"artifacts/secret.json", "../summary.json", "/tmp/summary.json"} {
		if _, err := reader.Read(c, path, 0); err == nil {
			t.Fatalf("Read(%q) succeeded; want rejected", path)
		}
	}
}

func TestReaderRejectsOutputRootOutsideBase(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "summary.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(base, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(caseWithRoot(outside), "summary.json", 0); err == nil {
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
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "summary.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	reader, err := NewReader(base, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(caseWithRoot(root), "summary.md", 0); err == nil {
		t.Fatal("symlink escape succeeded; want rejected")
	}
}

func TestReaderMaxBytes(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "case_1")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rca.md"), []byte("123456"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(base, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(caseWithRoot(root), "rca.md", 0); err == nil {
		t.Fatal("oversized read succeeded; want rejected")
	}
}

func caseWithRoot(root string) CaseRef {
	return CaseRef{CaseID: "case_1", OutputRoot: &root}
}
