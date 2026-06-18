package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// WebhookChannel POSTs the JSON Payload to a single OPERATOR_NOTIFY_WEBHOOK_URL.
// Retry / backoff is handled by the parent Notifier.
type WebhookChannel struct {
	URL    string
	Client *http.Client
}

func (w *WebhookChannel) Name() string { return "webhook" }

func (w *WebhookChannel) Deliver(ctx context.Context, p Payload) (int, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "backlight/1 notify=webhook")
	resp, err := w.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("operator webhook returned HTTP %d", resp.StatusCode)
}
