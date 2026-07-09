package artifacts

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestECWReaderExposesDedicatedProfileWithoutChangingDefault(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "case_1")
	for _, rel := range []string{
		"artifacts/rca/rca_frontier.json",
		"artifacts/rca/input/decompiled_code_context.json",
		"artifacts/rca/input/decompiled_pseudocode.md",
		"artifacts/poc_sketch/poc_context.json",
		"artifacts/agent_poc/result.json",
		"artifacts/agent_poc/foundry/test/PoC.t.sol",
		"artifacts/semantic/pseudocode_compact.txt",
	} {
		writeArtifactFile(t, root, rel, "ecw")
	}
	writeArtifactFile(t, root, "artifacts/rca/victim_sources/0xabc/Contract.sol", "source")
	writeArtifactFile(t, root, "artifacts/cefg/full_cefg.json", "{}")

	defaultReader, err := NewReader(base, 0)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(defaultReader.AllowedPaths(), "artifacts/rca/rca_frontier.json") {
		t.Fatal("default artifact profile includes ECW RCA frontier; want unchanged product profile")
	}
	if slices.Contains(defaultReader.AllowedPaths(), "artifacts/rca/input/decompiled_code_context.json") {
		t.Fatal("default artifact profile includes RCA decompiler context; want unchanged product profile")
	}

	ecwReader, err := NewECWReader(base, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ecwReader.MaxBytes != DefaultECWExportMaxBytes {
		t.Fatalf("ECW reader max bytes = %d, want %d", ecwReader.MaxBytes, DefaultECWExportMaxBytes)
	}
	for _, rel := range []string{
		"artifacts/rca/rca_frontier.json",
		"artifacts/rca/input/decompiled_code_context.json",
		"artifacts/rca/input/decompiled_pseudocode.md",
		"artifacts/poc_sketch/poc_context.json",
		"artifacts/agent_poc/result.json",
		"artifacts/agent_poc/foundry/test/PoC.t.sol",
		"artifacts/semantic/pseudocode_compact.txt",
	} {
		if _, err := ecwReader.Read(caseWithRoot(root), rel, 0); err != nil {
			t.Fatalf("ECW Read(%q): %v", rel, err)
		}
	}

	for _, rel := range []string{
		"artifacts/rca/victim_sources/0xabc/Contract.sol",
		"artifacts/cefg/full_cefg.json",
		"artifacts/semantic/raw.json",
		"prompts/system.txt",
		"codex/events.jsonl",
	} {
		if _, err := ecwReader.Read(caseWithRoot(root), rel, 0); err == nil {
			t.Fatalf("ECW Read(%q) succeeded; want rejected", rel)
		}
	}
}

// TestECWReaderServesRenamedPoCBaseContract guards the self-containment of the
// exported PoC project. The engine renamed the PoC base contract file from
// LumosPoCBase.sol to Base.sol (PoC.t.sol imports "./Base.sol"); the export
// allowlist must expose Base.sol at both PoC locations so the exported bundle
// compiles standalone instead of dropping the base file the PoC imports.
func TestECWReaderServesRenamedPoCBaseContract(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "case_1")
	writeArtifactFile(t, root, "report_bundle/poc/PoC.t.sol", "import \"./Base.sol\";\ncontract AttackTest is Base {}\n")
	writeArtifactFile(t, root, "report_bundle/poc/Base.sol", "abstract contract Base {}\n")
	writeArtifactFile(t, root, "artifacts/agent_poc/foundry/test/PoC.t.sol", "import \"./Base.sol\";\ncontract AttackTest is Base {}\n")
	writeArtifactFile(t, root, "artifacts/agent_poc/foundry/test/Base.sol", "abstract contract Base {}\n")

	reader, err := NewECWReader(base, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"report_bundle/poc/Base.sol", "artifacts/agent_poc/foundry/test/Base.sol"} {
		if !slices.Contains(reader.AllowedPaths(), rel) {
			t.Fatalf("ECW export profile missing renamed PoC base %q; exported PoC would not build", rel)
		}
		result, err := reader.Read(caseWithRoot(root), rel, 0)
		if err != nil {
			t.Fatalf("ECW Read(%q): %v", rel, err)
		}
		if result.Text != "abstract contract Base {}\n" {
			t.Fatalf("unexpected PoC base content for %q: %+v", rel, result)
		}
	}
}

func writeArtifactFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
