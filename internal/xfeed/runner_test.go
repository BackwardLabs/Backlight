package xfeed

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/outcome"
)

func TestRunnerDraftsBacklightIncidentThread(t *testing.T) {
	root := writeTruebitArtifacts(t)
	skillDir := writeSkillDir(t)
	tx := "0xcd4755645595094a8ab984d0db7e3b4aabde72a5c87c4f176a030629c47fb014"
	r := &Runner{Enabled: true, SkillDir: skillDir}
	res, err := r.Run(context.Background(), Case{
		CaseID:       "004_truebit",
		Chain:        "ethereum",
		TxHash:       tx,
		OutputRoot:   root,
		IncidentSlug: "004_truebit",
		Outcome:      outcome.OutcomePartial,
		PublishTier:  outcome.PublishTierEconomicIncompleteRCA,
		PoCState:     outcome.PoCStateEconomic,
		RCAState:     outcome.RCAStateScopeLimited,
		ReportURL:    "https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/truebit/README.md",
		PoCURL:       "https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/truebit/truebit.t.sol",
		GitHubURL:    "https://github.com/BackwardLabs/Q1-2026/tree/main/test/2026-01/truebit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.ReadyToPublish || res.StatusLabel != "Initial Analysis" || res.ImageMode != "none" {
		t.Fatalf("result readiness/status/image = %+v", res)
	}
	for _, want := range []string{
		"[Backlight Initial Analysis]",
		"🚨 Truebit exploit on Ethereum",
		"Tx: " + tx,
		"🕒 Occurred: 2026-01-08 16:02 UTC",
		"💥 Impact: approximately ~$26.4M",
		"Evidence points to a pricing/accounting mismatch in the Truebit market settlement path.",
		"Need more detail? Check our repo and analysis thread below ↓ 🧵",
	} {
		if !strings.Contains(res.MainPost, want) {
			t.Fatalf("main post missing %q:\n%s", want, res.MainPost)
		}
	}
	for _, want := range []string{
		"1/ Artifacts + analysis 🧾",
		"- Report: https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/truebit/README.md",
		"- PoC: https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/truebit/truebit.t.sol",
		"Analysis:\nAt a high level, this is a market interaction and settlement-accounting issue.",
		"Explorer:\nhttps://etherscan.io/tx/" + tx,
	} {
		if !strings.Contains(res.ReplyPost, want) {
			t.Fatalf("reply post missing %q:\n%s", want, res.ReplyPost)
		}
	}
	if strings.Contains(res.ReplyPost, "Attacker CA") {
		t.Fatalf("reply should omit attacker CA by default:\n%s", res.ReplyPost)
	}
	if !strings.Contains(res.TelegramPost, res.MainPost+"\n\nGitHub:\nhttps://github.com/BackwardLabs/Q1-2026/tree/main/test/2026-01/truebit") {
		t.Fatalf("telegram post did not append GitHub URL:\n%s", res.TelegramPost)
	}
	for _, path := range []string{res.StatusPath, res.MainPostPath, res.ReplyPostPath, res.TelegramPostPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("draft artifact not written %s: %v", path, err)
		}
	}
}

func TestRunnerBlocksWhenPublicReportURLMissing(t *testing.T) {
	root := writeTruebitArtifacts(t)
	r := &Runner{Enabled: true, SkillDir: writeSkillDir(t)}
	res, err := r.Run(context.Background(), Case{
		CaseID:       "004_truebit",
		Chain:        "ethereum",
		TxHash:       "0xcd4755645595094a8ab984d0db7e3b4aabde72a5c87c4f176a030629c47fb014",
		OutputRoot:   root,
		IncidentSlug: "004_truebit",
		Outcome:      outcome.OutcomePartial,
		GitHubURL:    "https://github.com/BackwardLabs/Q1-2026/tree/main/test/2026-01/truebit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ReadyToPublish || !containsString(res.Blockers, "report_public_url_missing") {
		t.Fatalf("missing report should block publish: %+v", res)
	}
}

func TestRunnerBlocksWhenFormatFileMissing(t *testing.T) {
	root := writeTruebitArtifacts(t)
	r := &Runner{Enabled: true, SkillDir: filepath.Join(t.TempDir(), "missing")}
	res, err := r.Run(context.Background(), Case{
		CaseID:       "004_truebit",
		Chain:        "ethereum",
		TxHash:       "0xcd4755645595094a8ab984d0db7e3b4aabde72a5c87c4f176a030629c47fb014",
		OutputRoot:   root,
		IncidentSlug: "004_truebit",
		Outcome:      outcome.OutcomePartial,
		ReportURL:    "https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/truebit/README.md",
		GitHubURL:    "https://github.com/BackwardLabs/Q1-2026/tree/main/test/2026-01/truebit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ReadyToPublish || !containsString(res.Blockers, "x_feed_format_missing") {
		t.Fatalf("missing format should block publish: %+v", res)
	}
}

func TestRunnerGeneratesExploitFlowCardImagePath(t *testing.T) {
	root := writeTruebitArtifacts(t)
	r := &Runner{
		Enabled:       true,
		SkillDir:      writeSkillDirWithCard(t),
		CardEnabled:   true,
		CardPythonBin: writeFakeCardPython(t),
	}
	res, err := r.Run(context.Background(), Case{
		CaseID:       "004_truebit",
		Chain:        "ethereum",
		TxHash:       "0xcd4755645595094a8ab984d0db7e3b4aabde72a5c87c4f176a030629c47fb014",
		OutputRoot:   root,
		IncidentSlug: "004_truebit",
		Outcome:      outcome.OutcomePartial,
		PublishTier:  outcome.PublishTierEconomicIncompleteRCA,
		PoCState:     outcome.PoCStateEconomic,
		RCAState:     outcome.RCAStateScopeLimited,
		ReportURL:    "https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/truebit/README.md",
		GitHubURL:    "https://github.com/BackwardLabs/Q1-2026/tree/main/test/2026-01/truebit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ImageMode != "exploit_flow_card_png" || !strings.HasSuffix(res.ImagePath, "x-feed-visuals/exploit-flow-card.png") {
		t.Fatalf("card image not attached: %+v", res)
	}
	for _, path := range []string{res.CardBriefPath, res.CardSVGPath, res.CardPNGPath, res.ImagePath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("card artifact not written %s: %v", path, err)
		}
	}
	brief := mustReadFile(t, res.CardBriefPath)
	for _, want := range []string{
		"# Truebit Exploit Flow",
		"- **Estimated loss**: $26423938.99",
		"- **Proof kind**: economic_proof",
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("card brief missing %q:\n%s", want, brief)
		}
	}
}

func writeTruebitArtifacts(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	reportDir := filepath.Join(root, "report_bundle", "report")
	pocDir := filepath.Join(root, "report_bundle", "poc")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pocDir, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := `{
  "chain": "ethereum",
  "status": "partial",
  "tx_hash": "0xcd4755645595094a8ab984d0db7e3b4aabde72a5c87c4f176a030629c47fb014",
  "economic_reproduction": {
    "status": "pass",
    "incident": {"net_loss_usd": 26423938.988967046},
    "poc": {"expected_reproduced_usd": 26423613.68783422},
    "pricing": {"tx_timestamp": 1767888155}
  },
  "poc": {"status":"verified","execution_state":"economic_poc"},
  "rca": {"status":"partial","analysis_status":"partial"}
}`
	if err := os.WriteFile(filepath.Join(reportDir, "run_summary.json"), []byte(summary), 0o644); err != nil {
		t.Fatal(err)
	}
	reportJSON := `{
  "analysis_status": "partial",
  "chain": "ethereum",
  "vulnerability": {
    "category": "business_logic_flaw",
    "subtype": "market_pricing_accounting_formula_gap",
    "root_cause": "The transaction's direct loss is in the Truebit market settlement path."
  }
}`
	if err := os.WriteFile(filepath.Join(reportDir, "report.json"), []byte(reportJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pocDir, "PoC.t.sol"), []byte("uint256 constant TX_TIMESTAMP = 1767888155;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeSkillDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, defaultIncidentFormat)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Backlight Incident Post Format\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeSkillDirWithCard(t *testing.T) string {
	t.Helper()
	root := writeSkillDir(t)
	scriptPath := filepath.Join(root, defaultCardScript)
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte("# fake card script\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeFakeCardPython(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-python")
	script := `#!/bin/sh
set -eu
out=""
base=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --out-dir) out="$2"; shift 2 ;;
    --basename) base="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$out"
printf '<svg></svg>\n' > "$out/$base.svg"
printf 'png\n' > "$out/$base.png"
printf 'svg: %s\npng: %s\n' "$out/$base.svg" "$out/$base.png"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
