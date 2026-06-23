package telegrambot

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

// txHashRegex mirrors internal/api/validation.go: optional 0x prefix + 64 hex
// chars (Ethereum-style tx hash).
var txHashRegex = regexp.MustCompile(`^(0x)?[0-9a-fA-F]{64}$`)

const recentLimit = 10

// session is the in-memory state of one operator's in-progress /signal
// registration. The flow is button-driven: txHash is captured from the command,
// chain from an inline button, and the operator confirms with a button. It is
// intentionally not persisted — a restart just asks the operator to re-run
// /signal, acceptable for a manual submission path.
type session struct {
	txHash    string
	chain     string
	protocol  string
	updatedAt time.Time
}

func (b *Bot) handleUpdate(ctx context.Context, u Update) {
	if u.CallbackQuery != nil {
		b.handleCallback(ctx, u.CallbackQuery)
		return
	}
	msg := u.Message
	if msg == nil || strings.TrimSpace(msg.Text) == "" {
		return
	}
	chatID := msg.Chat.ID
	// Chat-level authorization: only the configured operator chat(s) may drive
	// the bot. Everything else is ignored silently (no reply) so the bot's
	// existence isn't leaked to unauthorized chats.
	if !b.allowedChats[chatID] {
		b.log.Info("telegram message from unauthorized chat ignored", "chat_id", chatID)
		return
	}
	text := strings.TrimSpace(msg.Text)

	cmd, ok := parseCommand(text)
	if !ok {
		// No interactive plain-text step: registration captures the tx from the
		// command itself and uses buttons for the rest, so a group bot needs no
		// privacy-mode workaround. Stray text is ignored.
		return
	}
	switch cmd {
	case "signal":
		b.startSignal(ctx, msg, chatID, text)
	case "recent":
		b.handleRecent(ctx, chatID)
	case "status":
		b.handleStatus(ctx, chatID, commandArg(text))
	case "cancel":
		if b.clearSession(sessionKey(msg)) {
			b.reply(ctx, chatID, "진행 중인 등록을 취소했습니다.")
		} else {
			b.reply(ctx, chatID, "취소할 진행 중인 등록이 없습니다.")
		}
	case "help", "start":
		b.reply(ctx, chatID, helpText())
	default:
		b.reply(ctx, chatID, "알 수 없는 명령입니다.\n\n"+helpText())
	}
}

// startSignal begins a registration. tx is captured from the command (always
// delivered, even under group privacy mode); chain and confirmation are buttons.
//
//	/signal <tx>                     -> chain buttons
//	/signal <chain> <tx> [protocol]  -> straight to the confirm card
//	/signal                          -> usage hint
func (b *Bot) startSignal(ctx context.Context, msg *Message, chatID int64, text string) {
	key := sessionKey(msg)
	args := strings.Fields(commandArg(text))
	switch len(args) {
	case 0:
		b.clearSession(key)
		b.reply(ctx, chatID, "tx 해시와 함께 시작하세요:\n/signal <tx_hash>\n(체인 선택·등록은 버튼으로 진행됩니다)\n\n한 줄로: /signal <chain> <tx> [protocol]")
	case 1:
		tx := args[0]
		if !txHashRegex.MatchString(tx) {
			b.reply(ctx, chatID, "tx 해시 형식이 올바르지 않습니다 (0x + 64 hex).")
			return
		}
		sess := &session{txHash: tx}
		b.setSession(key, sess)
		b.replyMarkup(ctx, chatID, "체인을 선택하세요\nTx: "+shortTx(tx), chainKeyboard())
	default:
		chain := args[0]
		tx := args[1]
		if !txHashRegex.MatchString(tx) {
			b.reply(ctx, chatID, "형식: /signal <chain> <tx> [protocol]\ntx 해시가 올바르지 않습니다 (0x + 64 hex).")
			return
		}
		sess := &session{txHash: tx, chain: chain, protocol: strings.TrimSpace(strings.Join(args[2:], " "))}
		b.setSession(key, sess)
		b.replyMarkup(ctx, chatID, confirmText(sess), confirmKeyboard())
	}
}

func (b *Bot) handleRecent(ctx context.Context, chatID int64) {
	items, _, err := b.store.ListCases(ctx, store.CaseListFilter{Limit: recentLimit})
	if err != nil {
		b.log.Error("telegram /recent ListCases failed", "err", err)
		b.reply(ctx, chatID, "조회 실패: 내부 오류가 발생했습니다.")
		return
	}
	if len(items) == 0 {
		b.reply(ctx, chatID, "최근 인시던트가 없습니다.")
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "최근 인시던트 %d건  (번호를 눌러 상세)", len(items))
	ids := make([]string, 0, len(items))
	var rows [][]inlineKeyboardButton
	var row []inlineKeyboardButton
	for i := range items {
		c := &items[i]
		fmt.Fprintf(&sb, "\n%d. %s — %s — %s", i+1, recentLabel(c), occurredAt(c), shortStatus(c))
		ids = append(ids, c.CaseID)
		row = append(row, inlineKeyboardButton{Text: fmt.Sprintf("%d", i+1), CallbackData: fmt.Sprintf("st:%d", i)})
		if len(row) == 5 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	b.setRecentCache(chatID, ids)
	b.replyMarkup(ctx, chatID, sb.String(), &inlineKeyboardMarkup{InlineKeyboard: rows})
}

// handleStatus reports a single case so an operator can see whether the engine
// run is queued, running, or finished (and how) without leaving Telegram.
func (b *Bot) handleStatus(ctx context.Context, chatID int64, caseID string) {
	caseID = strings.TrimSpace(caseID)
	if caseID == "" {
		b.reply(ctx, chatID, "사용법: /status <case_id>\n(case_id는 등록 응답에 표시되며, /recent는 번호 버튼으로 바로 열 수 있습니다)")
		return
	}
	c, err := b.store.GetCase(ctx, caseID)
	if err != nil {
		b.log.Error("telegram /status GetCase failed", "err", err, "case_id", caseID)
		b.reply(ctx, chatID, "조회 실패: 내부 오류가 발생했습니다.")
		return
	}
	if c == nil {
		b.reply(ctx, chatID, "케이스를 찾을 수 없습니다: "+caseID)
		return
	}
	b.sendStatus(ctx, chatID, c)
}

// sendStatus renders a case status card, attaching a report button when the
// verified GitHub product publish has recorded a report URL.
func (b *Bot) sendStatus(ctx context.Context, chatID int64, c *store.Case) {
	var markup *inlineKeyboardMarkup
	if url := b.reportURL(ctx, c.CaseID); url != "" {
		markup = &inlineKeyboardMarkup{InlineKeyboard: [][]inlineKeyboardButton{
			{{Text: "📄 리포트", URL: url}},
		}}
	}
	b.replyMarkup(ctx, chatID, statusDetail(c), markup)
}

// reportURL returns the published report link for a case, if a github_publish
// audit event recorded one.
func (b *Bot) reportURL(ctx context.Context, caseID string) string {
	events, err := b.store.CaseEvents(ctx, caseID)
	if err != nil {
		return ""
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].EventType != "github_publish" || len(events[i].Payload) == 0 {
			continue
		}
		var m map[string]any
		if json.Unmarshal(events[i].Payload, &m) != nil {
			continue
		}
		if u, ok := m["report_url"].(string); ok && strings.TrimSpace(u) != "" {
			return strings.TrimSpace(u)
		}
	}
	return ""
}

// enrich mirrors api.Server.enrichIncidentMetadata: a best-effort identity
// pass that is a no-op when the resolver is absent, disabled, or already has
// identity from the operator-supplied protocol.
func (b *Bot) enrich(ctx context.Context, chain, txHash string, metadata json.RawMessage) json.RawMessage {
	if b.resolver == nil {
		return metadata
	}
	enriched, err := b.resolver.EnrichIncidentMetadata(ctx, chain, txHash, metadata)
	if err != nil || len(enriched) == 0 {
		return metadata
	}
	return enriched
}

// --- session store -------------------------------------------------------

func sessionKey(msg *Message) string {
	var userID int64
	if msg.From != nil {
		userID = msg.From.ID
	}
	return fmt.Sprintf("%d:%d", msg.Chat.ID, userID)
}

func (b *Bot) activeSession(key string) *session {
	b.mu.Lock()
	defer b.mu.Unlock()
	sess, ok := b.sessions[key]
	if !ok {
		return nil
	}
	if b.now().Sub(sess.updatedAt) > sessionTTL {
		delete(b.sessions, key)
		return nil
	}
	return sess
}

func (b *Bot) setSession(key string, sess *session) {
	sess.updatedAt = b.now()
	b.mu.Lock()
	b.sessions[key] = sess
	b.mu.Unlock()
}

func (b *Bot) clearSession(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.sessions[key]
	delete(b.sessions, key)
	return ok
}

func (b *Bot) setRecentCache(chatID int64, ids []string) {
	b.mu.Lock()
	if b.recent == nil {
		b.recent = map[int64][]string{}
	}
	b.recent[chatID] = ids
	b.mu.Unlock()
}

func (b *Bot) recentCaseID(chatID int64, idx int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := b.recent[chatID]
	if idx < 0 || idx >= len(ids) {
		return ""
	}
	return ids[idx]
}

// --- pure helpers --------------------------------------------------------

func parseCommand(text string) (string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", false
	}
	token := strings.TrimPrefix(fields[0], "/")
	// Strip the @botname suffix Telegram appends in group chats: /signal@MyBot.
	if at := strings.IndexByte(token, '@'); at >= 0 {
		token = token[:at]
	}
	return strings.ToLower(token), true
}

func userSource(u *User) string {
	if u != nil {
		if u.Username != "" {
			return "telegram:@" + u.Username
		}
		if u.FirstName != "" {
			return fmt.Sprintf("telegram:%s(%d)", u.FirstName, u.ID)
		}
		return fmt.Sprintf("telegram:%d", u.ID)
	}
	return "telegram"
}

func buildMetadata(protocol string) json.RawMessage {
	m := map[string]any{"submitted_via": "telegram_command"}
	if p := strings.TrimSpace(protocol); p != "" {
		m["protocol"] = p
		m["protocol_name"] = p
		m["protocol_source"] = "telegram_command"
	}
	data, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage("{}")
	}
	return data
}

func submissionReply(c *store.Case, dedup, chain string) string {
	label := store.IncidentSlug(c)
	switch dedup {
	case store.DedupNew:
		return fmt.Sprintf("✅ 등록됨 — %s\n%s · %s", c.CaseID, label, chain)
	case store.DedupRerunCreated:
		return fmt.Sprintf("✅ 재실행 등록됨 — %s\n%s · %s", c.CaseID, label, chain)
	case store.DedupExisting:
		return fmt.Sprintf("♻️ 이미 등록된 tx입니다 — %s (기존 케이스)", c.CaseID)
	case store.DedupExistingNonRetryable:
		return fmt.Sprintf("♻️ 이미 처리된 tx입니다(재시도 불가) — %s", c.CaseID)
	default:
		return fmt.Sprintf("등록됨 — %s (%s)", c.CaseID, dedup)
	}
}

func recentLabel(c *store.Case) string {
	if p := protocolDisplay(c.Metadata); p != "" {
		return p
	}
	return store.IncidentSlug(c)
}

func protocolDisplay(metadata json.RawMessage) string {
	if len(metadata) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(metadata, &m); err != nil {
		return ""
	}
	for _, key := range []string{"protocol_name", "protocol", "project_name", "project", "display_name"} {
		if v, ok := m[key].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" && !strings.EqualFold(v, "unknown") {
				return v
			}
		}
	}
	return ""
}

func occurredAt(c *store.Case) string {
	if c.DetectedAt != nil && strings.TrimSpace(*c.DetectedAt) != "" {
		return formatTimestamp(*c.DetectedAt)
	}
	return formatTimestamp(c.CreatedAt)
}

func formatTimestamp(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown"
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC().Format("2006-01-02 15:04 UTC")
		}
	}
	return raw
}

// shortStatus is the compact case status for /recent lines: the lifecycle state
// plus the terminal outcome once one exists (e.g. "queued", "running",
// "done·verified", "failed·engine_error").
func shortStatus(c *store.Case) string {
	if c.Outcome != nil && strings.TrimSpace(*c.Outcome) != "" {
		return c.State + "·" + strings.TrimSpace(*c.Outcome)
	}
	return c.State
}

func statusDetail(c *store.Case) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "케이스 %s", c.CaseID)
	if p := protocolDisplay(c.Metadata); p != "" {
		fmt.Fprintf(&sb, "\n프로토콜: %s", p)
	}
	fmt.Fprintf(&sb, "\n체인: %s", c.Chain)
	fmt.Fprintf(&sb, "\nTx: %s", shortTx(c.TxHash))
	fmt.Fprintf(&sb, "\n상태: %s", c.State)
	if c.Outcome != nil && strings.TrimSpace(*c.Outcome) != "" {
		fmt.Fprintf(&sb, "\n결과: %s", strings.TrimSpace(*c.Outcome))
	}
	if c.FailureKind != nil && strings.TrimSpace(*c.FailureKind) != "" {
		fmt.Fprintf(&sb, "\n실패유형: %s", strings.TrimSpace(*c.FailureKind))
	}
	fmt.Fprintf(&sb, "\n시도: %d", c.AttemptNumber)
	fmt.Fprintf(&sb, "\n갱신: %s", formatTimestamp(c.UpdatedAt))
	return sb.String()
}

func shortTx(tx string) string {
	if len(tx) > 14 {
		return tx[:10] + "…" + tx[len(tx)-4:]
	}
	return tx
}

// commandArg returns the text after the first whitespace-delimited token, e.g.
// "/status case_abc" -> "case_abc".
func commandArg(text string) string {
	parts := strings.SplitN(strings.TrimSpace(text), " ", 2)
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// botCommands is the menu registered with Telegram via setMyCommands so clients
// show the command list (and descriptions) when a user types "/". Command names
// must be lowercase, 1-32 chars, without the leading slash.
func botCommands() []botCommand {
	return []botCommand{
		{Command: "signal", Description: "새 인시던트 등록: /signal <tx> (체인·등록은 버튼)"},
		{Command: "recent", Description: "최근 인시던트 10건 (번호 눌러 상세)"},
		{Command: "status", Description: "케이스 상태 조회: /status <case_id>"},
		{Command: "cancel", Description: "진행 중인 등록 취소"},
		{Command: "help", Description: "사용 가능한 명령 도움말"},
	}
}

func helpText() string {
	return strings.Join([]string{
		"Backlight 명령:",
		"/signal <tx> — 새 인시던트 등록 (체인 선택·등록은 버튼)",
		"  한 줄로: /signal <chain> <tx> [protocol]",
		"/recent — 최근 인시던트 10건 (번호 버튼으로 상세)",
		"/status <case_id> — 케이스 상태/결과 조회",
		"/cancel — 진행 중인 등록 취소",
		"/help — 도움말",
	}, "\n")
}
