// Package notify dispatches operator-facing notifications for the five
// lifecycle events listed in seeds/v1.yaml: verified, partial, unverified,
// engine_error, and handoff_failed.
//
// Per the seed:
//   - exactly two native channels in v1: HTTP webhook and Telegram bot;
//   - each channel is enabled iff its env vars are set;
//   - notification failure NEVER changes case state and NEVER blocks
//     downstream fan-out;
//   - per-channel retries use bounded exponential backoff governed by
//     HELIOS_NOTIFY_RETRY_* env vars.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/metrics"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

const (
	EventVerified      = "verified"
	EventPartial       = "partial"
	EventUnverified    = "unverified"
	EventEngineError   = "engine_error"
	EventHandoffFailed = "handoff_failed"
)

// Channel is the surface every notification transport implements.
type Channel interface {
	Name() string // "webhook" | "telegram"
	Deliver(ctx context.Context, payload Payload) (httpStatus int, err error)
}

// Payload is the structured data each event carries. WebhookChannel sends it
// as JSON; TelegramChannel renders it as a short text message.
type Payload struct {
	Event           string  `json:"event"`
	CaseID          string  `json:"case_id"`
	Chain           string  `json:"chain"`
	TxHash          string  `json:"tx_hash"`
	State           string  `json:"state"`
	Outcome         string  `json:"outcome"`
	FailureKind     *string `json:"failure_kind"`
	HandoffStatus   string  `json:"handoff_status"`
	OutputRoot      *string `json:"output_root"`
	SummaryJSONPath *string `json:"summary_json_path"`
	AttemptNumber   int     `json:"attempt_number"`
}

func PayloadFromCase(c *store.Case, event string) Payload {
	return Payload{
		Event:           event,
		CaseID:          c.CaseID,
		Chain:           c.Chain,
		TxHash:          c.TxHash,
		State:           c.State,
		Outcome:         derefString(c.Outcome),
		FailureKind:     c.FailureKind,
		HandoffStatus:   c.HandoffStatus,
		OutputRoot:      c.OutputRoot,
		SummaryJSONPath: c.SummaryJSONPath,
		AttemptNumber:   c.AttemptNumber,
	}
}

// Notifier fans an event out to every enabled channel with per-channel retry.
type Notifier struct {
	Store       *store.Store
	Channels    []Channel
	MaxAttempts int
	BackoffBase time.Duration
	BackoffMax  time.Duration
	Logger      *slog.Logger

	wg sync.WaitGroup
}

func (n *Notifier) Wait() { n.wg.Wait() }

// Configured returns true iff at least one channel is enabled.
func (n *Notifier) Configured() bool { return len(n.Channels) > 0 }

// Notify is fire-and-forget — callers spawn this in a goroutine if they want
// to bound their own latency. Setting notification_status is best-effort:
// failures here never block the caller (per seed).
//
// EmitOnDispatch=true sets notification_status=retrying immediately, so the
// API surface reflects "we're working on it" between Notify being called and
// the first delivery completing.
func (n *Notifier) Notify(ctx context.Context, c *store.Case, event string) {
	if !n.Configured() {
		// Disabled at startup: notification_status is already "disabled"
		// and never changes again.
		return
	}

	n.wg.Add(1)
	defer n.wg.Done()

	log := n.Logger.With("case_id", c.CaseID, "event", event)

	if err := n.Store.SetNotificationStatus(ctx, c.CaseID, "retrying"); err != nil {
		log.Warn("set notification_status=retrying failed", "err", err)
	}

	payload := PayloadFromCase(c, event)
	allOK := true
	for _, ch := range n.Channels {
		if !n.deliverChannel(ctx, ch, payload, log) {
			allOK = false
		}
		if ctx.Err() != nil {
			log.Info("notify cancelled mid-flight")
			break
		}
	}

	final := "succeeded"
	if !allOK {
		final = "failed"
	}
	if err := n.Store.SetNotificationStatus(ctx, c.CaseID, final); err != nil {
		log.Warn("set notification_status final failed", "err", err, "final", final)
	}
}

// deliverChannel runs the bounded exp backoff loop for one channel.
// Returns true iff at least one attempt succeeded within the budget.
func (n *Notifier) deliverChannel(ctx context.Context, ch Channel, payload Payload, log *slog.Logger) bool {
	for attempt := 1; attempt <= n.MaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		status, err := ch.Deliver(ctx, payload)
		result := classify(status, err)

		var errMsg *string
		if err != nil {
			s := err.Error()
			errMsg = &s
		}
		metrics.ObserveNotificationAttempt(ch.Name(), result)
		if recordErr := n.Store.RecordNotificationAttempt(ctx, store.NotificationAttempt{
			AttemptID:    store.NewID("noa"),
			CaseID:       payload.CaseID,
			Channel:      ch.Name(),
			Event:        payload.Event,
			AttemptedAt:  time.Now().UTC().Format(time.RFC3339Nano),
			Result:       result,
			Error:        errMsg,
			AttemptIndex: attempt,
		}); recordErr != nil {
			log.Error("record notification_attempt", "err", recordErr, "channel", ch.Name(), "attempt", attempt)
		}

		if result == "success" {
			return true
		}
		if result == "permanent_failure" {
			return false
		}
		if attempt == n.MaxAttempts {
			return false
		}

		sleep := n.backoff(attempt)
		log.Info("notification retry scheduled", "channel", ch.Name(), "attempt", attempt, "next_sleep", sleep.String())
		select {
		case <-ctx.Done():
			return false
		case <-time.After(sleep):
		}
	}
	return false
}

func classify(status int, err error) string {
	if err == nil && status >= 200 && status < 300 {
		return "success"
	}
	if status >= 400 && status < 500 {
		return "permanent_failure"
	}
	return "retryable_failure"
}

func (n *Notifier) backoff(completedAttempt int) time.Duration {
	if completedAttempt < 1 {
		completedAttempt = 1
	}
	exp := math.Pow(2, float64(completedAttempt-1))
	delay := time.Duration(float64(n.BackoffBase) * exp)
	if delay > n.BackoffMax {
		delay = n.BackoffMax
	}
	return delay
}

// MarshalForChannelTest exposes the JSON encoding used by webhook deliveries.
// Only callers in this package and tests should use it.
func MarshalForChannelTest(p Payload) ([]byte, error) {
	return json.Marshal(p)
}

// SentinelNoChannelsConfigured is returned by Notify ... actually unused here
// but reserved for future error paths.
var SentinelNoChannelsConfigured = errors.New("no notification channels configured")

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
