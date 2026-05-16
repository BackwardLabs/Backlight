// Package handoff dispatches a "case is done" event to every URL configured
// in HELIOS_DOWNSTREAM_WEBHOOK_URLS, with bounded exponential backoff per URL.
//
// Aggregation rules per seeds/v1.yaml:
//   - handoff_status=succeeded iff every URL gets at least one 2xx response.
//   - handoff_status=failed     iff any single URL exhausts its retry budget.
//   - handoff_status=retrying   while any URL is mid-budget.
//   - handoff_status=skipped    when the URL list is empty (handled by the
//     worker before Dispatch is even called) or
//     when state=failed/outcome=engine_error.
//
// On full success the case advances state=done -> handed-off in the same
// SQLite transaction that flips handoff_status to succeeded.
package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/metrics"
	"github.com/UPside-Lumos-V2/helios/internal/notify"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

type Dispatcher struct {
	Store       *store.Store
	Client      *http.Client
	URLs        []string
	MaxAttempts int
	BackoffBase time.Duration
	BackoffMax  time.Duration
	Notifier    *notify.Notifier // nil iff no operator channel is configured
	Logger      *slog.Logger

	wg sync.WaitGroup
}

// Wait blocks until every in-flight Dispatch call returns.
func (d *Dispatcher) Wait() { d.wg.Wait() }

// Configured reports whether any downstream URL is configured. The worker
// uses this to decide whether to short-circuit (state=done -> handed-off
// with handoff_status=skipped) inside MarkDone.
func (d *Dispatcher) Configured() bool { return len(d.URLs) > 0 }

// Payload is the JSON body POSTed to every downstream URL.
type Payload struct {
	CaseID          string  `json:"case_id"`
	Chain           string  `json:"chain"`
	TxHash          string  `json:"tx_hash"`
	State           string  `json:"state"`
	Outcome         string  `json:"outcome"`
	FailureKind     *string `json:"failure_kind"`
	OutputRoot      *string `json:"output_root"`
	SummaryJSONPath *string `json:"summary_json_path"`
	AttemptNumber   int     `json:"attempt_number"`
}

func payloadFromCase(c *store.Case) Payload {
	return Payload{
		CaseID:          c.CaseID,
		Chain:           c.Chain,
		TxHash:          c.TxHash,
		State:           c.State,
		Outcome:         derefString(c.Outcome),
		FailureKind:     c.FailureKind,
		OutputRoot:      c.OutputRoot,
		SummaryJSONPath: c.SummaryJSONPath,
		AttemptNumber:   c.AttemptNumber,
	}
}

// Dispatch runs the per-URL fan-out for a single case. It blocks until every
// URL succeeds, fails, or ctx is cancelled. Callers typically run this in
// its own goroutine.
//
// urlsToTry restricts the dispatch to a subset of d.URLs (used by retry-handoff
// to avoid re-delivering to URLs that already succeeded). Pass nil to attempt
// every configured URL.
func (d *Dispatcher) Dispatch(ctx context.Context, c *store.Case, urlsToTry []string) {
	if !d.Configured() {
		return
	}

	d.wg.Add(1)
	defer d.wg.Done()

	log := d.Logger.With("case_id", c.CaseID, "outcome", derefString(c.Outcome))

	// Move the case to retrying so an operator polling the API sees we're
	// working on it. Best-effort — even if this fails we proceed.
	if err := d.Store.SetHandoffStatus(ctx, c.CaseID, "retrying", false); err != nil {
		log.Warn("could not set handoff_status=retrying", "err", err)
	}

	targets := urlsToTry
	if targets == nil {
		targets = d.URLs
	}

	payload := payloadFromCase(c)
	body, err := json.Marshal(payload)
	if err != nil {
		log.Error("payload marshal failed", "err", err)
		_ = d.Store.SetHandoffStatus(ctx, c.CaseID, "failed", false)
		return
	}

	allOK := true
	for _, url := range targets {
		ok := d.deliverOneURL(ctx, c.CaseID, url, body, log)
		if !ok {
			allOK = false
			// Per seed: handoff_status flips to failed as soon as any
			// single URL exhausts its retry budget. Surface that early
			// so the operator can see partial progress.
			if err := d.Store.SetHandoffStatus(ctx, c.CaseID, "failed", false); err != nil {
				log.Error("set handoff_status=failed", "err", err)
			}
			// Continue trying the remaining URLs anyway — the operator
			// gains nothing from us giving up after the first failure,
			// and successful targets still deserve their delivery.
		}
		if ctx.Err() != nil {
			log.Info("dispatch cancelled mid-flight", "remaining_urls", "(skipped)")
			return
		}
	}

	if allOK {
		if err := d.Store.SetHandoffStatus(ctx, c.CaseID, "succeeded", true); err != nil {
			log.Error("set handoff_status=succeeded failed", "err", err)
			return
		}
		log.Info("handoff complete", "urls", len(targets))
		return
	}

	log.Warn("handoff finished with failures", "urls", len(targets))
	// Fire the handoff_failed operator notification once, after every URL
	// has had its full retry budget. Refresh the case so the payload reflects
	// the post-failure handoff_status=failed.
	if d.Notifier != nil && d.Notifier.Configured() {
		refreshed, err := d.Store.GetCase(ctx, c.CaseID)
		if err != nil || refreshed == nil {
			log.Warn("handoff_failed notify skipped: could not refresh case", "err", err)
			return
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.Notifier.Notify(ctx, refreshed, notify.EventHandoffFailed)
		}()
	}
}

// deliverOneURL runs the bounded exponential backoff loop for a single URL.
// Returns true iff at least one attempt got a 2xx response within the budget.
func (d *Dispatcher) deliverOneURL(ctx context.Context, caseID, url string, body []byte, log *slog.Logger) bool {
	for attempt := 1; attempt <= d.MaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		status, err := d.postOnce(ctx, url, body)
		result := classifyResult(status, err)
		var stored *int
		if status > 0 {
			stored = &status
		}
		var errMsg *string
		if err != nil {
			s := err.Error()
			errMsg = &s
		}
		metrics.ObserveHandoffAttempt(result)
		if recordErr := d.Store.RecordHandoffAttempt(ctx, store.HandoffAttempt{
			AttemptID:             store.NewID("hoa"),
			CaseID:                caseID,
			TargetURL:             url,
			AttemptedAt:           time.Now().UTC().Format(time.RFC3339Nano),
			HTTPStatus:            stored,
			Result:                result,
			Error:                 errMsg,
			AttemptIndexForTarget: attempt,
		}); recordErr != nil {
			log.Error("record handoff_attempt", "err", recordErr, "target_url", url, "attempt", attempt)
		}

		if result == "success" {
			return true
		}
		if result == "permanent_failure" {
			// 4xx-class — retrying won't help.
			return false
		}
		if attempt == d.MaxAttempts {
			return false
		}

		sleep := d.backoff(attempt)
		log.Info("handoff retry scheduled", "target_url", url, "attempt", attempt, "next_sleep", sleep.String())
		select {
		case <-ctx.Done():
			return false
		case <-time.After(sleep):
		}
	}
	return false
}

func (d *Dispatcher) postOnce(ctx context.Context, url string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "helios/1")
	resp, err := d.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// Drain response so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("downstream returned HTTP %d", resp.StatusCode)
}

func classifyResult(status int, err error) string {
	if err == nil && status >= 200 && status < 300 {
		return "success"
	}
	// 4xx is permanent (client-side error in our payload or auth)
	if status >= 400 && status < 500 {
		return "permanent_failure"
	}
	return "retryable_failure"
}

// backoff returns the delay BEFORE the next attempt, given the attempt index
// that just completed. The seed defines exponential growth from base capped
// at max. Pure function so callers can unit-test the schedule.
func (d *Dispatcher) backoff(completedAttempt int) time.Duration {
	if completedAttempt < 1 {
		completedAttempt = 1
	}
	exp := math.Pow(2, float64(completedAttempt-1))
	delay := time.Duration(float64(d.BackoffBase) * exp)
	if delay > d.BackoffMax {
		delay = d.BackoffMax
	}
	return delay
}

// PendingURLsForRetry returns URLs that have NOT yet succeeded according to
// the recorded handoff_attempts. Used by retry-handoff to avoid re-delivering
// to targets that already accepted the case.
func (d *Dispatcher) PendingURLsForRetry(ctx context.Context, caseID string) ([]string, error) {
	attempts, err := d.Store.HandoffAttempts(ctx, caseID)
	if err != nil {
		return nil, err
	}
	succeeded := map[string]bool{}
	for _, a := range attempts {
		if a.Result == "success" {
			succeeded[a.TargetURL] = true
		}
	}
	var out []string
	for _, u := range d.URLs {
		if !succeeded[u] {
			out = append(out, u)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no URLs pending — every configured downstream already succeeded")
	}
	return out, nil
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
