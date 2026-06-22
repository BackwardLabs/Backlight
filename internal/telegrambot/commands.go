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

type convState int

const (
	stateAwaitingChain convState = iota
	stateAwaitingTx
	stateAwaitingProtocol
)

// session is the in-memory state of one operator's in-progress /signal flow.
// It is intentionally not persisted: a process restart simply asks the operator
// to re-run /signal, which is acceptable for a manual submission path.
type session struct {
	state     convState
	chain     string
	txHash    string
	updatedAt time.Time
}

func (b *Bot) handleUpdate(ctx context.Context, u Update) {
	msg := u.Message
	if msg == nil || strings.TrimSpace(msg.Text) == "" {
		return
	}
	chatID := msg.Chat.ID
	// Chat-level authorization: only the configured operator chat(s) may drive
	// the bot. Everything else is ignored silently (no reply) so the bot's
	// existence isn't leaked to unauthorized chats.
	if !b.allowedChats[chatID] {
		b.log.Info("telegram command from unauthorized chat ignored", "chat_id", chatID)
		return
	}
	text := strings.TrimSpace(msg.Text)
	key := sessionKey(msg)

	if cmd, ok := parseCommand(text); ok {
		// Any command interrupts an in-progress guided flow.
		switch cmd {
		case "signal":
			b.startSignal(ctx, key, chatID)
		case "recent":
			b.clearSession(key)
			b.handleRecent(ctx, chatID)
		case "status":
			b.clearSession(key)
			b.handleStatus(ctx, chatID, commandArg(text))
		case "cancel":
			if b.clearSession(key) {
				b.reply(ctx, chatID, "진행 중인 등록을 취소했습니다.")
			} else {
				b.reply(ctx, chatID, "취소할 진행 중인 등록이 없습니다.")
			}
		case "help", "start":
			b.clearSession(key)
			b.reply(ctx, chatID, helpText())
		default:
			b.reply(ctx, chatID, "알 수 없는 명령입니다.\n\n"+helpText())
		}
		return
	}

	// Plain text is only meaningful as an answer to an active guided flow.
	// Otherwise stay quiet so the bot isn't noisy in a shared operator chat.
	sess := b.activeSession(key)
	if sess == nil {
		return
	}
	b.advanceSignal(ctx, key, chatID, msg, sess, text)
}

func (b *Bot) startSignal(ctx context.Context, key string, chatID int64) {
	b.setSession(key, &session{state: stateAwaitingChain})
	b.reply(ctx, chatID, "새 인시던트를 등록합니다.\n체인을 입력하세요 (예: eth, bsc).\n취소하려면 /cancel")
}

func (b *Bot) advanceSignal(ctx context.Context, key string, chatID int64, msg *Message, sess *session, text string) {
	switch sess.state {
	case stateAwaitingChain:
		sess.chain = text
		sess.state = stateAwaitingTx
		b.setSession(key, sess)
		b.reply(ctx, chatID, "tx 해시를 입력하세요 (0x… 64 hex).")
	case stateAwaitingTx:
		if !txHashRegex.MatchString(text) {
			b.reply(ctx, chatID, "tx 해시 형식이 올바르지 않습니다. 0x + 64 hex 문자여야 합니다. 다시 입력하세요.")
			return
		}
		sess.txHash = text
		sess.state = stateAwaitingProtocol
		b.setSession(key, sess)
		b.reply(ctx, chatID, "프로토콜명을 입력하세요. 없으면 - 만 입력하세요.")
	case stateAwaitingProtocol:
		protocol := ""
		if text != "-" {
			protocol = text
		}
		b.clearSession(key)
		b.submitSignal(ctx, chatID, msg, sess.chain, sess.txHash, protocol)
	}
}

func (b *Bot) submitSignal(ctx context.Context, chatID int64, msg *Message, chain, txHash, protocol string) {
	source := submissionSource(msg)
	detectedAt := b.now().UTC().Format(time.RFC3339Nano)
	metadata := buildMetadata(protocol)
	metadata = b.enrich(ctx, chain, txHash, metadata)

	c, dedup, err := b.store.SubmitCase(ctx, chain, txHash, &source, &detectedAt, metadata, false)
	if err != nil {
		b.log.Error("telegram signal SubmitCase failed", "err", err, "chain", chain)
		b.reply(ctx, chatID, "등록 실패: 내부 오류가 발생했습니다. 잠시 후 다시 시도하세요.")
		return
	}
	b.reply(ctx, chatID, submissionReply(c, dedup, chain))
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
	fmt.Fprintf(&sb, "최근 인시던트 %d건", len(items))
	for i := range items {
		c := &items[i]
		fmt.Fprintf(&sb, "\n%d. %s — %s — %s — %s", i+1, recentLabel(c), occurredAt(c), shortStatus(c), c.CaseID)
	}
	b.reply(ctx, chatID, sb.String())
}

// handleStatus reports the lifecycle of a single case so an operator can see
// whether the engine run is queued, running, or finished (and how it finished)
// without leaving Telegram.
func (b *Bot) handleStatus(ctx context.Context, chatID int64, caseID string) {
	caseID = strings.TrimSpace(caseID)
	if caseID == "" {
		b.reply(ctx, chatID, "사용법: /status <case_id>\n(case_id는 등록 응답이나 /recent 끝에 표시됩니다)")
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
	b.reply(ctx, chatID, statusDetail(c))
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

func submissionSource(msg *Message) string {
	if msg.From != nil {
		if msg.From.Username != "" {
			return "telegram:@" + msg.From.Username
		}
		if msg.From.FirstName != "" {
			return fmt.Sprintf("telegram:%s(%d)", msg.From.FirstName, msg.From.ID)
		}
		return fmt.Sprintf("telegram:%d", msg.From.ID)
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
		{Command: "signal", Description: "새 인시던트 등록 (체인 → tx → 프로토콜)"},
		{Command: "recent", Description: "최근 인시던트 10건 (상태 포함)"},
		{Command: "status", Description: "케이스 상태 조회: /status <case_id>"},
		{Command: "cancel", Description: "진행 중인 등록 취소"},
		{Command: "help", Description: "사용 가능한 명령 도움말"},
	}
}

func helpText() string {
	return strings.Join([]string{
		"Backlight 명령:",
		"/signal — 새 인시던트 등록 (체인 → tx 해시 → 프로토콜 순서로 입력)",
		"/recent — 최근 인시던트 10건 (프로토콜 · 발생일시 · 상태 · case_id)",
		"/status <case_id> — 케이스 상태/결과 조회",
		"/cancel — 진행 중인 등록 취소",
		"/help — 도움말",
	}, "\n")
}
