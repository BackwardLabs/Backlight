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
	fmt.Fprintf(&b, "[helios] event=%s\n", p.Event)
	fmt.Fprintf(&b, "case_id=%s\n", p.CaseID)
	fmt.Fprintf(&b, "chain=%s tx=%s\n", p.Chain, tx)
	fmt.Fprintf(&b, "state=%s outcome=%s", p.State, p.Outcome)
	if p.FailureKind != nil && *p.FailureKind != "" {
		fmt.Fprintf(&b, " failure_kind=%s", *p.FailureKind)
	}
	if p.HandoffStatus != "" {
		fmt.Fprintf(&b, "\nhandoff_status=%s", p.HandoffStatus)
	}
	if p.SummaryJSONPath != nil && *p.SummaryJSONPath != "" {
		fmt.Fprintf(&b, "\nsummary=%s", *p.SummaryJSONPath)
	}
	return b.String()
}
