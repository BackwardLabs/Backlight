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

	ecwReader, err := NewECWReader(base, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ecwReader.MaxBytes != DefaultECWExportMaxBytes {
		t.Fatalf("ECW reader max bytes = %d, want %d", ecwReader.MaxBytes, DefaultECWExportMaxBytes)
	}
	for _, rel := range []string{
		"artifacts/rca/rca_frontier.json",
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
