package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Update is a single Telegram update. The bot requests message and
// callback_query updates, so exactly one of Message / CallbackQuery is set.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// Message is the subset of a Telegram message the command bot reads.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

// CallbackQuery is an inline-button tap. Its Message is the message the button
// is attached to (carrying the chat + message_id to edit). Button taps are
// delivered regardless of group privacy mode, which is why the bot leans on
// them for everything except the unavoidable free-text tx hash.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// inlineKeyboardMarkup / inlineKeyboardButton model Telegram inline keyboards.
// A button with CallbackData posts a callback_query when tapped; a button with
// URL opens a link.
type inlineKeyboardMarkup struct {
	InlineKeyboard [][]inlineKeyboardButton `json:"inline_keyboard"`
}

type inlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// apiClient is a thin Telegram Bot API client covering only the methods the
// command bot needs: long-poll getUpdates plus sendMessage (and a best-effort
// deleteWebhook at startup). apiBase defaults to https://api.telegram.org so
// tests can point it at an httptest server.
type apiClient struct {
	botToken string
	apiBase  string
	client   *http.Client
}

func (c *apiClient) endpoint(method string) string {
	base := c.apiBase
	if base == "" {
		base = "https://api.telegram.org"
	}
	base = strings.TrimRight(base, "/")
	return fmt.Sprintf("%s/bot%s/%s", base, c.botToken, method)
}

type getUpdatesRequest struct {
	Offset         int64    `json:"offset"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

type getUpdatesResponse struct {
	OK          bool     `json:"ok"`
	Result      []Update `json:"result"`
	Description string   `json:"description"`
}

// getUpdates long-polls Telegram: the request is held open server-side for up
// to pollTimeout and returns the instant a message arrives, so operator
// commands are answered with near-real-time latency. The HTTP deadline is the
// poll timeout plus a margin so the held connection returns normally before the
// client gives up.
func (c *apiClient) getUpdates(ctx context.Context, offset int64, pollTimeout time.Duration) ([]Update, error) {
	timeoutSec := int(pollTimeout / time.Second)
	if timeoutSec < 0 {
		timeoutSec = 0
	}
	body, err := json.Marshal(getUpdatesRequest{
		Offset:         offset,
		Timeout:        timeoutSec,
		AllowedUpdates: []string{"message", "callback_query"},
	})
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, pollTimeout+10*time.Second)
	defer cancel()
	var decoded getUpdatesResponse
	if err := c.doJSON(reqCtx, "getUpdates", body, &decoded); err != nil {
		return nil, err
	}
	if !decoded.OK {
		return nil, fmt.Errorf("telegram getUpdates not ok: %s", decoded.Description)
	}
	return decoded.Result, nil
}

type sendMessageRequest struct {
	ChatID      int64                 `json:"chat_id"`
	Text        string                `json:"text"`
	ReplyMarkup *inlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

func (c *apiClient) sendMessage(ctx context.Context, chatID int64, text string) error {
	return c.sendMessageMarkup(ctx, chatID, text, nil)
}

func (c *apiClient) sendMessageMarkup(ctx context.Context, chatID int64, text string, markup *inlineKeyboardMarkup) error {
	body, err := json.Marshal(sendMessageRequest{ChatID: chatID, Text: text, ReplyMarkup: markup})
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var decoded struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := c.doJSON(reqCtx, "sendMessage", body, &decoded); err != nil {
		return err
	}
	if !decoded.OK {
		return fmt.Errorf("telegram sendMessage not ok: %s", decoded.Description)
	}
	return nil
}

type editMessageTextRequest struct {
	ChatID      int64                 `json:"chat_id"`
	MessageID   int64                 `json:"message_id"`
	Text        string                `json:"text"`
	ReplyMarkup *inlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// editMessageText rewrites an existing message in place (used to advance the
// inline-button flow on the same message the operator tapped).
func (c *apiClient) editMessageText(ctx context.Context, chatID, messageID int64, text string, markup *inlineKeyboardMarkup) error {
	body, err := json.Marshal(editMessageTextRequest{ChatID: chatID, MessageID: messageID, Text: text, ReplyMarkup: markup})
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var decoded struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := c.doJSON(reqCtx, "editMessageText", body, &decoded); err != nil {
		return err
	}
	if !decoded.OK {
		return fmt.Errorf("telegram editMessageText not ok: %s", decoded.Description)
	}
	return nil
}

// answerCallbackQuery acknowledges a button tap so the client stops showing the
// loading spinner. Best-effort; the operator-visible result is the edited
// message, not this ack.
func (c *apiClient) answerCallbackQuery(ctx context.Context, callbackID, text string) error {
	payload := map[string]any{"callback_query_id": callbackID}
	if text != "" {
		payload["text"] = text
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.doJSON(reqCtx, "answerCallbackQuery", body, nil)
}

// deleteWebhook is best-effort: long-poll getUpdates and a registered webhook
// are mutually exclusive on one bot, so clearing any stale webhook at startup
// keeps polling from failing with 409 Conflict.
func (c *apiClient) deleteWebhook(ctx context.Context) error {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.doJSON(reqCtx, "deleteWebhook", []byte(`{}`), nil)
}

type botCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// setMyCommands registers the bot's command menu so Telegram clients show the
// list (with descriptions) when a user types "/". Idempotent — it overwrites
// the previously registered set.
func (c *apiClient) setMyCommands(ctx context.Context, commands []botCommand) error {
	body, err := json.Marshal(map[string]any{"commands": commands})
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var decoded struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := c.doJSON(reqCtx, "setMyCommands", body, &decoded); err != nil {
		return err
	}
	if !decoded.OK {
		return fmt.Errorf("telegram setMyCommands not ok: %s", decoded.Description)
	}
	return nil
}

// doJSON performs the request and guarantees the bot token never escapes in a
// returned error. The token is embedded in every request URL
// (.../bot<token>/<method>), so Go renders it verbatim inside *url.Error
// transport failures; redactToken is the single choke point that keeps it out
// of operator logs.
func (c *apiClient) doJSON(ctx context.Context, method string, body []byte, out any) error {
	return c.redactToken(c.roundTrip(ctx, method, body, out))
}

func (c *apiClient) redactToken(err error) error {
	if err == nil || c.botToken == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), c.botToken, "***")
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}

func (c *apiClient) roundTrip(ctx context.Context, method string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "backlight/1 telegram=command-bot")
	client := c.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram %s returned HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode telegram %s response: %w", method, err)
		}
	}
	return nil
}
