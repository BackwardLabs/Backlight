package xfeed

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/mention"
	"github.com/UPside-Lumos-V2/helios/internal/outcome"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

func TestRunnerTagsVictimMentionInHeadline(t *testing.T) {
	root := writeTruebitArtifacts(t)
	skillDir := writeSkillDir(t)
	tx := "0xcd4755645595094a8ab984d0db7e3b4aabde72a5c87c4f176a030629c47fb014"
	r := &Runner{
		Enabled:  true,
		SkillDir: skillDir,
		Mentions: mention.BuildIndex([]store.MentionEntity{
			{CanonicalID: "truebit", EntityName: "Truebit", Aliases: []string{"Truebit"}, XHandle: "TruebitProtocol", MentionPolicy: "allow", Status: "active"},
		}),
	}
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
		GitHubURL:    "https://github.com/BackwardLabs/Q1-2026/tree/main/test/2026-01/truebit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.MainPost, "🚨 Truebit (@TruebitProtocol) — Settlement Accounting Exploit") {
		t.Fatalf("headline missing victim mention:\n%s", res.MainPost)
	}
	if res.ProtocolMention != "@TruebitProtocol" || res.MentionVerification != "static" || res.MentionReason != "tagged" {
		t.Fatalf("mention metadata = %+v", res)
	}
}

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
		"🚨 Truebit — Settlement Accounting Exploit",
		"On January 8, 2026, Truebit was exploited on Ethereum, with an estimated loss of $26.42M.",
		"Key info",
		"• Occurred: 2026-01-08 16:02 UTC",
		"• Estimated loss: $26.42M",
		"TL;DR",
		"Evidence points to a pricing/accounting mismatch in the Truebit market settlement path.",
		"Why it matters",
		"Builder takeaway",
	} {
		if !strings.Contains(res.MainPost, want) {
			t.Fatalf("main post missing %q:\n%s", want, res.MainPost)
		}
	}
	for _, want := range []string{
		"Artifacts + analysis 🧾",
		"Artifacts:\n- Report: https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/truebit/README.md",
		"- PoC: https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/truebit/truebit.t.sol",
		"Analysis:\nAt a high level,",
		"Explorer:\nhttps://etherscan.io/tx/" + tx,
	} {
		if !strings.Contains(res.ReplyPost, want) {
			t.Fatalf("reply post missing %q:\n%s", want, res.ReplyPost)
		}
	}
	if strings.HasPrefix(res.ReplyPost, "1/") {
		t.Fatalf("single evidence reply should not be numbered:\n%s", res.ReplyPost)
	}
	if strings.Contains(res.ReplyPost, "Attacker CA") {
		t.Fatalf("reply should omit attacker CA by default:\n%s", res.ReplyPost)
	}
	if strings.Contains(res.MainPost, "ETH payouts") || strings.Contains(res.ReplyPost, "ETH moved") {
		t.Fatalf("pricing fallback should not hard-code ETH when no asset symbol is known:\nmain:\n%s\nreply:\n%s", res.MainPost, res.ReplyPost)
	}
	for _, bad := range []string{"[Backlight", "Tx: ", "PoC", "Forge", "Report:"} {
		if strings.Contains(res.MainPost, bad) {
			t.Fatalf("main post contains internal/linked detail %q:\n%s", bad, res.MainPost)
		}
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

func TestRenderEvidenceReplyFallsBackToTransactionOnly(t *testing.T) {
	got := renderEvidenceReply(incidentFacts{ExplorerURL: "https://etherscan.io/tx/0x1234"})
	if want := "Tx: https://etherscan.io/tx/0x1234"; got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
}

func TestPublicAnalysisUsesConfirmedInvariant(t *testing.T) {
	reportJSON := []byte(`{
		"vulnerability": {
			"confidence": "high",
			"violated_invariant": "A token transfer into an AMM pair must not debit or resynchronize the pair's existing inventory before crediting the incoming transfer."
		},
		"impact": {"attacker_profit_symbol": "USDT"}
	}`)
	got := publicAnalysis(reportJSON, nil)
	for _, want := range []string{
		"broke a protocol-state invariant",
		"pair's existing inventory",
		"enabled USDT extraction",
		"public report and PoC document the observed economic effect",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("analysis missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "lower-level implementation details remain under review") {
		t.Fatalf("confirmed analysis retained partial-RCA caveat: %s", got)
	}
}

func TestRunnerUsesAssetSymbolForSettlementNarrative(t *testing.T) {
	root := writeDLMCArtifacts(t)
	r := &Runner{Enabled: true, SkillDir: writeSkillDir(t)}
	res, err := r.Run(context.Background(), Case{
		CaseID:       "dlmc",
		Chain:        "bsc",
		TxHash:       "0x151025d3f0a782340a74d30ef33a5fad044b838e74437a803f0652e70c231306",
		OutputRoot:   root,
		IncidentSlug: "dlmc",
		Outcome:      outcome.OutcomeVerified,
		PublishTier:  outcome.PublishTierPublicVerified,
		PoCState:     outcome.PoCStateEconomic,
		RCAState:     outcome.RCAStateComplete,
		ReportURL:    "https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-06/dlmc/README.md",
		GitHubURL:    "https://github.com/BackwardLabs/Q1-2026/tree/main/test/2026-06/dlmc",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"USDT payouts bounded by market reserves",
		"USDT leave the affected reserve",
	} {
		if !strings.Contains(res.MainPost, want) {
			t.Fatalf("main post missing %q:\n%s", want, res.MainPost)
		}
	}
	if strings.Contains(res.MainPost, "ETH payouts") || strings.Contains(res.MainPost, "ETH leave") || strings.Contains(res.ReplyPost, "ETH moved") {
		t.Fatalf("settlement narrative should use USDT, not ETH:\nmain:\n%s\nreply:\n%s", res.MainPost, res.ReplyPost)
	}
}

func TestOccurredTextUsesReportIncidentBasisBeforeDetectedAt(t *testing.T) {
	summary := []byte(`{"economic_reproduction":{"pricing":{"tx_timestamp":1893456000}}}`)
	report := strings.Join([]string{
		"- **Detected at**: 2026-06-25T13:07:18Z",
		"- **Funds valued at**: 2026-01-10T08:30:35Z (price as of block N-1, pre-hack)",
	}, "\n")
	got := occurredText(summary, nil, nil, nil, report, "")
	if got != "2026-01-10 08:30 UTC" {
		t.Fatalf("occurredText = %q, want report incident basis time", got)
	}
}

func TestOccurredTextDoesNotUseDetectedAtAsIncidentTime(t *testing.T) {
	report := "- **Detected at**: 2026-06-25T13:07:18Z"
	if got := occurredText(nil, nil, nil, nil, report, ""); got != "" {
		t.Fatalf("occurredText used detection timestamp as incident time: %q", got)
	}
}

func TestOccurredTextPrefersAgentResultTimestampBeforeSummaryPricing(t *testing.T) {
	summary := []byte(`{"economic_reproduction":{"pricing":{"tx_timestamp":1893456000}}}`)
	result := []byte(`{"tx_timestamp":1768033835}`)
	got := occurredText(summary, nil, result, nil, "", "")
	if got != "2026-01-10 08:30 UTC" {
		t.Fatalf("occurredText = %q, want agent result timestamp", got)
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

func TestRunnerBlocksProofBoundaryRCAFromXPublish(t *testing.T) {
	root := writeTruebitArtifacts(t)
	r := &Runner{Enabled: true, SkillDir: writeSkillDir(t)}
	res, err := r.Run(context.Background(), Case{
		CaseID:       "004_truebit",
		Chain:        "ethereum",
		TxHash:       "0xcd4755645595094a8ab984d0db7e3b4aabde72a5c87c4f176a030629c47fb014",
		OutputRoot:   root,
		IncidentSlug: "004_truebit",
		Outcome:      outcome.OutcomePartial,
		PublishTier:  outcome.PublishTierEconomicIncompleteRCA,
		PoCState:     outcome.PoCStateEconomic,
		RCAState:     outcome.RCAStateNoPatchableEvidence,
		ReportURL:    "https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/truebit/README.md",
		PoCURL:       "https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/truebit/truebit.t.sol",
		GitHubURL:    "https://github.com/BackwardLabs/Q1-2026/tree/main/test/2026-01/truebit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ReadyToPublish || !containsString(res.Blockers, "x_publish_ineligible") {
		t.Fatalf("proof-boundary RCA should block x publish: %+v", res)
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

func TestRunnerBlocksWhenRCADraftHasNoConcreteRootCause(t *testing.T) {
	root := writeTruebitArtifacts(t)
	reportPath := filepath.Join(root, "report_bundle", "report", "report.json")
	if err := os.WriteFile(reportPath, []byte(`{"analysis_status":"partial","vulnerability":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Enabled: true, SkillDir: writeSkillDir(t)}
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
		PoCURL:       "https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/truebit/truebit.t.sol",
		GitHubURL:    "https://github.com/BackwardLabs/Q1-2026/tree/main/test/2026-01/truebit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ReadyToPublish || !containsString(res.Blockers, "x_draft_rca_insufficient") {
		t.Fatalf("generic RCA draft should block publish: %+v", res)
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
		"# Public Card Packet — Truebit Incident",
		"- **Disclosure status**: publishable",
		"- **Headline**: Truebit — Settlement Accounting Exploit",
		"- **Occurred**: 2026-01-08 16:02 UTC",
		"- **Impact card**: $26.42M",
		"- **RCA status**: partial",
		"- **Violated invariant**:",
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("card brief missing %q:\n%s", want, brief)
		}
	}
}

func TestRunnerCardBriefUsesRCAFlowAndImpact(t *testing.T) {
	root := writePancakeArtifacts(t)
	r := &Runner{
		Enabled:       true,
		SkillDir:      writeSkillDirWithCard(t),
		CardEnabled:   true,
		CardPythonBin: writeFakeCardPython(t),
	}
	res, err := r.Run(context.Background(), Case{
		CaseID:       "pancakeswap_v2",
		Chain:        "bsc",
		TxHash:       "0x8dabb60a94e5124462e5f494a25c14bcd52f6f4d1f7c665a249496f4c6c24764",
		OutputRoot:   root,
		IncidentSlug: "pnacakeswap_v2",
		Outcome:      outcome.OutcomeVerified,
		PublishTier:  outcome.PublishTierPublicVerified,
		PoCState:     outcome.PoCStateEconomic,
		RCAState:     outcome.RCAStateComplete,
		ReportURL:    "https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-06/pancakeswap_v2/README.md",
		GitHubURL:    "https://github.com/BackwardLabs/Q1-2026/tree/main/test/2026-06/pancakeswap_v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	brief := mustReadFile(t, res.CardBriefPath)
	for _, want := range []string{
		"# Public Card Packet — Pancakeswap V2 Incident",
		"- **Headline**: Pancakeswap V2 — LP Reserve Burn",
		"- **Vulnerable path**: OLPCToken.transfer -> PancakePair.skim -> PancakePair.sync",
		"- **Vulnerable contract**: OLPCToken (0x5881...0000)",
		"- **Victim**: PancakePair (impacted OLPC/LABUBU AMM pair)",
		"- **Impact card**: $1.12M",
		"- **Affected assets**: USDT, LABUBU, OLPC",
		"- **Flow step 2**: skim() starts transfer",
		"- **Flow step 3**: pool balance drops",
		"- **Flow step 4**: attacker cashes out",
		"- **RCA status**: confirmed",
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("card brief missing %q:\n%s", want, brief)
		}
	}
	for _, bad := range []string{
		"accounting or pricing mismatch",
		"payout > checked value",
		"enter vulnerable path",
		"- **PoC status**:",
		"- **Proof kind**:",
		"- **Attacker gain reproduced**:",
	} {
		if strings.Contains(brief, bad) {
			t.Fatalf("card brief contains generic fallback %q:\n%s", bad, brief)
		}
	}
}

func TestRunnerCardBriefUsesRCAVulnerableFunctionForMarginWithdrawal(t *testing.T) {
	root := writeMTTokenArtifacts(t)
	r := &Runner{
		Enabled:       true,
		SkillDir:      writeSkillDirWithCard(t),
		CardEnabled:   true,
		CardPythonBin: writeFakeCardPython(t),
	}
	res, err := r.Run(context.Background(), Case{
		CaseID:       "mttoken",
		Chain:        "arbitrum",
		TxHash:       "0xe1e6aa5332deaf0fa0a3584113c17bedc906148730cbbc73efae16306121687b",
		OutputRoot:   root,
		IncidentSlug: "mttoken",
		Outcome:      outcome.OutcomePartial,
		PublishTier:  outcome.PublishTierEconomicIncompleteRCA,
		PoCState:     outcome.PoCStateEconomic,
		RCAState:     outcome.RCAStateScopeLimited,
		ReportURL:    "https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-06/mttoken/README.md",
		GitHubURL:    "https://github.com/BackwardLabs/Q1-2026/tree/main/test/2026-06/mttoken",
	})
	if err != nil {
		t.Fatal(err)
	}
	brief := mustReadFile(t, res.CardBriefPath)
	for _, want := range []string{
		"- **RCA status**: partial",
		"- **Confidence**: medium",
		"- **Vulnerable path**: changePosition(int256,int256,int256)",
		"- **Vulnerable contract**: vulnerable proxy (0xf7ca...80bc)",
		"- **Flow step 1**: reach changePosition()",
		"- **Flow step 2**: position/accounting changes",
		"- **Flow step 3**: margin check accepts withdrawal",
		"- **Flow step 4**: victim assets leave",
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("card brief missing %q:\n%s", want, brief)
		}
	}
	for _, bad := range []string{
		"call withdraw(victim)",
		"helper spends allowance",
		"unknown proxy",
	} {
		if strings.Contains(brief, bad) {
			t.Fatalf("card brief contains stale/generic text %q:\n%s", bad, brief)
		}
	}
	for _, want := range []string{
		"Medium-confidence RCA points to changePosition allowed an under-collateralized negative margin withdrawal",
		"The attacker reached changePosition() on the vulnerable MTToken proxy path.",
		"The RCA points to an under-collateralized negative margin withdrawal being accepted after attacker-controlled position/accounting changes.",
	} {
		if !strings.Contains(res.MainPost, want) {
			t.Fatalf("main post missing %q:\n%s", want, res.MainPost)
		}
	}
	for _, bad := range []string{
		"Root cause remains under review.",
		"validation and accounting checks would keep the state bounded",
	} {
		if strings.Contains(res.MainPost, bad) {
			t.Fatalf("main post contains stale/generic text %q:\n%s", bad, res.MainPost)
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

func writePancakeArtifacts(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	reportDir := filepath.Join(root, "report_bundle", "report")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := `{
  "chain": "bsc",
  "status": "pass",
  "tx_hash": "0x8dabb60a94e5124462e5f494a25c14bcd52f6f4d1f7c665a249496f4c6c24764",
  "economic_reproduction": {
    "status": "pass",
    "verdict": "exact",
    "incident": {
      "asset_legs": [
        {"holder":"0xedb7dcb4cdfec957f8df5cbf5e94229a6cc9f365","symbol":"LABUBU"},
        {"holder":"0xedb7dcb4cdfec957f8df5cbf5e94229a6cc9f365","symbol":"OLPC"}
      ]
    },
    "poc": {
      "expected_reproduced_usd": 1114815.7795317562,
      "gain_family_selection": {
        "included_legs": [
          {"symbol":"USDT"}
        ]
      }
    },
    "pricing": {"tx_timestamp": 1781955063}
  },
  "poc": {"status":"verified","execution_state":"economic_poc"},
  "rca": {"status":"complete","analysis_status":"complete"}
}`
	if err := os.WriteFile(filepath.Join(reportDir, "run_summary.json"), []byte(summary), 0o644); err != nil {
		t.Fatal(err)
	}
	reportJSON := `{
  "analysis_status": "complete",
  "chain": "bsc",
  "attack_summary": {
    "loop_count": 20,
    "public_entrypoint_called_per_iteration": "OLPCToken.transfer -> PancakePair.skim -> PancakePair.sync"
  },
  "impact": {
    "attacker_profit_symbol": "USDT",
    "attacker_profit_formatted": "1115903.663412131721557252"
  },
  "vulnerability": {
    "title": "OLPC pair-out transfer branch lets skim trigger amplified pair-balance burns",
    "root_cause": "OLPCToken._update treats any transfer from its Pancake swap pair to a non-exempt address as a buy and debits value * decimalsValue from the pair to 0xdead, then sets the actual transfer value to zero. Repeated skim/sync cycles reduce the pair's OLPC balance and enable downstream conversion into USDT profit.",
    "violated_invariant": "Pair-out transfer hooks must not burn more pair balance than the skim transfer requested.",
    "severity": "high",
    "confidence": "high",
    "affected_contracts": [
      {
        "address": "0x58815cdf9955121a6274680ab396a36fc9e00000",
        "name": "OLPCToken",
        "role": "primary vulnerable contract"
      },
      {
        "address": "0xedb7dcb4cdfec957f8df5cbf5e94229a6cc9f365",
        "name": "PancakePair",
        "role": "impacted OLPC/LABUBU AMM pair"
      }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(reportDir, "report.json"), []byte(reportJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	report := `# pnacakeswap_v2 Incident Report

## Root Cause

- **Finding**: OLPC pair-out transfer branch lets skim trigger amplified pair-balance burns
- **In short**: The vulnerable path is the ` + "`OLPCToken.transfer -> PancakePair.skim -> PancakePair.sync`" + ` flow.

Mechanism:

- The attacker reached the victim through the ` + "`OLPCToken.transfer -> PancakePair.skim -> PancakePair.sync`" + ` flow during the exploit.
- OLPCToken._update treats any transfer from its Pancake swap pair to a non-exempt address as a buy and debits value * decimalsValue from the pair.
- The accounting update reduced pair balance and reserves, enabling downstream conversion into USDT profit.

Key evidence:

- PoC execution, economic proof, forge build, and forge test all passed.
- The OLPC/LABUBU pair lost OLPC and LABUBU while the attacker gained USDT.
- The PoC primes the OLPC pair, repeatedly transfers small OLPC amounts, calls skim and sync, then swaps OLPC for USDT.
`
	if err := os.WriteFile(filepath.Join(reportDir, "REPORT.md"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeDLMCArtifacts(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	reportDir := filepath.Join(root, "report_bundle", "report")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := `{
  "chain": "bsc",
  "status": "pass",
  "tx_hash": "0x151025d3f0a782340a74d30ef33a5fad044b838e74437a803f0652e70c231306",
  "economic_reproduction": {
    "status": "pass",
    "poc": {
      "expected_reproduced_usd": 221878.74610017723,
      "gain_family_selection": {
        "included_legs": [
          {"symbol":"USDT"}
        ]
      }
    },
    "pricing": {"tx_timestamp": 1782299710}
  },
  "poc": {"status":"verified","execution_state":"economic_poc"},
  "rca": {"status":"complete","analysis_status":"complete"}
}`
	if err := os.WriteFile(filepath.Join(reportDir, "run_summary.json"), []byte(summary), 0o644); err != nil {
		t.Fatal(err)
	}
	reportJSON := `{
  "analysis_status": "complete",
  "chain": "bsc",
  "vulnerability": {
    "category": "business_logic_flaw",
    "subtype": "redeemable_supply_price_accounting",
    "title": "DLMC buy/referral accounting inflated livePrice and allowed same-transaction redemption of referral DLMC for USDT",
    "root_cause": "DLMCToken buy(uint256) mints DLMC to address(this), records the buyer's same-transaction investment, and grants referrer sellable DLMC before sell(uint256) redeems at inflated livePrice.",
    "violated_invariant": "A DLMC balance redeemable through sell() must not be priced using temporary buy liquidity while excluding newly minted redeemable supply.",
    "severity": "high",
    "confidence": "high"
  },
  "impact": {
    "attacker_profit_symbol": "USDT",
    "attacker_profit_formatted": "222560.221693222099016479",
    "victim_usdt_delta_formatted": "-226119.118936329868440038"
  }
}`
	if err := os.WriteFile(filepath.Join(reportDir, "report.json"), []byte(reportJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	report := `# dlmc Incident Report

## Root Cause

- **Finding**: DLMC buy/referral accounting inflated livePrice and allowed same-transaction redemption of referral DLMC for USDT
- **Violated invariant**: A DLMC balance redeemable through sell() must not be priced using temporary buy liquidity while excluding newly minted redeemable supply.
- **Severity**: high
- **Confidence**: high
`
	if err := os.WriteFile(filepath.Join(reportDir, "REPORT.md"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeMTTokenArtifacts(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	reportDir := filepath.Join(root, "report_bundle", "report")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := `{
  "chain": "arbitrum",
  "status": "partial",
  "tx_hash": "0xe1e6aa5332deaf0fa0a3584113c17bedc906148730cbbc73efae16306121687b",
  "economic_reproduction": {
    "status": "pass",
    "incident": {
      "net_loss_usd": 406220.61,
      "asset_legs": [
        {"holder":"0xf7ca7384cc6619866749955065f17bedd3ed80bc","symbol":"USDC"},
        {"holder":"0xf7ca7384cc6619866749955065f17bedd3ed80bc","symbol":"WETH"}
      ]
    },
    "poc": {
      "expected_reproduced_usd": 395149.66,
      "gain_family_selection": {
        "included_legs": [
          {"symbol":"USDC"}
        ]
      }
    },
    "pricing": {"tx_timestamp": 1768033835}
  },
  "poc": {"status":"verified","execution_state":"economic_poc"},
  "rca": {"status":"partial","analysis_status":"partial"}
}`
	if err := os.WriteFile(filepath.Join(reportDir, "run_summary.json"), []byte(summary), 0o644); err != nil {
		t.Fatal(err)
	}
	reportJSON := `{
  "analysis_status": "partial",
  "root_cause_confidence": "medium",
  "chain": "arbitrum",
  "attack_summary": {
    "entry_function": "changePosition(int256,int256,int256)",
    "attacker_callback_used": "executeOperation(address,uint256,uint256,address,bytes)"
  },
  "impact": {
    "attacker_profit_symbol": "USDC",
    "attacker_profit_formatted": "394742.852305",
    "victim_losses": [
      {"symbol":"USDC","delta_formatted":"-197436.748947"},
      {"symbol":"WETH","delta_formatted":"-67.574321417357759466"}
    ]
  },
  "vulnerability": {
    "title": "changePosition allowed an under-collateralized negative margin withdrawal after attacker-controlled position accounting changes",
    "root_cause": "After attacker-controlled position setup and a large position/accounting change, the final drain frame calls changePosition(0, -894992852305, 0), and the victim accepts the negative margin withdrawal while writing large accounting state changes and releasing funds.",
    "violated_invariant": "A margin withdrawal must not exceed the account's verified withdrawable equity after current position size, PnL, funding, fees, and price/solvency checks are applied.",
    "severity": "critical",
    "confidence": "medium",
    "affected_contracts": [
      {
        "address": "0xf7ca7384cc6619866749955065f17bedd3ed80bc",
        "name": "unknown proxy",
        "role": "primary vulnerable contract"
      }
    ]
  },
  "limitations": [
    "source_branch_gap: verified source for implementation is not present."
  ]
}`
	if err := os.WriteFile(filepath.Join(reportDir, "report.json"), []byte(reportJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	report := `# MTToken Incident Report

## Root Cause

- **Finding**: changePosition allowed an under-collateralized negative margin withdrawal after attacker-controlled position accounting changes
- **In short**: The victim proxy delegates changePosition(int256,int256,int256) to its implementation.

Mechanism:

- The exploit entered through changePosition(int256,int256,int256) before reaching the vulnerable accounting path.
- That path trusted attacker-controlled state while performing protected accounting updates.
- The accounting update violated the withdrawable equity invariant.

Key evidence:

- PoC, forge build/test, and economic proof status are pass.
- Verified PoC sequence shows setup changePosition calls, a large position/accounting change, then drain() calling changePosition(0, -894992852305, 0).
`
	if err := os.WriteFile(filepath.Join(reportDir, "REPORT.md"), []byte(report), 0o644); err != nil {
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
