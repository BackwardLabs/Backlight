package telegrambot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const sessionExpiredMsg = "세션이 만료되었습니다. /signal <tx>로 다시 시작하세요."

// handleCallback routes an inline-button tap. Button taps are delivered to the
// bot regardless of group privacy mode, so the registration flow leans on them
// for chain selection and confirmation (only the tx hash must be typed, and it
// rides in the /signal command).
func (b *Bot) handleCallback(ctx context.Context, cq *CallbackQuery) {
	if cq == nil || cq.Message == nil {
		return
	}
	chatID := cq.Message.Chat.ID
	if !b.allowedChats[chatID] {
		_ = b.client.answerCallbackQuery(ctx, cq.ID, "")
		b.log.Info("telegram callback from unauthorized chat ignored", "chat_id", chatID)
		return
	}
	// Acknowledge immediately so the client stops showing the loading spinner.
	_ = b.client.answerCallbackQuery(ctx, cq.ID, "")

	key := callbackSessionKey(cq)
	msgID := cq.Message.MessageID
	data := cq.Data
	switch {
	case strings.HasPrefix(data, "c:"):
		b.onChainChosen(ctx, key, chatID, msgID, strings.TrimPrefix(data, "c:"))
	case data == "ok":
		b.onConfirm(ctx, key, chatID, msgID, cq.From)
	case data == "no":
		b.clearSession(key)
		b.editText(ctx, chatID, msgID, "등록을 취소했습니다.", nil)
	case strings.HasPrefix(data, "st:"):
		b.onRecentDetail(ctx, chatID, strings.TrimPrefix(data, "st:"))
	default:
		b.log.Info("telegram unknown callback data", "data", data)
	}
}

func (b *Bot) onChainChosen(ctx context.Context, key string, chatID, msgID int64, chain string) {
	sess := b.activeSession(key)
	if sess == nil {
		b.editText(ctx, chatID, msgID, sessionExpiredMsg, nil)
		return
	}
	if chain == "other" {
		b.clearSession(key)
		b.editText(ctx, chatID, msgID, "다른 체인은 한 줄로 입력하세요:\n/signal <chain> <tx>", nil)
		return
	}
	sess.chain = chain
	b.setSession(key, sess)
	b.editText(ctx, chatID, msgID, confirmText(sess), confirmKeyboard())
}

func (b *Bot) onConfirm(ctx context.Context, key string, chatID, msgID int64, user *User) {
	sess := b.activeSession(key)
	if sess == nil {
		b.editText(ctx, chatID, msgID, sessionExpiredMsg, nil)
		return
	}
	b.clearSession(key)
	b.editText(ctx, chatID, msgID, b.submitPending(ctx, sess, user), nil)
}

func (b *Bot) onRecentDetail(ctx context.Context, chatID int64, idxStr string) {
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		return
	}
	caseID := b.recentCaseID(chatID, idx)
	if caseID == "" {
		b.reply(ctx, chatID, "목록이 만료되었습니다. /recent를 다시 실행하세요.")
		return
	}
	c, err := b.store.GetCase(ctx, caseID)
	if err != nil || c == nil {
		b.reply(ctx, chatID, "케이스를 찾을 수 없습니다.")
		return
	}
	b.sendStatus(ctx, chatID, c)
}

// submitPending runs the actual case submission (manual-operator POST /cases
// semantics) and returns the operator-facing result line.
func (b *Bot) submitPending(ctx context.Context, s *session, user *User) string {
	source := userSource(user)
	detectedAt := b.now().UTC().Format(time.RFC3339Nano)
	metadata := b.enrich(ctx, s.chain, s.txHash, buildMetadata(s.protocol))
	c, dedup, err := b.store.SubmitCase(ctx, s.chain, s.txHash, &source, &detectedAt, metadata, false)
	if err != nil {
		b.log.Error("telegram signal SubmitCase failed", "err", err, "chain", s.chain)
		return "등록 실패: 내부 오류가 발생했습니다. 잠시 후 다시 시도하세요."
	}
	return submissionReply(c, dedup, s.chain)
}

func (b *Bot) editText(ctx context.Context, chatID, msgID int64, text string, markup *inlineKeyboardMarkup) {
	if err := b.client.editMessageText(ctx, chatID, msgID, text, markup); err != nil {
		b.log.Warn("telegram editMessageText failed", "err", err, "chat_id", chatID)
	}
}

func callbackSessionKey(cq *CallbackQuery) string {
	var userID int64
	if cq.From != nil {
		userID = cq.From.ID
	}
	return fmt.Sprintf("%d:%d", cq.Message.Chat.ID, userID)
}

func confirmText(s *session) string {
	var sb strings.Builder
	sb.WriteString("등록 확인")
	sb.WriteString("\n체인: " + s.chain)
	sb.WriteString("\nTx: " + shortTx(s.txHash))
	if s.protocol != "" {
		sb.WriteString("\n프로토콜: " + s.protocol)
	}
	return sb.String()
}

func chainKeyboard() *inlineKeyboardMarkup {
	return &inlineKeyboardMarkup{InlineKeyboard: [][]inlineKeyboardButton{
		{{Text: "ETH", CallbackData: "c:eth"}, {Text: "BSC", CallbackData: "c:bsc"}},
		{{Text: "Base", CallbackData: "c:base"}, {Text: "Arbitrum", CallbackData: "c:arb"}},
		{{Text: "기타(한 줄 입력)", CallbackData: "c:other"}},
	}}
}

func confirmKeyboard() *inlineKeyboardMarkup {
	return &inlineKeyboardMarkup{InlineKeyboard: [][]inlineKeyboardButton{
		{{Text: "✅ 등록", CallbackData: "ok"}, {Text: "✖ 취소", CallbackData: "no"}},
	}}
}
