package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TelegramChannel posts a short text message via Telegram Bot API
// sendMessage. APIBase defaults to https://api.telegram.org so tests can
// override it.
type TelegramChannel struct {
	BotToken string
	ChatID   string
	APIBase  string
	Client   *http.Client
}

func (t *TelegramChannel) Name() string { return "telegram" }

type telegramRequest struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

func (t *TelegramChannel) endpoint() string {
	base := t.APIBase
	if base == "" {
		base = "https://api.telegram.org"
	}
	base = strings.TrimRight(base, "/")
	return fmt.Sprintf("%s/bot%s/sendMessage", base, t.BotToken)
}

func (t *TelegramChannel) Deliver(ctx context.Context, p Payload) (int, error) {
	text := p.TelegramText
	if text == "" {
		text = renderTelegramText(p)
	}
	body, err := json.Marshal(telegramRequest{
		ChatID: t.ChatID,
		Text:   text,
	})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint(), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "backlight/1 notify=telegram")
	resp, err := t.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("telegram sendMessage returned HTTP %d", resp.StatusCode)
}

// renderTelegramText keeps the message compact while separating the
// operator-facing diagnosis from the stored lifecycle event/outcome. This is
// important for legacy runs whose stored outcome may be engine_error even when
// the terminal payload proves an analysis blocker such as rca_blocked.
func renderTelegramText(p Payload) string {
	tx := p.TxHash
	if len(tx) > 14 {
		tx = tx[:10] + "…" + tx[len(tx)-4:]
	}
	diagnosis, title, reason := telegramDiagnosis(p)
	var b strings.Builder
	fmt.Fprintf(&b, "[Backlight] %s", title)
	fmt.Fprintf(&b, "\n\nIncident: %s on %s", incidentTitle(p), chainTitle(p.Chain))
	fmt.Fprintf(&b, "\nTx: %s", tx)
	fmt.Fprintf(&b, "\nResult: %s", resultSummary(p, diagnosis))
	if reasonForMessage(diagnosis, reason) != "" {
		fmt.Fprintf(&b, "\nReason: %s", truncateText(reasonForMessage(diagnosis, reason), 180))
	}
	// Engine-error triage lines: let an operator decide the next action from
	// Telegram alone (who owns it, is it worth retrying, what to check).
	if diagnosis == "engine_error" {
		if p.DiagnosisOwner != "" {
			fmt.Fprintf(&b, "\nOwner: %s", p.DiagnosisOwner)
		}
		if p.DiagnosisRetryable != nil {
			fmt.Fprintf(&b, "\nRetryable: %s", yesNo(*p.DiagnosisRetryable))
		}
		if p.DiagnosisAction != "" {
			fmt.Fprintf(&b, "\nAction: %s", truncateText(p.DiagnosisAction, 200))
		}
		if p.CaseID != "" {
			fmt.Fprintf(&b, "\nCase: %s", p.CaseID)
		}
	}
	if p.ReportURL != "" {
		fmt.Fprintf(&b, "\nReport: %s", p.ReportURL)
	}
	if completed := completedTime(p.CompletedAt); completed != "" {
		fmt.Fprintf(&b, "\nCompleted: %s", completed)
	}
	return b.String()
}

func telegramDiagnosis(p Payload) (key, title, reason string) {
	reason = p.RerunReason
	switch p.AnalysisStage {
	case "success":
		return "success", "Success", firstText(reason, "verified_result")
	case "reachable_poc":
		return "reachable_poc", "Reachable PoC", firstText(reason, "economic_proof_incomplete")
	case "rca_blocked":
		if rcaIsPartial(p) {
			return "partial", "Partial result", firstText(rcaReason(p), nonGenericReason(reason))
		}
		return "rca_blocked", "RCA blocked", firstText(rcaReason(p), reason)
	case "poc_blocked":
		return "poc_blocked", "PoC blocked", reason
	case "poc_failed":
		return "poc_failed", "PoC failed", reason
	case "engine_error":
		switch failureKindText(p) {
		case "agent_poc_agent_runtime_error":
			return "agent_poc_agent_runtime_error", "Agent PoC runtime error", engineErrorReason(p)
		case "rca_agent_runtime_error":
			return "rca_agent_runtime_error", "RCA agent runtime error", engineErrorReason(p)
		}
		return "engine_error", "Engine error", firstText(reason, failureKindText(p))
	}
	switch p.Outcome {
	case "verified":
		return "success", "Success", "verified_result"
	case "partial":
		return "partial", "Partial result", reason
	case "unverified":
		return "poc_failed", "PoC failed", firstText(reason, failureKindText(p))
	case "engine_error":
		switch failureKindText(p) {
		case "agent_poc_agent_runtime_error":
			return "agent_poc_agent_runtime_error", "Agent PoC runtime error", engineErrorReason(p)
		case "rca_agent_runtime_error":
			return "rca_agent_runtime_error", "RCA agent runtime error", engineErrorReason(p)
		}
		return "engine_error", "Engine error", firstText(reason, failureKindText(p))
	}
	return firstText(p.Outcome, p.Event, "unknown"), firstText(p.Outcome, p.Event, "Unknown"), reason
}

func engineErrorReason(p Payload) string {
	return firstText(p.FailureDetailKind, p.FailureMessage, p.RerunReason, failureKindText(p))
}

func failureKindText(p Payload) string {
	if p.FailureKind == nil {
		return ""
	}
	return *p.FailureKind
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func resultSummary(p Payload, diagnosis string) string {
	decision := decisionText(p.RerunDecision)
	if component := componentResultSummary(p); component != "" && !isEngineDiagnosis(diagnosis) {
		return joinTelegramParts(component, decision)
	}
	switch diagnosis {
	case "success":
		return joinTelegramParts("verified", decision)
	case "reachable_poc":
		return joinTelegramParts("reachable PoC, economic proof incomplete", decision)
	case "rca_blocked":
		return joinTelegramParts("RCA blocked", decision)
	case "poc_blocked":
		return joinTelegramParts("PoC blocked", decision)
	case "poc_failed":
		return joinTelegramParts("PoC failed", decision)
	case "agent_poc_agent_runtime_error":
		return joinTelegramParts("Agent PoC runtime error", decision)
	case "rca_agent_runtime_error":
		return joinTelegramParts("RCA agent runtime error", decision)
	case "engine_error":
		return joinTelegramParts("engine error", decision)
	case "partial":
		return joinTelegramParts("partial", decision)
	default:
		return firstText(joinTelegramParts(diagnosis, decision), diagnosis, p.Outcome, p.Event, "unknown")
	}
}

func isEngineDiagnosis(diagnosis string) bool {
	switch diagnosis {
	case "agent_poc_agent_runtime_error", "rca_agent_runtime_error", "engine_error":
		return true
	default:
		return false
	}
}

func componentResultSummary(p Payload) string {
	parts := make([]string, 0, 2)
	if text := pocResultText(p); text != "" {
		parts = append(parts, text)
	}
	if text := rcaResultText(p); text != "" {
		parts = append(parts, text)
	}
	return strings.Join(parts, "; ")
}

func pocResultText(p Payload) string {
	if p.PoCState == "" && p.PoCStatus == "" && p.PoCProofKind == "" && p.PoCForgeTestStatus == "" && p.PoCFailureKind == "" {
		return ""
	}
	status := pocResultStatus(p)
	details := make([]string, 0, 4)
	if proof := proofKindLabel(p.PoCProofKind); proof != "" {
		details = append(details, proof)
	}
	if test := forgeStatusLabel("forge test", p.PoCForgeTestStatus); test != "" {
		details = append(details, test)
	}
	if build := forgeStatusLabel("forge build", p.PoCForgeBuildStatus); build != "" && p.PoCForgeTestStatus == "" {
		details = append(details, build)
	}
	if p.PoCFailureKind != "" {
		details = append(details, humanizeToken(p.PoCFailureKind))
	}
	return componentText("PoC", status, details)
}

func pocResultStatus(p Payload) string {
	switch strings.TrimSpace(strings.ToLower(p.PoCState)) {
	case "reachable_poc":
		return "reachable"
	case "poc_failed":
		return "failed"
	case "poc_missing":
		return "missing"
	case "poc_unknown":
		return "unknown"
	}
	return firstText(pocStatusLabel(p.PoCStatus), pocStateLabel(p.PoCState), "unknown")
}

func rcaResultText(p Payload) string {
	if p.RCAState == "" && p.RCAStatus == "" && p.RCAAnalysisStatus == "" && p.RCABlockerCode == "" && p.RCABlockerReason == "" {
		return ""
	}
	status := firstText(rcaStatusLabel(p.RCAStatus), rcaStatusLabel(p.RCAAnalysisStatus), rcaStateLabel(p.RCAState), "unknown")
	details := make([]string, 0, 3)
	if state := rcaStateLabel(p.RCAState); state != "" && state != status {
		details = append(details, state)
	}
	if blocker := firstText(p.RCABlockerCode, p.RCABlockerReason); blocker != "" {
		details = append(details, humanizeToken(blocker))
	}
	return componentText("RCA", status, details)
}

func componentText(name, status string, details []string) string {
	if len(details) == 0 {
		return name + " " + status
	}
	return fmt.Sprintf("%s %s (%s)", name, status, strings.Join(details, ", "))
}

func pocStatusLabel(status string) string {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "verified":
		return "verified"
	case "unverified":
		return "unverified"
	case "missing":
		return "missing"
	case "blocked":
		return "blocked"
	default:
		return humanizeToken(status)
	}
}

func pocStateLabel(state string) string {
	switch strings.TrimSpace(strings.ToLower(state)) {
	case "economic_poc":
		return "verified"
	case "reachable_poc":
		return "reachable"
	case "poc_failed":
		return "failed"
	case "poc_missing":
		return "missing"
	case "poc_unknown":
		return "unknown"
	default:
		return humanizeToken(state)
	}
}

func rcaStatusLabel(status string) string {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "complete", "pass", "verified":
		return "complete"
	case "partial":
		return "partial"
	case "blocked":
		return "blocked"
	case "missing", "not_run":
		return "not run"
	default:
		return humanizeToken(status)
	}
}

func rcaStateLabel(state string) string {
	switch strings.TrimSpace(strings.ToLower(state)) {
	case "rca_complete":
		return "complete"
	case "rca_scope_limited":
		return "scope limited"
	case "rca_low_confidence":
		return "low confidence"
	case "rca_missing_evidence":
		return "missing evidence"
	case "rca_poc_dependent":
		return "PoC dependent"
	case "rca_runtime_error":
		return "runtime error"
	case "rca_conflicting_evidence":
		return "conflicting evidence"
	case "rca_not_run":
		return "not run"
	case "rca_unknown":
		return "unknown"
	default:
		return humanizeToken(state)
	}
}

func proofKindLabel(proof string) string {
	switch strings.TrimSpace(strings.ToLower(proof)) {
	case "economic_proof":
		return "economic proof"
	case "reachability_only":
		return "reachability only"
	default:
		return humanizeToken(proof)
	}
}

func forgeStatusLabel(name, status string) string {
	status = strings.TrimSpace(strings.ToLower(status))
	if status == "" {
		return ""
	}
	return name + " " + status
}

func rcaIsPartial(p Payload) bool {
	return p.RCAStatus == "partial" || p.RCAAnalysisStatus == "partial" || p.RCAState == "rca_scope_limited"
}

func rcaReason(p Payload) string {
	return firstText(p.RCABlockerCode, p.RCABlockerReason, nonGenericReason(p.RerunReason), p.RCAState, p.RCAAnalysisStatus, p.RCAStatus)
}

func nonGenericReason(reason string) string {
	switch strings.TrimSpace(strings.ToLower(reason)) {
	case "", "partial", "blocked":
		return ""
	default:
		return reason
	}
}

func humanizeToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "_", " ")
	value = strings.ReplaceAll(value, "-", " ")
	return value
}

func decisionText(decision string) string {
	switch decision {
	case "no_rerun":
		return "no rerun"
	case "auto_rerun":
		return "auto rerun"
	case "guided_repair":
		return "guided repair"
	case "manual_review":
		return "manual review"
	default:
		return ""
	}
}

func reasonForMessage(diagnosis, reason string) string {
	if diagnosis == "success" || reason == "verified_result" {
		return ""
	}
	return reason
}

func incidentTitle(p Payload) string {
	slug := p.IncidentSlug
	if slug == "" {
		slug = incidentSlugFromCaseID(p.CaseID)
	}
	if title := titleFromSlug(slug); title != "" {
		return title
	}
	return "Incident"
}

func incidentSlugFromCaseID(caseID string) string {
	parts := strings.Split(strings.TrimPrefix(caseID, "case_"), "_")
	if len(parts) == 0 {
		return ""
	}
	start := 0
	if len(parts[start]) == 6 && allDigits(parts[start]) {
		start++
	}
	if start < len(parts) && isChainToken(parts[start]) {
		start++
	}
	end := len(parts)
	for i := start; i < len(parts); i++ {
		if isAttemptToken(parts[i]) {
			end = i
			break
		}
	}
	if start >= end {
		return ""
	}
	return strings.Join(parts[start:end], "_")
}

func titleFromSlug(slug string) string {
	parts := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(slug)), func(r rune) bool {
		return r == '_' || r == '-' || r == '/'
	})
	if len(parts) == 0 {
		return ""
	}
	start := 0
	if len(parts[start]) == 6 && allDigits(parts[start]) {
		start++
	}
	if start < len(parts) && isChainToken(parts[start]) {
		start++
	}
	words := make([]string, 0, len(parts)-start)
	for _, part := range parts[start:] {
		if part == "" || isAttemptToken(part) {
			continue
		}
		words = append(words, titleWord(part))
	}
	return strings.Join(words, " ")
}

func titleWord(word string) string {
	switch word {
	case "atm", "bsc", "fpc", "sea", "usdt", "usd", "btc", "eth":
		return strings.ToUpper(word)
	}
	if word == "" {
		return ""
	}
	return strings.ToUpper(word[:1]) + word[1:]
}

func chainTitle(chain string) string {
	switch strings.ToLower(strings.TrimSpace(chain)) {
	case "eth", "ethereum":
		return "Ethereum"
	case "bsc", "bnb", "bnb_chain":
		return "BSC"
	default:
		if chain == "" {
			return "unknown chain"
		}
		return chain
	}
}

func completedTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func truncateText(value string, max int) string {
	if max <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return strings.TrimSpace(string(runes[:max-1])) + "…"
}

func joinTelegramParts(values ...string) string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return strings.Join(out, " · ")
}

func isAttemptToken(value string) bool {
	if len(value) < 2 || value[0] != 'a' {
		return false
	}
	return allDigits(value[1:])
}

func isChainToken(value string) bool {
	switch value {
	case "eth", "ethereum", "bsc", "bnb", "bnbchain", "bnb_chain":
		return true
	default:
		return false
	}
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func firstText(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
