package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInferIncidentMetadataUsesPrimaryVulnerableTokenSymbol(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "artifacts", "rca", "report.json"), `{
		"vulnerability": {
			"affected_contracts": [
				{"address":"0xb192d4a737430aa61cea4ce9bfb6432f7d42592f","name":"Token","role":"primary vulnerable contract"},
				{"address":"0xa1e08e10eb09857a8c6f2ef6cca297c1a081ed6b","name":"PancakePair","role":"affected AMM pair"}
			]
		}
	}`)
	mustWrite(t, filepath.Join(root, "artifacts", "rca", "input", "address_db.json"), `{
		"addresses": [
			{"address":"0xb192d4a737430aa61cea4ce9bfb6432f7d42592f","label":"Token","name":"Token","contract_name":"Token","token_metadata":{"symbol":"FPC","name":"FPC"}}
		]
	}`)

	got := inferIncidentMetadata(root)
	if got["protocol"] != "FPC" {
		t.Fatalf("protocol = %v, want FPC; metadata=%#v", got["protocol"], got)
	}
	if got["vulnerable_contract"] != "0xb192d4a737430aa61cea4ce9bfb6432f7d42592f" {
		t.Fatalf("vulnerable_contract = %v", got["vulnerable_contract"])
	}
}

func mustWrite(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
