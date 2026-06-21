package xfeed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/UPside-Lumos-V2/helios/internal/outcome"
)

const (
	formatVersion         = "backlight_x_feed_incident_v1"
	defaultIncidentFormat = "skills/draft-x-exploit-thread/references/incident-post-format.md"
	defaultCardScript     = "skills/exploit-flow-card/scripts/render_card.py"
)

type Runner struct {
	Enabled           bool
	SkillDir          string
	IncludeAttackerCA bool
	CardEnabled       bool
	CardPythonBin     string
	CardTimeout       time.Duration
}

type Case struct {
	CaseID       string
	Chain        string
	TxHash       string
	OutputRoot   string
	IncidentSlug string
	Outcome      string
	PublishTier  string
	PoCState     string
	RCAState     string
	ReportURL    string
	PoCURL       string
	GitHubURL    string
}

type Result struct {
	ReadyToPublish   bool     `json:"ready_to_publish"`
	StatusLabel      string   `json:"status_label"`
	MainPost         string   `json:"main_post"`
	ReplyPost        string   `json:"reply_post"`
	TelegramPost     string   `json:"telegram_post"`
	ImagePath        string   `json:"image_path,omitempty"`
	ImageMode        string   `json:"image_mode"`
	CardBriefPath    string   `json:"card_brief_path,omitempty"`
	CardSVGPath      string   `json:"card_svg_path,omitempty"`
	CardPNGPath      string   `json:"card_png_path,omitempty"`
	CardError        string   `json:"card_error,omitempty"`
	Format           string   `json:"format"`
	ExplorerURL      string   `json:"explorer_url,omitempty"`
	GitHubURL        string   `json:"github_url,omitempty"`
	ReportURL        string   `json:"report_url,omitempty"`
	PoCURL           string   `json:"poc_url,omitempty"`
	Blockers         []string `json:"blockers,omitempty"`
	SourceFormat     string   `json:"source_format_path,omitempty"`
	MainPostPath     string   `json:"main_post_path,omitempty"`
	ReplyPostPath    string   `json:"reply_post_path,omitempty"`
	TelegramPostPath string   `json:"telegram_post_path,omitempty"`
	StatusPath       string   `json:"status_path,omitempty"`
}

type incidentFacts struct {
	Protocol       string
	Chain          string
	TxHash         string
	Occurred       string
	Impact         string
	RootCause      string
	AttackType     string
	WhatHappened   []string
	Analysis       string
	ReportURL      string
	PoCURL         string
	GitHubURL      string
	ExplorerURL    string
	StatusLabel    string
	ImageMode      string
	ImagePath      string
	CardBriefPath  string
	CardSVGPath    string
	CardPNGPath    string
	CardError      string
	ImpactUSD      float64
	ReproducedUSD  float64
	Card           cardFacts
	ReadyToPublish bool
	Blockers       []string
}

type cardFacts struct {
	Subtitle           string
	VulnerablePath     string
	VulnerableContract string
	Vulnerability      string
	ExploitResult      string
	Victim             string
	Impact             string
	AttackerGain       string
	LoopLabel          string
	FlowSteps          []string
	Mechanism          []string
	KeyEvidence        []string
}

func (r *Runner) Configured() bool {
	return r != nil && r.Enabled
}

func (r *Runner) Run(ctx context.Context, c Case) (*Result, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if !r.Configured() {
		return nil, errors.New("x feed runner is not enabled")
	}
	if strings.TrimSpace(c.OutputRoot) == "" {
		return nil, errors.New("output_root is required for x feed draft")
	}

	facts := buildFacts(c)
	r.applyExploitFlowCard(ctx, c, &facts)
	result := renderResult(r, c, facts)
	if _, err := os.Stat(result.SourceFormat); err != nil {
		result.ReadyToPublish = false
		result.Blockers = append(result.Blockers, "x_feed_format_missing")
	}
	if err := writeArtifacts(c.OutputRoot, result); err != nil {
		return nil, err
	}
	return result, nil
}

func buildFacts(c Case) incidentFacts {
	summary := readJSON(filepath.Join(c.OutputRoot, "report_bundle", "report", "run_summary.json"))
	if len(summary) == 0 {
		summary = readJSON(filepath.Join(c.OutputRoot, "summary.json"))
	}
	reportJSON := readJSON(filepath.Join(c.OutputRoot, "report_bundle", "report", "report.json"))
	if len(reportJSON) == 0 {
		reportJSON = readJSON(filepath.Join(c.OutputRoot, "artifacts", "rca", "report.json"))
	}
	report := firstText(
		readFile(filepath.Join(c.OutputRoot, "report_bundle", "report", "REPORT.md")),
		readFile(filepath.Join(c.OutputRoot, "report_bundle", "report", "Report.md")),
		readFile(filepath.Join(c.OutputRoot, "REPORT.md")),
		readFile(filepath.Join(c.OutputRoot, "Report.md")),
	)
	poc := firstText(
		readFile(filepath.Join(c.OutputRoot, "report_bundle", "poc", "PoC.t.sol")),
		readFile(filepath.Join(c.OutputRoot, "PoC.t.sol")),
	)
	allText := strings.Join([]string{string(summary), string(reportJSON), report, poc}, "\n")

	protocol := firstText(
		protocolFromSlug(c.IncidentSlug),
		titleFromSlug(jsonString(summary, "protocol_name", "protocol")),
		titleFromSlug(jsonString(reportJSON, "protocol_name", "protocol")),
		protocolFromReport(report),
		protocolFromSlug(filepath.Base(c.OutputRoot)),
		"Incident",
	)
	protocol = correctProtocolTypos(protocol)
	chain := chainTitle(firstText(c.Chain, jsonString(summary, "chain"), jsonString(reportJSON, "chain"), "unknown"))
	tx := firstText(c.TxHash, jsonString(summary, "tx_hash"), jsonString(reportJSON, "tx_hash"), txHash(allText))
	attackType := attackType(reportJSON)
	impact := impactText(summary, reportJSON)
	impactUSD := impactUSDValue(summary)
	reproducedUSD := reproducedUSDValue(summary)
	occurred := occurredText(summary, reportJSON, poc)
	rootCause := publicRootCause(protocol, reportJSON, c)
	whatHappened := publicWhatHappened(protocol, reportJSON, summary)
	analysis := publicAnalysis(reportJSON, summary)
	explorer := explorerURL(firstText(c.Chain, jsonString(summary, "chain"), jsonString(reportJSON, "chain")), tx)
	statusLabel := statusLabel(c, summary, reportJSON)
	reportURL := strings.TrimSpace(c.ReportURL)
	pocURL := strings.TrimSpace(c.PoCURL)
	githubURL := firstText(c.GitHubURL, reportURL)

	blockers := publishBlockers(reportURL, githubURL)
	return incidentFacts{
		Protocol:       protocol,
		Chain:          chain,
		TxHash:         tx,
		Occurred:       fallback(occurred, "under review"),
		Impact:         fallback(impact, "under review"),
		RootCause:      rootCause,
		AttackType:     attackType,
		WhatHappened:   whatHappened,
		Analysis:       analysis,
		ReportURL:      reportURL,
		PoCURL:         pocURL,
		GitHubURL:      githubURL,
		ExplorerURL:    explorer,
		StatusLabel:    statusLabel,
		ImageMode:      "none",
		ImpactUSD:      impactUSD,
		ReproducedUSD:  reproducedUSD,
		Card:           buildCardFacts(protocol, summary, reportJSON, report, impact, impactUSD, reproducedUSD),
		ReadyToPublish: len(blockers) == 0,
		Blockers:       blockers,
	}
}

func renderResult(r *Runner, c Case, f incidentFacts) *Result {
	for len(f.WhatHappened) < 3 {
		f.WhatHappened = append(f.WhatHappened, "Details remain under review.")
	}
	main := strings.TrimSpace(fmt.Sprintf(`[Backlight %s]

🚨 %s exploit on %s
Tx: %s

🕒 Occurred: %s
💥 Impact: %s

🔎 Root cause:
%s

What happened:
%s
%s
%s

Need more detail? Check our repo and analysis thread below ↓ 🧵`,
		f.StatusLabel,
		f.Protocol,
		f.Chain,
		f.TxHash,
		f.Occurred,
		f.Impact,
		f.RootCause,
		f.WhatHappened[0],
		f.WhatHappened[1],
		f.WhatHappened[2],
	))
	report := fallback(f.ReportURL, "pending")
	poc := pocLine(f.PoCURL, c)
	replyParts := []string{
		"1/ Artifacts + analysis 🧾",
		"",
		"Artifacts:",
		"- Report: " + report,
		"- PoC: " + poc,
		"",
		"Analysis:",
		f.Analysis,
	}
	if f.ExplorerURL != "" {
		replyParts = append(replyParts, "", "Explorer:", f.ExplorerURL)
	}
	if r != nil && r.IncludeAttackerCA {
		// Reserved for the original x-feed reference format. It stays opt-in
		// because the current Backlight public thread omits attacker CA details.
		replyParts = append(replyParts, "", "Attacker CA:", "- under review")
	}
	reply := strings.TrimSpace(strings.Join(replyParts, "\n"))
	telegram := main
	if f.GitHubURL != "" {
		telegram += "\n\nGitHub:\n" + f.GitHubURL
	}

	skillDir := "."
	if r != nil && strings.TrimSpace(r.SkillDir) != "" {
		skillDir = r.SkillDir
	}
	sourceFormat := filepath.Join(skillDir, defaultIncidentFormat)
	return &Result{
		ReadyToPublish: f.ReadyToPublish,
		StatusLabel:    f.StatusLabel,
		MainPost:       main,
		ReplyPost:      reply,
		TelegramPost:   strings.TrimSpace(telegram),
		ImagePath:      f.ImagePath,
		ImageMode:      f.ImageMode,
		CardBriefPath:  f.CardBriefPath,
		CardSVGPath:    f.CardSVGPath,
		CardPNGPath:    f.CardPNGPath,
		CardError:      f.CardError,
		Format:         formatVersion,
		ExplorerURL:    f.ExplorerURL,
		GitHubURL:      f.GitHubURL,
		ReportURL:      f.ReportURL,
		PoCURL:         f.PoCURL,
		Blockers:       f.Blockers,
		SourceFormat:   sourceFormat,
	}
}

func (r *Runner) applyExploitFlowCard(ctx context.Context, c Case, f *incidentFacts) {
	if r == nil || !r.CardEnabled || f == nil {
		return
	}
	script := filepath.Join(r.skillDir(), defaultCardScript)
	if info, err := os.Stat(script); err != nil || info.IsDir() {
		f.ImageMode = "card_script_missing"
		f.CardError = "exploit-flow-card script not found"
		return
	}
	briefPath, err := writeCardBrief(c.OutputRoot, c, *f)
	if err != nil {
		f.ImageMode = "card_brief_failed"
		f.CardError = err.Error()
		return
	}
	f.CardBriefPath = briefPath

	outDir := filepath.Join(c.OutputRoot, "x-feed-visuals")
	pythonBin := strings.TrimSpace(r.CardPythonBin)
	if pythonBin == "" {
		pythonBin = "python3"
	}
	timeout := r.CardTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	cardCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(
		cardCtx,
		pythonBin,
		script,
		briefPath,
		"--out-dir", outDir,
		"--basename", "exploit-flow-card",
		"--title", fmt.Sprintf("%s Exploit Flow", f.Protocol),
	)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := cmd.CombinedOutput()
	svgPath := filepath.Join(outDir, "exploit-flow-card.svg")
	pngPath := filepath.Join(outDir, "exploit-flow-card.png")
	if fileExists(svgPath) {
		f.CardSVGPath = svgPath
	}
	if fileExists(pngPath) {
		f.CardPNGPath = pngPath
		f.ImagePath = pngPath
		f.ImageMode = "exploit_flow_card_png"
		return
	}
	if err != nil {
		f.ImageMode = "card_render_failed"
		f.CardError = strings.TrimSpace(string(bytes.TrimSpace(output)))
		if f.CardError == "" {
			f.CardError = err.Error()
		} else {
			f.CardError = truncateText(f.CardError+": "+err.Error(), 500)
		}
		return
	}
	if f.CardSVGPath != "" {
		f.ImageMode = "exploit_flow_card_svg_only"
		f.CardError = "png_not_rendered"
		return
	}
	f.ImageMode = "card_render_missing_output"
	f.CardError = "exploit-flow-card did not produce PNG or SVG"
}

func (r *Runner) skillDir() string {
	if r != nil && strings.TrimSpace(r.SkillDir) != "" {
		return r.SkillDir
	}
	return "."
}

func writeCardBrief(outputRoot string, c Case, f incidentFacts) (string, error) {
	if strings.TrimSpace(outputRoot) == "" {
		return "", errors.New("output_root is required for card brief")
	}
	path := filepath.Join(outputRoot, "x-feed-card-brief.md")
	reproduced := f.ReproducedUSD
	if reproduced <= 0 {
		reproduced = f.ImpactUSD
	}
	proofKind := "proof under review"
	if c.PoCState == outcome.PoCStateEconomic {
		proofKind = "economic_proof"
	}
	pocStatus := "under review"
	if c.PoCState == outcome.PoCStateEconomic || c.Outcome == outcome.OutcomeVerified || c.Outcome == outcome.OutcomePartial {
		pocStatus = "verified"
	}
	mechanism := append([]string(nil), f.WhatHappened...)
	if len(f.Card.FlowSteps) > 0 {
		mechanism = append([]string(nil), f.Card.FlowSteps...)
	}
	for len(mechanism) < 3 {
		mechanism = append(mechanism, "Details remain under review.")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s Exploit Flow\n\n", f.Protocol)
	fmt.Fprintf(&b, "- **Protocol**: %s\n", f.Protocol)
	fmt.Fprintf(&b, "- **Chain**: %s\n", f.Chain)
	fmt.Fprintf(&b, "- **Estimated loss**: %s\n", moneyForCard(f.ImpactUSD, f.Impact))
	fmt.Fprintf(&b, "- **Attacker gain reproduced**: %s\n", moneyForCard(reproduced, "unknown"))
	if f.Card.Impact != "" {
		fmt.Fprintf(&b, "- **Impact card**: %s\n", f.Card.Impact)
	}
	if f.Card.AttackerGain != "" {
		fmt.Fprintf(&b, "- **Attacker gain card**: %s\n", f.Card.AttackerGain)
	}
	if f.Card.VulnerablePath != "" {
		fmt.Fprintf(&b, "- **Vulnerable path**: %s\n", f.Card.VulnerablePath)
	}
	if f.Card.VulnerableContract != "" {
		fmt.Fprintf(&b, "- **Vulnerable contract**: %s\n", f.Card.VulnerableContract)
	}
	if f.Card.Vulnerability != "" {
		fmt.Fprintf(&b, "- **Vulnerability**: %s\n", f.Card.Vulnerability)
	}
	if f.Card.ExploitResult != "" {
		fmt.Fprintf(&b, "- **Exploit result**: %s\n", f.Card.ExploitResult)
	}
	if f.Card.Victim != "" {
		fmt.Fprintf(&b, "- **Victim**: %s\n", f.Card.Victim)
	}
	if f.Card.LoopLabel != "" {
		fmt.Fprintf(&b, "- **Loop label**: %s\n", f.Card.LoopLabel)
	}
	for i, step := range f.Card.FlowSteps {
		if i >= 4 {
			break
		}
		fmt.Fprintf(&b, "- **Flow step %d**: %s\n", i+1, step)
	}
	fmt.Fprintf(&b, "- **PoC status**: %s\n", pocStatus)
	fmt.Fprintf(&b, "- **Proof kind**: %s\n", proofKind)
	fmt.Fprintf(&b, "- **Tx**: %s\n\n", f.TxHash)
	fmt.Fprintf(&b, "## Root Cause\n\n%s\n\n", firstText(f.Card.Subtitle, f.RootCause))
	fmt.Fprintf(&b, "Mechanism:\n")
	cardMechanism := append([]string(nil), f.Card.Mechanism...)
	if len(cardMechanism) == 0 {
		cardMechanism = mechanism
	}
	for len(cardMechanism) < 3 {
		cardMechanism = append(cardMechanism, "Details remain under review.")
	}
	for _, line := range cardMechanism[:3] {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	fmt.Fprintf(&b, "\nKey evidence:\n")
	if len(f.Card.KeyEvidence) > 0 {
		for _, line := range f.Card.KeyEvidence {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSuffix(line, ".")+".")
		}
	} else {
		fmt.Fprintf(&b, "- Impact scale: %s.\n", f.Impact)
	}
	if f.ReproducedUSD > 0 {
		fmt.Fprintf(&b, "- Economic reproduction matched approximately %s.\n", moneyForCard(f.ReproducedUSD, "unknown"))
	}
	if f.ExplorerURL != "" {
		fmt.Fprintf(&b, "- Explorer: %s.\n", f.ExplorerURL)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func buildCardFacts(protocol string, summary, reportJSON []byte, report, impact string, impactUSD, reproducedUSD float64) cardFacts {
	vulnerablePath := cleanCardText(firstText(
		jsonPathString(reportJSON, "attack_summary", "public_entrypoint_called_per_iteration"),
		markdownField(report, "In short"),
	))
	vulnerability := cleanCardText(firstText(
		jsonPathString(reportJSON, "vulnerability", "title"),
		markdownField(report, "Finding"),
	))
	rootCause := cleanCardText(firstText(
		jsonPathString(reportJSON, "vulnerability", "root_cause"),
		firstNonemptyLine(markdownSection(report, "Root Cause")),
	))
	if vulnerability == "" {
		vulnerability = rootCause
	}

	primaryContract := affectedContractLabel(reportJSON, []string{"primary vulnerable", "vulnerable"})
	victim := affectedContractLabel(reportJSON, []string{"impacted", "victim", "amm pair", "pool", "market", "reserve"})
	if victim == "" || victim == primaryContract {
		victim = firstText(victimFromImpact(summary), "Affected protocol")
	}

	attackerGain := attackerGainCard(summary, reportJSON, reproducedUSD)
	impactCard := firstText(impactCardText(impactUSD, impact, attackerGain), attackerGain, "under review")
	loopLabel := loopLabelFromSummary(reportJSON)
	exploitResult := exploitResultText(vulnerablePath, rootCause, attackerGain)
	steps := cardFlowSteps(vulnerablePath, rootCause, attackerGain)
	mechanism := reportBullets(report, "Mechanism")
	if len(mechanism) == 0 {
		mechanism = cardMechanism(vulnerablePath, rootCause, victim, attackerGain)
	}
	evidence := cardEvidence(report, summary, reportJSON, victim, attackerGain)
	subtitle := firstText(vulnerability, rootCause, fmt.Sprintf("%s exploit path under review", protocol))

	return cardFacts{
		Subtitle:           truncateText(subtitle, 180),
		VulnerablePath:     truncateText(vulnerablePath, 160),
		VulnerableContract: truncateText(primaryContract, 90),
		Vulnerability:      truncateText(firstText(vulnerability, rootCause), 170),
		ExploitResult:      truncateText(exploitResult, 110),
		Victim:             truncateText(victim, 90),
		Impact:             truncateText(impactCard, 60),
		AttackerGain:       truncateText(attackerGain, 60),
		LoopLabel:          truncateText(loopLabel, 80),
		FlowSteps:          steps,
		Mechanism:          mechanism,
		KeyEvidence:        evidence,
	}
}

func affectedContractLabel(reportJSON []byte, roleNeedles []string) string {
	arr, _ := jsonValue(reportJSON, "vulnerability", "affected_contracts").([]any)
	if len(arr) == 0 {
		return ""
	}
	pick := func(requireRole bool) string {
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			role := strings.ToLower(fmt.Sprint(m["role"]))
			if requireRole {
				matched := false
				for _, needle := range roleNeedles {
					if strings.Contains(role, needle) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			if label := contractLabel(m); label != "" {
				return label
			}
		}
		return ""
	}
	if label := pick(true); label != "" {
		return label
	}
	return pick(false)
}

func contractLabel(m map[string]any) string {
	name := cleanCardText(fmt.Sprint(m["name"]))
	address := strings.TrimSpace(fmt.Sprint(m["address"]))
	role := cleanCardText(fmt.Sprint(m["role"]))
	if name == "" || strings.EqualFold(name, "<nil>") || strings.EqualFold(name, "unknown") {
		name = shortAddress(address)
	}
	switch {
	case name != "" && role != "" && !strings.Contains(strings.ToLower(role), "primary vulnerable"):
		return fmt.Sprintf("%s (%s)", name, role)
	case name != "" && address != "":
		return fmt.Sprintf("%s (%s)", name, shortAddress(address))
	case name != "":
		return name
	default:
		return shortAddress(address)
	}
}

func victimFromImpact(summary []byte) string {
	arr, _ := jsonValue(summary, "economic_reproduction", "incident", "asset_legs").([]any)
	if len(arr) == 0 {
		arr, _ = jsonValue(summary, "economic_reproduction", "incident", "drain_legs").([]any)
	}
	symbols := make([]string, 0, 3)
	holder := ""
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if holder == "" {
			holder = shortAddress(fmt.Sprint(m["holder"]))
		}
		symbol := cleanCardText(fmt.Sprint(m["symbol"]))
		if symbol != "" && !containsValue(symbols, symbol) {
			symbols = append(symbols, symbol)
		}
		if len(symbols) >= 3 {
			break
		}
	}
	if holder == "" && len(symbols) == 0 {
		return ""
	}
	if len(symbols) > 0 {
		return "Holder lost " + strings.Join(symbols[:min(len(symbols), 2)], " + ")
	}
	return holder
}

func attackerGainCard(summary, reportJSON []byte, reproducedUSD float64) string {
	symbol := jsonPathString(reportJSON, "impact", "attacker_profit_symbol")
	if symbol == "" {
		symbol = firstGainSymbol(summary)
	}
	if reproducedUSD > 0 {
		if symbol != "" {
			return usdApprox(reproducedUSD) + " " + symbol
		}
		return usdApprox(reproducedUSD)
	}
	formatted := jsonPathString(reportJSON, "impact", "attacker_profit_formatted")
	if formatted != "" && symbol != "" {
		return compactTokenAmount(formatted) + " " + symbol
	}
	return ""
}

func firstGainSymbol(summary []byte) string {
	for _, path := range [][]string{
		{"economic_reproduction", "poc", "included_legs"},
		{"economic_reproduction", "poc", "gain_family_selection", "included_legs"},
	} {
		arr, _ := jsonValue(summary, path...).([]any)
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if symbol := cleanCardText(fmt.Sprint(m["symbol"])); symbol != "" {
				return symbol
			}
		}
	}
	return ""
}

func impactCardText(impactUSD float64, impact, attackerGain string) string {
	if impactUSD > 0 {
		return usdApprox(impactUSD)
	}
	if strings.Contains(impact, "$") {
		return impact
	}
	return attackerGain
}

func loopLabelFromSummary(reportJSON []byte) string {
	loopCount := jsonPathNumber(reportJSON, "attack_summary", "loop_count")
	if loopCount > 1 {
		return fmt.Sprintf("Loop repeats %.0f times", loopCount)
	}
	return "Repeated calls amplify the effect"
}

func exploitResultText(path, rootCause, attackerGain string) string {
	hay := strings.ToLower(path + " " + rootCause)
	switch {
	case strings.Contains(hay, "skim") && strings.Contains(hay, "sync"):
		return "attacker cashes out"
	case strings.Contains(hay, "withdraw"):
		return "withdrawal moves victim assets out"
	case strings.Contains(hay, "buytru") && strings.Contains(hay, "selltru"):
		return "sell path returns excessive ETH"
	case attackerGain != "":
		return "attacker realizes " + attackerGain
	default:
		return "exploit effect remains under review"
	}
}

func cardFlowSteps(path, rootCause, attackerGain string) []string {
	hay := strings.ToLower(path + " " + rootCause)
	switch {
	case strings.Contains(hay, "skim") && strings.Contains(hay, "sync"):
		return []string{
			"1. add extra tokens",
			"2. skim() starts transfer",
			"3. pool balance drops",
			"4. attacker cashes out",
		}
	case strings.Contains(hay, "buytru") && strings.Contains(hay, "selltru"):
		return []string{
			"1. buyTRU",
			"2. hold / approve TRU",
			"3. sellTRU",
			"4. excessive ETH payout",
		}
	case strings.Contains(hay, "withdraw"):
		return []string{
			"1. call withdraw(victim)",
			"2. helper spends allowance",
			"3. victim assets move out",
			"4. attacker-controlled recipient gains",
		}
	}
	parts := splitCallPath(path)
	steps := make([]string, 0, 4)
	for _, part := range parts {
		if len(steps) >= 3 {
			break
		}
		label := publicCallStep(part)
		if label != "" {
			steps = append(steps, fmt.Sprintf("%d. %s", len(steps)+1, label))
		}
	}
	if len(steps) == 0 {
		steps = append(steps, "1. enter exploit path")
	}
	for len(steps) < 3 {
		steps = append(steps, fmt.Sprintf("%d. protocol state changes", len(steps)+1))
	}
	final := "4. value extracted"
	if attackerGain != "" {
		final = "4. attacker receives " + attackerGain
	}
	steps = append(steps, final)
	return steps
}

func publicCallStep(part string) string {
	part = cleanCardText(part)
	lower := strings.ToLower(part)
	switch {
	case part == "":
		return ""
	case strings.Contains(lower, "claimreward"):
		return "claim rewards"
	case strings.Contains(lower, "withdraw"):
		return "call withdraw"
	case strings.Contains(lower, "transferfrom"):
		return "move victim assets"
	case looksLikeRawCall(part):
		return "enter exploit path"
	default:
		label := stripCallArgs(part)
		if strings.EqualFold(label, "execute()") || strings.EqualFold(label, "call()") || strings.EqualFold(label, "fallback()") {
			return "enter exploit path"
		}
		return truncateText(label, 30)
	}
}

func looksLikeRawCall(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if regexp.MustCompile(`^0x[a-f0-9]{6,}`).MatchString(lower) {
		return true
	}
	return regexp.MustCompile(`\((address|uint|bytes|bool|int)`).MatchString(lower)
}

func stripCallArgs(value string) string {
	value = cleanCardText(value)
	if idx := strings.Index(value, "("); idx >= 0 {
		name := strings.TrimSpace(value[:idx])
		if name == "" {
			return "entry call"
		}
		return name + "()"
	}
	return value
}

func splitCallPath(path string) []string {
	raw := strings.FieldsFunc(path, func(r rune) bool {
		return r == '>' || r == '→'
	})
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		part = strings.Trim(strings.TrimSpace(part), "- ")
		if part != "" {
			parts = append(parts, cleanCardText(part))
		}
	}
	return parts
}

func cardMechanism(path, rootCause, victim, attackerGain string) []string {
	out := make([]string, 0, 3)
	if path != "" {
		out = append(out, "The attacker drove the path: "+path+".")
	}
	if rootCause != "" {
		out = append(out, rootCause)
	}
	if victim != "" || attackerGain != "" {
		out = append(out, strings.TrimSpace(fmt.Sprintf("The affected target was %s; attacker gain was %s.", fallback(victim, "under review"), fallback(attackerGain, "under review"))))
	}
	return out
}

func cardEvidence(report string, summary, reportJSON []byte, victim, attackerGain string) []string {
	evidence := reportBullets(report, "Key evidence")
	if len(evidence) > 0 {
		if len(evidence) > 3 {
			return evidence[:3]
		}
		return evidence
	}
	out := make([]string, 0, 3)
	if victim != "" {
		out = append(out, "Affected target: "+victim)
	}
	if attackerGain != "" {
		out = append(out, "Attacker gain reproduced: "+attackerGain)
	}
	if proof := jsonPathString(summary, "economic_reproduction", "verdict"); proof != "" {
		out = append(out, "Economic reproduction verdict: "+proof)
	}
	if len(out) == 0 {
		if title := jsonPathString(reportJSON, "vulnerability", "title"); title != "" {
			out = append(out, "RCA finding: "+title)
		}
	}
	return out
}

func markdownField(text, label string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	re := regexp.MustCompile(`(?m)^\s*-\s+\*\*` + regexp.QuoteMeta(label) + `\*\*:\s*(.+?)\s*$`)
	if match := re.FindStringSubmatch(text); len(match) == 2 {
		return cleanCardText(match[1])
	}
	return ""
}

func markdownSection(text, heading string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	re := regexp.MustCompile(`(?m)^##\s+` + regexp.QuoteMeta(heading) + `\s*$`)
	match := re.FindStringIndex(text)
	if match == nil {
		return ""
	}
	start := match[1]
	next := regexp.MustCompile(`(?m)^##\s+`).FindStringIndex(text[start:])
	end := len(text)
	if next != nil {
		end = start + next[0]
	}
	return strings.TrimSpace(text[start:end])
}

func reportBullets(text, label string) []string {
	lines := strings.Split(text, "\n")
	target := strings.ToLower(label) + ":"
	out := make([]string, 0, 3)
	inBlock := false
	for _, line := range lines {
		stripped := strings.TrimSpace(line)
		if !inBlock {
			if strings.EqualFold(stripped, target) {
				inBlock = true
			}
			continue
		}
		if strings.HasPrefix(stripped, "- ") {
			cleaned := cleanCardText(strings.TrimPrefix(stripped, "- "))
			if cleaned != "" {
				out = append(out, cleaned)
			}
			continue
		}
		if stripped == "" {
			if len(out) > 0 {
				break
			}
			continue
		}
		if strings.HasSuffix(stripped, ":") || strings.HasPrefix(stripped, "## ") {
			break
		}
	}
	return out
}

func firstNonemptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = cleanCardText(strings.TrimSpace(strings.TrimPrefix(line, "-")))
		if line != "" && !strings.HasSuffix(line, ":") {
			return line
		}
	}
	return ""
}

func cleanCardText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "<nil>") {
		return ""
	}
	value = regexp.MustCompile("\\[`?([^`\\]]+)`?\\]\\([^)]+\\)").ReplaceAllString(value, "$1")
	value = strings.ReplaceAll(value, "`", "")
	value = regexp.MustCompile(`\s+`).ReplaceAllString(value, " ")
	value = strings.Trim(value, "_*")
	return correctProtocolTypos(strings.TrimSpace(value))
}

func containsValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func correctProtocolTypos(value string) string {
	replacer := strings.NewReplacer(
		"Pnacakeswap", "Pancakeswap",
		"pnacakeswap", "pancakeswap",
		"PNACAKESWAP", "PANCAKESWAP",
	)
	return replacer.Replace(value)
}

func shortAddress(address string) string {
	address = strings.TrimSpace(address)
	if len(address) == 42 && strings.HasPrefix(strings.ToLower(address), "0x") {
		return address[:6] + "..." + address[len(address)-4:]
	}
	return address
}

func compactTokenAmount(value string) string {
	n, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(value), ",", ""), 64)
	if err != nil || n <= 0 {
		return cleanCardText(value)
	}
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", n/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", n/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.2fK", n/1_000)
	default:
		return fmt.Sprintf("%.4g", n)
	}
}

func writeArtifacts(outputRoot string, result *Result) error {
	if result == nil {
		return nil
	}
	paths := map[string]string{
		"x-feed-main-post.txt":     result.MainPost,
		"x-feed-reply-post.txt":    result.ReplyPost,
		"x-feed-telegram-post.txt": result.TelegramPost,
	}
	for rel, text := range paths {
		path := filepath.Join(outputRoot, rel)
		if err := os.WriteFile(path, []byte(strings.TrimRight(text, "\n")+"\n"), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
		switch rel {
		case "x-feed-main-post.txt":
			result.MainPostPath = path
		case "x-feed-reply-post.txt":
			result.ReplyPostPath = path
		case "x-feed-telegram-post.txt":
			result.TelegramPostPath = path
		}
	}
	result.StatusPath = filepath.Join(outputRoot, "x-feed-status.json")
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(result.StatusPath, append(data, '\n'), 0o644)
}

func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func readJSON(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil || !json.Valid(data) {
		return nil
	}
	return data
}

func jsonValue(data []byte, path ...string) any {
	if len(data) == 0 {
		return nil
	}
	var cur any
	if json.Unmarshal(data, &cur) != nil {
		return nil
	}
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	return cur
}

func jsonString(data []byte, keys ...string) string {
	if len(data) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(data, &v) != nil {
		return ""
	}
	wants := make(map[string]bool, len(keys))
	for _, key := range keys {
		wants[strings.ToLower(key)] = true
	}
	var walk func(any) string
	walk = func(node any) string {
		switch x := node.(type) {
		case map[string]any:
			for key, value := range x {
				if wants[strings.ToLower(key)] {
					if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
						return strings.TrimSpace(s)
					}
				}
			}
			for _, value := range x {
				if s := walk(value); s != "" {
					return s
				}
			}
		case []any:
			for _, value := range x {
				if s := walk(value); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return walk(v)
}

func jsonPathString(data []byte, path ...string) string {
	value, ok := jsonValue(data, path...).(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func jsonPathNumber(data []byte, path ...string) float64 {
	switch v := jsonValue(data, path...).(type) {
	case float64:
		return v
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return n
	default:
		return 0
	}
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func fallback(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return strings.TrimSpace(value)
}

func protocolFromSlug(slug string) string {
	slug = strings.Trim(strings.ToLower(strings.TrimSpace(slug)), "_-/ ")
	if slug == "" {
		return ""
	}
	parts := strings.FieldsFunc(slug, func(r rune) bool { return r == '_' || r == '-' || r == '/' })
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || regexp.MustCompile(`^\d+$`).MatchString(part) {
			continue
		}
		switch part {
		case "eth", "ethereum", "bsc", "bnb", "arb", "arbitrum", "base", "polygon", "optimism", "op", "avax", "unknown", "case", "incident", "lumos", "lumoskit", "run":
			continue
		default:
			filtered = append(filtered, part)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	return titleFromSlug(strings.Join(filtered, "_"))
}

func titleFromSlug(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '_' || r == '-' || r == '/' || r == '.'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if containsUpper(part) && strings.ToLower(part) != part && strings.ToUpper(part) != part {
			out = append(out, part)
			continue
		}
		upper := strings.ToUpper(part)
		if upper == part || len(part) <= 4 {
			out = append(out, upper[:1]+strings.ToLower(upper[1:]))
			continue
		}
		out = append(out, strings.ToUpper(part[:1])+part[1:])
	}
	return strings.Join(out, " ")
}

func containsUpper(value string) bool {
	for _, r := range value {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func protocolFromReport(report string) string {
	for _, line := range strings.Split(report, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if strings.HasPrefix(strings.ToLower(line), "protocol:") {
			return titleFromSlug(strings.TrimSpace(strings.TrimPrefix(line, "Protocol:")))
		}
	}
	return ""
}

func chainTitle(chain string) string {
	switch strings.ToLower(strings.TrimSpace(chain)) {
	case "eth", "ethereum", "mainnet":
		return "Ethereum"
	case "bsc", "bnb", "bnb chain", "binance smart chain":
		return "BSC"
	case "arb", "arbitrum":
		return "Arbitrum"
	case "op", "optimism":
		return "Optimism"
	case "avax", "avalanche":
		return "Avalanche"
	case "polygon", "matic":
		return "Polygon"
	case "base":
		return "Base"
	default:
		return titleFromSlug(chain)
	}
}

func txHash(text string) string {
	re := regexp.MustCompile(`0x[0-9a-fA-F]{64}`)
	return re.FindString(text)
}

func attackType(reportJSON []byte) string {
	category := jsonPathString(reportJSON, "vulnerability", "category")
	subtype := jsonPathString(reportJSON, "vulnerability", "subtype")
	switch {
	case category != "" && subtype != "":
		return category + " / " + subtype
	case category != "":
		return category
	case subtype != "":
		return subtype
	default:
		return "under review"
	}
}

func impactText(summary, reportJSON []byte) string {
	for _, n := range []float64{
		jsonPathNumber(summary, "economic_reproduction", "incident", "net_loss_usd"),
		jsonPathNumber(summary, "economic_reproduction", "incident", "drained_usd"),
	} {
		if n > 0 {
			return "approximately " + usdApprox(n)
		}
	}
	if formatted := jsonPathString(reportJSON, "impact", "attacker_profit_formatted"); formatted != "" {
		symbol := jsonPathString(reportJSON, "impact", "attacker_profit_symbol")
		return strings.TrimSpace("approximately " + formatted + " " + symbol)
	}
	return ""
}

func impactUSDValue(summary []byte) float64 {
	for _, n := range []float64{
		jsonPathNumber(summary, "economic_reproduction", "incident", "net_loss_usd"),
		jsonPathNumber(summary, "economic_reproduction", "incident", "drained_usd"),
	} {
		if n > 0 {
			return n
		}
	}
	return 0
}

func reproducedUSDValue(summary []byte) float64 {
	for _, n := range []float64{
		jsonPathNumber(summary, "economic_reproduction", "poc", "expected_reproduced_usd"),
		jsonPathNumber(summary, "economic_reproduction", "poc", "net_reproduced_usd"),
		jsonPathNumber(summary, "economic_reproduction", "poc", "attacker_gain_usd"),
		jsonPathNumber(summary, "economic_reproduction", "comparison", "profit_oracle_expected_usd"),
	} {
		if n > 0 {
			return n
		}
	}
	return 0
}

func moneyForCard(value float64, fallbackText string) string {
	if value > 0 {
		return fmt.Sprintf("$%.2f", value)
	}
	return fallback(fallbackText, "unknown")
}

func usdApprox(value float64) string {
	abs := math.Abs(value)
	switch {
	case abs >= 1_000_000_000:
		return fmt.Sprintf("~$%.1fB", value/1_000_000_000)
	case abs >= 1_000_000:
		return fmt.Sprintf("~$%.1fM", value/1_000_000)
	case abs >= 1_000:
		return fmt.Sprintf("~$%.1fK", value/1_000)
	default:
		return fmt.Sprintf("~$%.0f", value)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func truncateText(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if max <= 0 || len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return strings.TrimSpace(value[:max-3]) + "..."
}

func occurredText(summary, reportJSON []byte, poc string) string {
	for _, ts := range []float64{
		jsonPathNumber(summary, "economic_reproduction", "pricing", "tx_timestamp"),
		jsonPathNumber(summary, "tx_timestamp"),
		jsonPathNumber(reportJSON, "tx_timestamp"),
	} {
		if ts > 0 {
			return time.Unix(int64(ts), 0).UTC().Format("2006-01-02 15:04 UTC")
		}
	}
	re := regexp.MustCompile(`TX_TIMESTAMP\s*=\s*(\d+)`)
	if match := re.FindStringSubmatch(poc); len(match) == 2 {
		if ts, err := strconv.ParseInt(match[1], 10, 64); err == nil && ts > 0 {
			return time.Unix(ts, 0).UTC().Format("2006-01-02 15:04 UTC")
		}
	}
	return ""
}

func publicRootCause(protocol string, reportJSON []byte, c Case) string {
	category := strings.ToLower(attackType(reportJSON))
	partial := c.Outcome == outcome.OutcomePartial || strings.Contains(strings.ToLower(c.RCAState), "partial") || jsonPathString(reportJSON, "analysis_status") == "partial"
	if strings.Contains(category, "pricing") || strings.Contains(category, "accounting") || strings.Contains(category, "settlement") {
		if partial {
			return fmt.Sprintf("Evidence points to a pricing/accounting mismatch in the %s market settlement path. The exact formula or missing guard remains under review.", protocol)
		}
		return fmt.Sprintf("A pricing/accounting mismatch in the %s market settlement path let payouts exceed the checked reserve state.", protocol)
	}
	title := jsonPathString(reportJSON, "vulnerability", "title")
	if title != "" && !partial {
		return sanitizeSentence(title)
	}
	if partial {
		return "Root cause remains under review."
	}
	return sanitizeSentence(firstText(jsonPathString(reportJSON, "vulnerability", "root_cause"), "Root cause remains under review."))
}

func publicWhatHappened(protocol string, reportJSON, summary []byte) []string {
	category := strings.ToLower(attackType(reportJSON))
	proofVerified := strings.EqualFold(jsonPathString(summary, "poc", "status"), "verified") ||
		strings.EqualFold(jsonPathString(summary, "economic_reproduction", "status"), "pass")
	if strings.Contains(category, "pricing") || strings.Contains(category, "accounting") || strings.Contains(category, "settlement") {
		line3 := "That mismatch let value leave the affected reserve."
		if proofVerified {
			line3 = "That mismatch let ETH leave the market reserve, and the economic PoC reproduced the observed loss scale."
		}
		return []string{
			fmt.Sprintf("An attacker-controlled setup repeatedly interacted with a %s market path.", protocol),
			"The protocol assumption under review is that settlement accounting would keep ETH payouts bounded by market reserves.",
			line3,
		}
	}
	line3 := "That mismatch created an extractable value movement."
	if proofVerified {
		line3 = "The economic PoC reproduced the observed value movement at incident scale."
	}
	return []string{
		fmt.Sprintf("An attacker-controlled setup interacted with a %s protocol path.", protocol),
		"The protocol assumption under review is that validation and accounting checks would keep the state bounded.",
		line3,
	}
}

func publicAnalysis(reportJSON, summary []byte) string {
	category := strings.ToLower(attackType(reportJSON))
	if strings.Contains(category, "pricing") || strings.Contains(category, "accounting") || strings.Contains(category, "settlement") {
		return "At a high level, this is a market interaction and settlement-accounting issue. ETH moved out of the market reserve while the reproduced PoC matched the observed loss scale. The exact pricing formula or missing guard remains under review."
	}
	if strings.EqualFold(jsonPathString(summary, "poc", "status"), "verified") {
		return "At a high level, this is a protocol-state assumption issue. The reproduced PoC matched the observed economic effect, while lower-level implementation details remain under review."
	}
	return "At a high level, this is an incident under review. The public thread keeps to observed behavior and avoids reproduction details."
}

func sanitizeSentence(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	address := regexp.MustCompile(`0x[0-9a-fA-F]{40}`)
	text = address.ReplaceAllString(text, "the affected contract")
	selectors := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*\([^)]*\)`)
	text = selectors.ReplaceAllString(text, "the relevant protocol path")
	if len(text) > 240 {
		text = text[:237] + "..."
	}
	return text
}

func explorerURL(chain, tx string) string {
	tx = strings.TrimSpace(tx)
	if tx == "" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(chain)) {
	case "eth", "ethereum", "mainnet":
		return "https://etherscan.io/tx/" + tx
	case "base":
		return "https://basescan.org/tx/" + tx
	case "arb", "arbitrum":
		return "https://arbiscan.io/tx/" + tx
	case "op", "optimism":
		return "https://optimistic.etherscan.io/tx/" + tx
	case "bsc", "bnb", "bnb chain":
		return "https://bscscan.com/tx/" + tx
	case "polygon", "matic":
		return "https://polygonscan.com/tx/" + tx
	default:
		return ""
	}
}

func statusLabel(c Case, summary, reportJSON []byte) string {
	if c.Outcome == outcome.OutcomePartial || c.PublishTier == outcome.PublishTierEconomicIncompleteRCA || (c.RCAState != "" && c.RCAState != outcome.RCAStateComplete) || jsonPathString(reportJSON, "analysis_status") == "partial" {
		return "Initial Analysis"
	}
	if c.Outcome == outcome.OutcomeVerified && strings.EqualFold(jsonPathString(summary, "poc", "status"), "verified") {
		return "Verified Incident"
	}
	if c.Outcome == outcome.OutcomeVerified {
		return "Verified Incident"
	}
	return "Incident Alert"
}

func publishBlockers(reportURL, githubURL string) []string {
	var blockers []string
	if strings.TrimSpace(reportURL) == "" {
		blockers = append(blockers, "report_public_url_missing")
	}
	if strings.TrimSpace(githubURL) == "" {
		blockers = append(blockers, "github_public_url_missing")
	}
	return blockers
}

func pocLine(url string, c Case) string {
	if strings.TrimSpace(url) != "" {
		return strings.TrimSpace(url)
	}
	if c.PoCState == outcome.PoCStateEconomic || c.Outcome == outcome.OutcomeVerified || c.Outcome == outcome.OutcomePartial {
		return "withheld until review"
	}
	return "reproducibility pending"
}
