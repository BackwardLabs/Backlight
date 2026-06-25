package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/lumoskit"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

func TestCandidateTxHashesFromMetadata(t *testing.T) {
	cases := []struct {
		name string
		meta string
		want []string
	}{
		{"present", `{"candidate_tx_hashes":["0xaa","0xbb"]}`, []string{"0xaa", "0xbb"}},
		{"absent", `{"protocol_name":"X"}`, nil},
		{"empty", ``, nil},
		{"malformed", `not json`, nil},
		{"wrong_type", `{"candidate_tx_hashes":"oops"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := candidateTxHashesFromMetadata(json.RawMessage(tc.meta))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestReadNetFlowMagnitude(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "artifacts", "flow_context")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"net_flows":[{"delta":"-27000000"},{"delta":"15"},{"delta":"100"}]}`
	if err := os.WriteFile(filepath.Join(dir, "fund_flows.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	count, maxAbs := readNetFlowMagnitude(root)
	if count != 3 {
		t.Fatalf("count=%d want 3", count)
	}
	if got := maxAbs.Text('f', 0); got != "27000000" {
		t.Fatalf("maxAbs=%s want 27000000", got)
	}

	// Missing file (e.g. a setup tx whose prefix produced no flows) → zero.
	count2, maxAbs2 := readNetFlowMagnitude(t.TempDir())
	if count2 != 0 || maxAbs2.Sign() != 0 {
		t.Fatalf("expected zero for missing file, got count=%d max=%s", count2, maxAbs2.Text('f', 0))
	}
}

func TestSelectDrainTxPicksLargestNetMovement(t *testing.T) {
	setup := "0x" + strings.Repeat("a", 64)
	drain := "0x" + strings.Repeat("b", 64)
	bin := writeSelectorLumoskit(t, t.TempDir(), drain)

	w := &Worker{Runner: &lumoskit.Runner{Binary: bin}}
	root := t.TempDir()
	c := &store.Case{CaseID: "c1", Chain: "base", TxHash: setup, OutputRoot: &root}

	sel := w.selectDrainTx(context.Background(), c, []string{setup, drain})

	if sel.WinnerTxHash != drain {
		t.Fatalf("expected winner %s, got %s (ranking=%+v)", drain, sel.WinnerTxHash, sel.Ranking)
	}
	if sel.PrimaryTxHash != setup {
		t.Fatalf("expected primary %s, got %s", setup, sel.PrimaryTxHash)
	}
}

func TestSelectDrainTxKeepsPrimaryWhenNoMovement(t *testing.T) {
	setup := "0x" + strings.Repeat("a", 64)
	other := "0x" + strings.Repeat("c", 64)
	// No tx matches the drain marker → both produce empty net_flows.
	bin := writeSelectorLumoskit(t, t.TempDir(), "0x"+strings.Repeat("f", 64))

	w := &Worker{Runner: &lumoskit.Runner{Binary: bin}}
	root := t.TempDir()
	c := &store.Case{CaseID: "c1", Chain: "base", TxHash: setup, OutputRoot: &root}

	sel := w.selectDrainTx(context.Background(), c, []string{setup, other})

	if sel.WinnerTxHash != setup {
		t.Fatalf("expected winner to stay primary %s, got %s", setup, sel.WinnerTxHash)
	}
}

// writeSelectorLumoskit emits a fake lumoskit that writes a large net_flows
// fund_flows.json only for drainTx, and an empty one for every other tx.
func writeSelectorLumoskit(t *testing.T, dir, drainTx string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-selector-lumoskit")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
out=""; tx=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-root) out="$2"; shift 2 ;;
    --tx) tx="$2"; shift 2 ;;
    --chain) shift 2 ;;
    --stage) shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$out/artifacts/flow_context"
if [ "$tx" = "%s" ]; then
  cat > "$out/artifacts/flow_context/fund_flows.json" <<'JSON'
{"net_flows":[{"holder":"0xvictim","asset":"TRAC","delta":"-27000000","flow_ids":["f1"]},{"holder":"0xattacker","asset":"TRAC","delta":"27000000","flow_ids":["f1"]}]}
JSON
else
  cat > "$out/artifacts/flow_context/fund_flows.json" <<'JSON'
{"net_flows":[]}
JSON
fi
`, drainTx)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
