package telegrambot

import (
	"context"
	"encoding/json"
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

// fakeTelegram is an httptest stand-in for api.telegram.org. It records every
// sendMessage and serves queued getUpdates batches (then empties).
type fakeTelegram struct {
	server *httptest.Server

	mu      sync.Mutex
	sent    []sendMessageRequest
	updates [][]Update
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

func (f *fakeTelegram) lastSent(t *testing.T) sendMessageRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		t.Fatal("no messages sent")
	}
	return f.sent[len(f.sent)-1]
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

func TestGuidedSignalFlowCreatesCase(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	tx := "0x" + strings.Repeat("a", 64)
	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/signal"))
	bot.handleUpdate(ctx, msgUpdate(2, 100, 7, "alice", "eth"))
	bot.handleUpdate(ctx, msgUpdate(3, 100, 7, "alice", tx))
	bot.handleUpdate(ctx, msgUpdate(4, 100, 7, "alice", "Curve"))

	items, total, err := st.ListCases(ctx, store.CaseListFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("want exactly 1 case, got total=%d items=%d", total, len(items))
	}
	c := items[0]
	if c.Chain != "eth" || c.TxHash != tx {
		t.Fatalf("case fields wrong: chain=%q tx=%q", c.Chain, c.TxHash)
	}
	if c.Source == nil || !strings.Contains(*c.Source, "alice") {
		t.Fatalf("source not wired from telegram user: %v", c.Source)
	}
	if got := protocolDisplay(c.Metadata); got != "Curve" {
		t.Fatalf("protocol metadata = %q, want Curve", got)
	}
	last := fake.lastSent(t)
	if !strings.Contains(last.Text, c.CaseID) || !strings.Contains(last.Text, "등록됨") {
		t.Fatalf("confirmation missing case id / success: %q", last.Text)
	}
}

func TestUnauthorizedChatIgnored(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	bot.handleUpdate(ctx, msgUpdate(1, 999, 7, "mallory", "/signal"))
	bot.handleUpdate(ctx, msgUpdate(2, 999, 7, "mallory", "eth"))

	if n := fake.count(); n != 0 {
		t.Fatalf("expected no replies to unauthorized chat, got %d", n)
	}
	items, _, err := st.ListCases(ctx, store.CaseListFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no cases from unauthorized chat, got %d", len(items))
	}
}

func TestInvalidTxRePromptsThenSucceeds(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/signal"))
	bot.handleUpdate(ctx, msgUpdate(2, 100, 7, "alice", "eth"))
	bot.handleUpdate(ctx, msgUpdate(3, 100, 7, "alice", "not-a-hash"))

	last := fake.lastSent(t)
	if !strings.Contains(last.Text, "형식") {
		t.Fatalf("expected tx format error, got %q", last.Text)
	}
	items, _, _ := st.ListCases(ctx, store.CaseListFilter{Limit: 10})
	if len(items) != 0 {
		t.Fatalf("invalid tx should not create a case, got %d", len(items))
	}

	tx := "0x" + strings.Repeat("b", 64)
	bot.handleUpdate(ctx, msgUpdate(4, 100, 7, "alice", tx))
	bot.handleUpdate(ctx, msgUpdate(5, 100, 7, "alice", "-"))

	items, _, _ = st.ListCases(ctx, store.CaseListFilter{Limit: 10})
	if len(items) != 1 {
		t.Fatalf("want 1 case after recovery, got %d", len(items))
	}
	if got := protocolDisplay(items[0].Metadata); got != "" {
		t.Fatalf("skip-protocol should leave no protocol, got %q", got)
	}
}

func TestRepeatTxReportsExisting(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	tx := "0x" + strings.Repeat("d", 64)
	run := func(startID int64) {
		bot.handleUpdate(ctx, msgUpdate(startID, 100, 7, "alice", "/signal"))
		bot.handleUpdate(ctx, msgUpdate(startID+1, 100, 7, "alice", "eth"))
		bot.handleUpdate(ctx, msgUpdate(startID+2, 100, 7, "alice", tx))
		bot.handleUpdate(ctx, msgUpdate(startID+3, 100, 7, "alice", "-"))
	}
	run(1)
	if last := fake.lastSent(t); !strings.Contains(last.Text, "등록됨") {
		t.Fatalf("first submit should be new: %q", last.Text)
	}
	run(10)
	if last := fake.lastSent(t); !strings.Contains(last.Text, "이미 등록") {
		t.Fatalf("second submit should be existing: %q", last.Text)
	}
	items, total, _ := st.ListCases(ctx, store.CaseListFilter{Limit: 10})
	if total != 1 || len(items) != 1 {
		t.Fatalf("dedup should keep a single case, got total=%d", total)
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

func TestCancelClearsSession(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	fake := newFakeTelegram(t)
	bot := newTestBot(t, st, fake)

	bot.handleUpdate(ctx, msgUpdate(1, 100, 7, "alice", "/signal"))
	bot.handleUpdate(ctx, msgUpdate(2, 100, 7, "alice", "/cancel"))
	if last := fake.lastSent(t); !strings.Contains(last.Text, "취소") {
		t.Fatalf("expected cancel confirmation, got %q", last.Text)
	}
	// A bare answer after cancel must not resume the flow.
	bot.handleUpdate(ctx, msgUpdate(3, 100, 7, "alice", "eth"))
	items, _, _ := st.ListCases(ctx, store.CaseListFilter{Limit: 10})
	if len(items) != 0 {
		t.Fatalf("cancelled flow should not create a case, got %d", len(items))
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
	// A closed server address forces a connection-refused transport error,
	// whose *url.Error renders the request URL — which embeds the bot token.
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
