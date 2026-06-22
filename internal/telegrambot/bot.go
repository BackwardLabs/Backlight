// Package telegrambot adds an inbound Telegram command surface to Backlight.
//
// Backlight's notify package already sends outbound operator alerts via the
// Telegram Bot API. This package adds the opposite direction: an authorized
// operator submits an incident and lists recent ones by chatting with the bot.
//
// Updates are received by long-polling getUpdates — no public ingress, TLS
// cert, or webhook is required; the bot only makes the same outbound calls to
// api.telegram.org that notify already does. Submissions use the
// manual-operator semantics of POST /cases (Store.SubmitCase with synthesized
// source/detected_at), NOT the strict hack-detector /signals contract, because
// a human cannot supply the lumos_signal_to_helios.v1 envelope by hand.
package telegrambot

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

// IncidentMetadataResolver mirrors the optional enrichment POST /cases applies,
// so Telegram submissions get the same best-effort incident identity before the
// immutable case_id is allocated.
type IncidentMetadataResolver interface {
	EnrichIncidentMetadata(ctx context.Context, chain, txHash string, metadata json.RawMessage) (json.RawMessage, error)
}

// Config wires a Bot. BotToken, AllowedChatIDs, and Store are required for the
// bot to actually run (see Configured).
type Config struct {
	BotToken       string
	APIBase        string
	AllowedChatIDs []string
	PollTimeout    time.Duration
	Store          *store.Store
	Resolver       IncidentMetadataResolver
	Client         *http.Client
	Logger         *slog.Logger
	Now            func() time.Time
}

type Bot struct {
	client       *apiClient
	store        *store.Store
	resolver     IncidentMetadataResolver
	allowedChats map[int64]bool
	pollTimeout  time.Duration
	now          func() time.Time
	log          *slog.Logger

	mu       sync.Mutex
	sessions map[string]*session
	wg       sync.WaitGroup
}

const (
	defaultPollTimeout = 30 * time.Second
	sessionTTL         = 5 * time.Minute
	pollErrorBackoff   = 3 * time.Second
)

func New(cfg Config) *Bot {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	pollTimeout := cfg.PollTimeout
	if pollTimeout <= 0 {
		pollTimeout = defaultPollTimeout
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	client := cfg.Client
	if client == nil {
		// No global client timeout: per-request context deadlines bound every
		// call, and long-poll getUpdates intentionally outlives a 30s timeout.
		client = &http.Client{}
	}
	allowed := map[int64]bool{}
	for _, raw := range cfg.AllowedChatIDs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			logger.Warn("telegram command bot: ignoring non-numeric allowed chat id", "value", raw)
			continue
		}
		allowed[id] = true
	}
	return &Bot{
		client:       &apiClient{botToken: cfg.BotToken, apiBase: cfg.APIBase, client: client},
		store:        cfg.Store,
		resolver:     cfg.Resolver,
		allowedChats: allowed,
		pollTimeout:  pollTimeout,
		now:          now,
		log:          logger,
		sessions:     map[string]*session{},
	}
}

// Configured reports whether the bot has the minimum needed to run.
func (b *Bot) Configured() bool {
	return b.client.botToken != "" && len(b.allowedChats) > 0 && b.store != nil
}

// Start launches the long-poll loop in a background goroutine and returns
// immediately. Call Wait during shutdown to drain it.
func (b *Bot) Start(ctx context.Context) {
	if !b.Configured() {
		b.log.Warn("telegram command bot not started: missing bot token, allowed chats, or store")
		return
	}
	b.wg.Add(1)
	go b.loop(ctx)
}

// Wait blocks until the long-poll loop has exited. Safe to call even if Start
// never launched the loop.
func (b *Bot) Wait() { b.wg.Wait() }

func (b *Bot) loop(ctx context.Context) {
	defer b.wg.Done()
	// Long-polling and a registered webhook can't coexist on one bot; clear any
	// stale webhook so getUpdates doesn't fail with 409 Conflict.
	if err := b.client.deleteWebhook(ctx); err != nil && ctx.Err() == nil {
		b.log.Warn("telegram deleteWebhook failed", "err", err)
	}
	// Register the command menu so clients show the list when a user types "/".
	if err := b.client.setMyCommands(ctx, botCommands()); err != nil && ctx.Err() == nil {
		b.log.Warn("telegram setMyCommands failed", "err", err)
	}
	var offset int64
	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := b.client.getUpdates(ctx, offset, b.pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.log.Warn("telegram getUpdates failed", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollErrorBackoff):
			}
			continue
		}
		for i := range updates {
			u := updates[i]
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			b.handleUpdate(ctx, u)
		}
	}
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if err := b.client.sendMessage(ctx, chatID, text); err != nil {
		b.log.Warn("telegram sendMessage failed", "err", err, "chat_id", chatID)
	}
}
