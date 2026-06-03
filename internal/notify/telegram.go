package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	body, err := json.Marshal(telegramRequest{
		ChatID: t.ChatID,
		Text:   renderTelegramText(p),
	})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint(), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "helios/1 notify=telegram")
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

// renderTelegramText is a deliberately small text. The seed wants only
// case_id / chain / tx_hash / outcome / failure_kind / link-level info.
func renderTelegramText(p Payload) string {
	tx := p.TxHash
	if len(tx) > 14 {
		tx = tx[:10] + "…" + tx[len(tx)-4:]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[helios] %s", p.Event)
	if p.Outcome != "" {
		fmt.Fprintf(&b, " outcome=%s", p.Outcome)
	}
	if p.AnalysisStage != "" {
		fmt.Fprintf(&b, " stage=%s", p.AnalysisStage)
	}
	if p.CompletedAt != "" {
		fmt.Fprintf(&b, "\ncompleted_at=%s", p.CompletedAt)
	}
	fmt.Fprintf(&b, "\ncase_id=%s", p.CaseID)
	fmt.Fprintf(&b, "\nchain=%s tx=%s", p.Chain, tx)
	fmt.Fprintf(&b, "\nstate=%s outcome=%s", p.State, p.Outcome)
	if p.FailureKind != nil && *p.FailureKind != "" {
		fmt.Fprintf(&b, " failure_kind=%s", *p.FailureKind)
	}
	if p.SummaryStatus != "" {
		fmt.Fprintf(&b, "\nsummary_status=%s", p.SummaryStatus)
	}
	if p.AnalysisStage != "" {
		fmt.Fprintf(&b, "\nanalysis_stage=%s", p.AnalysisStage)
	}
	if p.RerunDecision != "" {
		fmt.Fprintf(&b, "\nrerun_decision=%s", p.RerunDecision)
		if p.RerunReason != "" {
			fmt.Fprintf(&b, " reason=%s", p.RerunReason)
		}
	}
	if p.AutoRerunResumeStage != "" {
		fmt.Fprintf(&b, "\nauto_rerun_resume_stage=%s", p.AutoRerunResumeStage)
	}
	if p.AutoRerunEligible != nil {
		fmt.Fprintf(&b, "\nauto_rerun_eligible=%t", *p.AutoRerunEligible)
	}
	if p.ResumeStage != "" || p.LumoskitStage != "" {
		fmt.Fprintf(&b, "\nresume_stage=%s lumoskit_stage=%s", p.ResumeStage, p.LumoskitStage)
	}
	if p.ReportURL != "" {
		fmt.Fprintf(&b, "\nreport_url=%s", p.ReportURL)
	}
	if p.PoCURL != "" {
		fmt.Fprintf(&b, "\npoc_url=%s", p.PoCURL)
	}
	if p.CommitURL != "" {
		fmt.Fprintf(&b, "\ncommit_url=%s", p.CommitURL)
	}
	if p.GitHubSkipReason != "" {
		fmt.Fprintf(&b, "\ngithub_publish=skipped reason=%s", p.GitHubSkipReason)
	}
	if p.HandoffStatus != "" {
		fmt.Fprintf(&b, "\nhandoff_status=%s", p.HandoffStatus)
	}
	if p.SummaryJSONPath != nil && *p.SummaryJSONPath != "" {
		fmt.Fprintf(&b, "\nsummary=%s", *p.SummaryJSONPath)
	}
	return b.String()
}
