package telegrambot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

// fakeTelegram is an httptest stand-in for api.telegram.org. It records
// sendMessage / editMessageText calls, serves queued getUpdates batches, and
// acks callbacks.
type fakeTelegram struct {
	server *httptest.Server

	mu       sync.Mutex
	sent     []sendMessageRequest
	edits    []editMessageTextRequest
	updates  [][]Update
	commands []botCommand
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var req sendMessageRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.mu.Lock()
			f.sent = append(f.sent, req)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			var req editMessageTextRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.mu.Lock()
			f.edits = append(f.edits, req)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			batch := f.nextBatch()
			if len(batch) == 0 {
				// Emulate a long-poll that returns empty after a brief hold so
				// the loop doesn't hot-spin during the test.
				time.Sleep(15 * time.Millisecond)
			}
			_ = json.NewEncoder(w).Encode(getUpdatesResponse{OK: true, Result: batch})
		case strings.HasSuffix(r.URL.Path, "/deleteWebhook"):
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasSuffix(r.URL.Path, "/setMyCommands"):
			var body struct {
				Commands []botCommand `json:"commands"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.commands = body.Commands
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeTelegram) enqueue(batch []Update) {
	f.mu.Lock()
	f.updates = append(f.updates, batch)
	f.mu.Unlock()
}

func (f *fakeTelegram) nextBatch() []Update {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.updates) == 0 {
		return nil
	}
	b := f.updates[0]
	f.updates = f.updates[1:]
	return b
}

func (f *fakeTelegram) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeTelegram) editCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.edits)
}

func (f *fakeTelegram) lastSent(t *testing.T) sendMessageRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		t.Fatal("no messages sent")
	}
	return f.sent[len(f.sent)-1]
}

func (f *fakeTelegram) lastEdit(t *testing.T) editMessageTextRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.edits) == 0 {
		t.Fatal("no messages edited")
	}
	return f.edits[len(f.edits)-1]
}

func (f *fakeTelegram) registeredCommandNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.commands))
	for _, c := range f.commands {
		names = append(names, c.Command)
	}
	return names
}

func markupHasData(m *inlineKeyboardMarkup, data string) bool {
	if m == nil {
		return false
	}
	for _, row := range m.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData == data {
				return true
			}
		}
	}
	return false
}

func markupHasURL(m *inlineKeyboardMarkup) bool {
	if m == nil {
		return false
	}
	for _, row := range m.InlineKeyboard {
		for _, b := range row {
			if b.URL != "" {
				return true
			}
		}
	}
	return false
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newTestBot(t *testing.T, st *store.Store, fake *fakeTelegram) *Bot {
	t.Helper()
	return New(Config{
		BotToken:       "token",
		APIBase:        fake.server.URL,
		AllowedChatIDs: []string{"100"},
		Store:          st,
		Client:         fake.server.Client(),
		Logger:         testLogger(),
		PollTimeout:    time.Second,
	})
}

func msgUpdate(updateID, chatID, userID int64, username, text string) Update {
	return Update{
		UpdateID: updateID,
		Message: &Message{
			MessageID: updateID,
			From:      &User{ID: userID, Username: username},
			Chat:      Chat{ID: chatID, Type: "group"},
			Text:      text,
		},
	}
}

func callbackUpdate(updateID, chatID, userID, messageID int64, username, data string) Update {
	return Update{
		UpdateID: updateID,
		CallbackQuery: &CallbackQuery{
			ID:      fmt.Sprintf("cb%d", updateID),
			From:    &User{ID: userID, Username: username},
			Message: &Message{MessageID: messageID, Chat: Chat{ID: chatID, Type: "group"}},
			Data:    data,
		},
	}
}

// TestButtonSignalFlowCreatesCase: /signal <tx> -> chain button -> confirm
// button -> submit, all via the inline-button path (no plain-text follow-up).
func TestButtonSignalFlowCreatesCase(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	tx := "0x" + strings.Repeat("a", 64)
	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/signal "+tx))
	if ks := fake.lastSent(t); !markupHasData(ks.ReplyMarkup, "c:eth") {
		t.Fatalf("expected chain keyboard, got %+v", ks.ReplyMarkup)
	}
	bot.handleUpdate(ctx, callbackUpdate(2, 100, 7, 500, "alice", "c:eth"))
	if ce := fake.lastEdit(t); !strings.Contains(ce.Text, "등록 확인") || !markupHasData(ce.ReplyMarkup, "ok") {
		t.Fatalf("expected confirm card, got %q markup=%+v", ce.Text, ce.ReplyMarkup)
	}
	bot.handleUpdate(ctx, callbackUpdate(3, 100, 7, 500, "alice", "ok"))
	if re := fake.lastEdit(t); !strings.Contains(re.Text, "등록됨") {
		t.Fatalf("expected success result, got %q", re.Text)
	}

	items, total, err := st.ListCases(ctx, store.CaseListFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("want exactly 1 case, got total=%d", total)
	}
	c := items[0]
	if c.Chain != "eth" || c.TxHash != tx {
		t.Fatalf("case fields wrong: chain=%q tx=%q", c.Chain, c.TxHash)
	}
	if c.Source == nil || !strings.Contains(*c.Source, "alice") {
		t.Fatalf("source not wired from telegram user: %v", c.Source)
	}
}

// TestOneShotSignalWithProtocol: /signal <chain> <tx> <protocol> -> confirm
// card directly -> submit, carrying the protocol.
func TestOneShotSignalWithProtocol(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	tx := "0x" + strings.Repeat("b", 64)
	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/signal eth "+tx+" Curve"))
	cs := fake.lastSent(t)
	if !strings.Contains(cs.Text, "등록 확인") || !strings.Contains(cs.Text, "Curve") || !markupHasData(cs.ReplyMarkup, "ok") {
		t.Fatalf("expected confirm card with protocol, got %q markup=%+v", cs.Text, cs.ReplyMarkup)
	}
	bot.handleUpdate(ctx, callbackUpdate(2, 100, 7, 500, "alice", "ok"))

	items, _, _ := st.ListCases(ctx, store.CaseListFilter{Limit: 10})
	if len(items) != 1 {
		t.Fatalf("want 1 case, got %d", len(items))
	}
	if got := protocolDisplay(items[0].Metadata); got != "Curve" {
		t.Fatalf("protocol = %q, want Curve", got)
	}
}

func TestInvalidTxInCommand(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/signal not-a-hash"))
	if last := fake.lastSent(t); !strings.Contains(last.Text, "형식") {
		t.Fatalf("expected tx format error, got %q", last.Text)
	}
	items, _, _ := st.ListCases(ctx, store.CaseListFilter{Limit: 10})
	if len(items) != 0 {
		t.Fatalf("invalid tx should not create a case, got %d", len(items))
	}
}

func TestRepeatTxReportsExisting(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	tx := "0x" + strings.Repeat("d", 64)
	run := func(startID int64) {
		bot.handleUpdate(ctx, msgUpdate(startID, 100, 7, "alice", "/signal eth "+tx))
		bot.handleUpdate(ctx, callbackUpdate(startID+1, 100, 7, 500, "alice", "ok"))
	}
	run(1)
	if le := fake.lastEdit(t); !strings.Contains(le.Text, "등록됨") {
		t.Fatalf("first submit should be new: %q", le.Text)
	}
	run(10)
	if le := fake.lastEdit(t); !strings.Contains(le.Text, "이미 등록") {
		t.Fatalf("second submit should be existing: %q", le.Text)
	}
	items, total, _ := st.ListCases(ctx, store.CaseListFilter{Limit: 10})
	if total != 1 || len(items) != 1 {
		t.Fatalf("dedup should keep a single case, got total=%d", total)
	}
}

func TestCancelButtonAbortsFlow(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	tx := "0x" + strings.Repeat("c", 64)
	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/signal "+tx))
	bot.handleUpdate(ctx, callbackUpdate(2, 100, 7, 500, "alice", "no"))
	if le := fake.lastEdit(t); !strings.Contains(le.Text, "취소") {
		t.Fatalf("expected cancel edit, got %q", le.Text)
	}
	items, _, _ := st.ListCases(ctx, store.CaseListFilter{Limit: 10})
	if len(items) != 0 {
		t.Fatalf("cancelled flow should not create a case, got %d", len(items))
	}
}

func TestCancelCommandThenConfirmExpired(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	tx := "0x" + strings.Repeat("c", 64)
	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/signal "+tx))
	bot.handleUpdate(ctx, msgUpdate(2, 100, 7, "alice", "/cancel"))
	if last := fake.lastSent(t); !strings.Contains(last.Text, "취소") {
		t.Fatalf("expected cancel confirmation, got %q", last.Text)
	}
	// A confirm tap after cancel finds no session -> expired, no case.
	bot.handleUpdate(ctx, callbackUpdate(3, 100, 7, 500, "alice", "ok"))
	if le := fake.lastEdit(t); !strings.Contains(le.Text, "만료") {
		t.Fatalf("expected session-expired edit, got %q", le.Text)
	}
	items, _, _ := st.ListCases(ctx, store.CaseListFilter{Limit: 10})
	if len(items) != 0 {
		t.Fatalf("cancelled flow should not create a case, got %d", len(items))
	}
}

func TestUnauthorizedChatIgnored(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	tx := "0x" + strings.Repeat("a", 64)
	bot.handleUpdate(ctx, msgUpdate(1, 999, 7, "mallory", "/signal "+tx))
	bot.handleUpdate(ctx, callbackUpdate(2, 999, 7, 500, "mallory", "ok"))

	if n := fake.count(); n != 0 {
		t.Fatalf("expected no messages to unauthorized chat, got %d", n)
	}
	if n := fake.editCount(); n != 0 {
		t.Fatalf("expected no edits for unauthorized chat, got %d", n)
	}
	items, _, err := st.ListCases(ctx, store.CaseListFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no cases from unauthorized chat, got %d", len(items))
	}
}

func TestRecentShowsStatusAndDetailButton(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	src := "telegram:@wi11y"
	if _, _, err := st.SubmitCase(ctx, "eth", "0x"+strings.Repeat("f", 64), &src, nil, json.RawMessage(`{"protocol_name":"Curve"}`), false); err != nil {
		t.Fatal(err)
	}

	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/recent"))
	last := fake.lastSent(t)
	for _, want := range []string{"Curve", "queued"} {
		if !strings.Contains(last.Text, want) {
			t.Fatalf("recent reply missing %q:\n%s", want, last.Text)
		}
	}
	if !markupHasData(last.ReplyMarkup, "st:0") {
		t.Fatalf("recent missing detail button: %+v", last.ReplyMarkup)
	}

	// Tapping the detail button sends the status card.
	bot.handleUpdate(ctx, callbackUpdate(2, 100, 7, 600, "alice", "st:0"))
	if le := fake.lastSent(t); !strings.Contains(le.Text, "케이스 ") || !strings.Contains(le.Text, "상태:") {
		t.Fatalf("detail button did not produce a status card: %q", le.Text)
	}
}

func TestRecentListsProtocolAndDate(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	det := "2026-06-20T05:14:00Z"
	src := "telegram:test"
	if _, _, err := st.SubmitCase(ctx, "eth", "0x"+strings.Repeat("c", 64), &src, &det, json.RawMessage(`{"protocol_name":"Curve"}`), false); err != nil {
		t.Fatal(err)
	}

	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/recent"))
	last := fake.lastSent(t)
	for _, want := range []string{"최근 인시던트", "Curve", "2026-06-20 05:14 UTC"} {
		if !strings.Contains(last.Text, want) {
			t.Fatalf("recent reply missing %q:\n%s", want, last.Text)
		}
	}
}

func TestRecentEmpty(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/recent"))
	if last := fake.lastSent(t); !strings.Contains(last.Text, "최근 인시던트가 없습니다") {
		t.Fatalf("expected empty-recent message, got %q", last.Text)
	}
}

func TestStatusCommandReportsLifecycle(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	tx := "0x" + strings.Repeat("e", 64)
	src := "telegram:@wi11y"
	c, _, err := st.SubmitCase(ctx, "eth", tx, &src, nil, json.RawMessage(`{"protocol_name":"Curve"}`), false)
	if err != nil {
		t.Fatal(err)
	}

	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/status "+c.CaseID))
	last := fake.lastSent(t)
	for _, want := range []string{c.CaseID, "체인: eth", "상태: queued", "Curve"} {
		if !strings.Contains(last.Text, want) {
			t.Fatalf("status(queued) missing %q:\n%s", want, last.Text)
		}
	}

	claimed, err := st.ClaimNextQueued(ctx, t.TempDir())
	if err != nil || claimed == nil {
		t.Fatalf("claim queued case: case=%v err=%v", claimed, err)
	}
	if err := st.MarkDone(ctx, claimed.CaseID, "verified", false); err != nil {
		t.Fatal(err)
	}
	bot.handleUpdate(ctx, msgUpdate(2, 100, 7, "alice", "/status "+c.CaseID))
	if last := fake.lastSent(t); !strings.Contains(last.Text, "결과: verified") {
		t.Fatalf("status(finished) missing outcome verified:\n%s", last.Text)
	}
}

func TestStatusReportButton(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	src := "telegram:@wi11y"
	c, _, err := st.SubmitCase(ctx, "eth", "0x"+strings.Repeat("a", 64), &src, nil, json.RawMessage(`{"protocol_name":"Curve"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendCaseEvent(ctx, c.CaseID, "github_publish", map[string]any{
		"report_url": "https://github.com/BackwardLabs/Q1-2026/blob/main/x/README.md",
	}); err != nil {
		t.Fatal(err)
	}

	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/status "+c.CaseID))
	if last := fake.lastSent(t); !markupHasURL(last.ReplyMarkup) {
		t.Fatalf("expected a report URL button, got markup=%+v", last.ReplyMarkup)
	}
}

func TestStatusMissingArgAndUnknownCase(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/status"))
	if last := fake.lastSent(t); !strings.Contains(last.Text, "사용법") {
		t.Fatalf("expected usage hint for bare /status, got %q", last.Text)
	}
	bot.handleUpdate(ctx, msgUpdate(2, 100, 7, "alice", "/status case_does_not_exist"))
	if last := fake.lastSent(t); !strings.Contains(last.Text, "찾을 수 없") {
		t.Fatalf("expected not-found for unknown case, got %q", last.Text)
	}
}

func TestCommandAtBotnameSuffixParsed(t *testing.T) {
	if cmd, ok := parseCommand("/signal@BacklightBot extra"); !ok || cmd != "signal" {
		t.Fatalf("parseCommand groupsuffix = (%q,%v)", cmd, ok)
	}
	if cmd, ok := parseCommand("not a command"); ok {
		t.Fatalf("parseCommand non-command returned %q", cmd)
	}
}

func TestClientRedactsBotTokenFromTransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	token := "123456:SUPER-SECRET-TOKEN"
	c := &apiClient{botToken: token, apiBase: base, client: &http.Client{Timeout: time.Second}}
	err := c.sendMessage(context.Background(), 1, "hi")
	if err == nil {
		t.Fatal("expected a transport error from a closed server")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked the bot token: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("expected the token to be redacted to ***, got: %v", err)
	}
}

func TestLoopProcessesUpdatesFromGetUpdates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := openStore(t)
	fake := newFakeTelegram(t)
	fake.enqueue([]Update{msgUpdate(10, 100, 7, "alice", "/help")})
	bot := newTestBot(t, st, fake)

	bot.Start(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for fake.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	bot.Wait()

	if fake.count() == 0 {
		t.Fatal("loop did not process the queued update")
	}
	if last := fake.lastSent(t); !strings.Contains(last.Text, "/signal") {
		t.Fatalf("help reply not delivered via loop: %q", last.Text)
	}
}

func TestSetMyCommandsRegisteredAtStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	bot.Start(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for len(fake.registeredCommandNames()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	bot.Wait()

	names := fake.registeredCommandNames()
	for _, want := range []string{"signal", "recent", "status", "cancel", "help"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("command %q not registered in menu, got %v", want, names)
		}
	}
}

// TestEndToEndButtonFlowThroughLoop drives the button registration flow through
// the REAL long-poll loop (getUpdates) and a REAL SQLite store — the same code
// path the deployed binary runs — and prints the transcript.
func TestEndToEndButtonFlowThroughLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	tx := "0x" + strings.Repeat("a", 64)
	fake.enqueue([]Update{msgUpdate(1, 100, 7, "wi11y", "/signal "+tx)})
	fake.enqueue([]Update{callbackUpdate(2, 100, 7, 500, "wi11y", "c:eth")})
	fake.enqueue([]Update{callbackUpdate(3, 100, 7, 500, "wi11y", "ok")})
	fake.enqueue([]Update{msgUpdate(4, 100, 7, "wi11y", "/recent")})

	bot.Start(ctx)
	deadline := time.Now().Add(8 * time.Second)
	created := false
	for time.Now().Before(deadline) {
		items, _, _ := st.ListCases(context.Background(), store.CaseListFilter{Limit: 10})
		if len(items) == 1 && fake.editCount() >= 2 && fake.count() >= 2 {
			created = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	bot.Wait()

	fake.mu.Lock()
	sent := append([]sendMessageRequest(nil), fake.sent...)
	edits := append([]editMessageTextRequest(nil), fake.edits...)
	fake.mu.Unlock()

	t.Log("=== live button-flow transcript (real getUpdates loop + real SQLite) ===")
	t.Log("USER ▶ /signal <tx>")
	for _, s := range sent {
		t.Logf("  BOT send ◀ %s", strings.ReplaceAll(s.Text, "\n", " / "))
	}
	t.Log("USER ▶ [tap ETH] [tap 등록]")
	for _, e := range edits {
		t.Logf("  BOT edit ◀ %s", strings.ReplaceAll(e.Text, "\n", " / "))
	}

	if !created {
		t.Fatalf("button flow did not complete: sent=%d edits=%d", len(sent), len(edits))
	}
	if le := edits[len(edits)-1]; !strings.Contains(le.Text, "등록됨") {
		t.Fatalf("final edit not a success result: %q", le.Text)
	}
	items, _, _ := st.ListCases(context.Background(), store.CaseListFilter{Limit: 10})
	c := items[0]
	if c.Chain != "eth" || c.TxHash != tx {
		t.Fatalf("case fields wrong: chain=%q tx=%q", c.Chain, c.TxHash)
	}
	t.Logf("=== store result: case_id=%s state=%s chain=%s source=%s ===", c.CaseID, c.State, c.Chain, *c.Source)
}
