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

// Update is a single Telegram update. The command bot only requests message
// updates, so non-message update kinds arrive with a nil Message.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message is the subset of a Telegram message the command bot reads.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
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
		AllowedUpdates: []string{"message"},
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
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

func (c *apiClient) sendMessage(ctx context.Context, chatID int64, text string) error {
	body, err := json.Marshal(sendMessageRequest{ChatID: chatID, Text: text})
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

// deleteWebhook is best-effort: long-poll getUpdates and a registered webhook
// are mutually exclusive on one bot, so clearing any stale webhook at startup
// keeps polling from failing with 409 Conflict.
func (c *apiClient) deleteWebhook(ctx context.Context) error {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.doJSON(reqCtx, "deleteWebhook", []byte(`{}`), nil)
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
