// Package worker drains the SQLite-backed FIFO queue, invokes the lumoskit
// child process per case, applies outcome mapping rules from
// internal/outcome, and writes the resulting (state, outcome, failure_kind)
// triple back through internal/store.
//
// Concurrency model: a single poll goroutine guards a semaphore of size
// MaxConcurrent. Each claimed case runs in its own goroutine. The package
// does not implement restart recovery, downstream fan-out, or operator
// notifications — those land in subsequent chunks.
package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/githubpublish"
	"github.com/UPside-Lumos-V2/helios/internal/handoff"
	"github.com/UPside-Lumos-V2/helios/internal/lumoskit"
	"github.com/UPside-Lumos-V2/helios/internal/metrics"
	"github.com/UPside-Lumos-V2/helios/internal/notify"
	"github.com/UPside-Lumos-V2/helios/internal/outcome"
	"github.com/UPside-Lumos-V2/helios/internal/prelumos"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

type Worker struct {
	Store                       *store.Store
	Runner                      *lumoskit.Runner
	Dispatcher                  *handoff.Dispatcher // nil iff no downstream URLs are configured
	Notifier                    *notify.Notifier    // nil iff no operator channel is configured
	GitHubPublisher             *githubpublish.Publisher
	PreLumosRunner              *prelumos.Runner
	PartialAutoRerunMaxAttempts int
	OutputRootParent            string
	MaxConcurrent               int
	PollInterval                time.Duration
	Logger                      *slog.Logger

	wg sync.WaitGroup
}

// downstreamConfigured returns true iff a Dispatcher with at least one URL
// is wired in. Worker uses this to decide whether MarkDone should also
// short-circuit to handed-off (skipped) per the seed.
func (w *Worker) downstreamConfigured() bool {
	return w.Dispatcher != nil && w.Dispatcher.Configured()
}

// Start spawns the poll goroutine. It returns immediately; call Wait to block
// until the worker has finished draining after ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	if w.PollInterval <= 0 {
		w.PollInterval = time.Second
	}
	if w.MaxConcurrent <= 0 {
		w.MaxConcurrent = 1
	}
	if w.Logger == nil {
		w.Logger = slog.Default()
	}

	w.wg.Add(1)
	go w.run(ctx)
}

// Wait blocks until the poll goroutine and every in-flight case have returned.
func (w *Worker) Wait() { w.wg.Wait() }

func (w *Worker) run(ctx context.Context) {
	defer w.wg.Done()

	sem := make(chan struct{}, w.MaxConcurrent)
	ticker := time.NewTicker(w.PollInterval)
	defer ticker.Stop()

	w.Logger.Info("worker started",
		"max_concurrent", w.MaxConcurrent,
		"poll_interval", w.PollInterval.String(),
		"output_root_parent", w.OutputRootParent,
	)

	for {
		select {
		case <-ctx.Done():
			w.Logger.Info("worker draining in-flight cases")
			return
		case <-ticker.C:
			w.tryDispatch(ctx, sem)
		}
	}
}

// tryDispatch is best-effort: it tries to take a semaphore slot, claim one
// queued case, and dispatch it. If no slot is available or no case is
// queued, it returns quietly until the next tick.
func (w *Worker) tryDispatch(ctx context.Context, sem chan struct{}) {
	select {
	case sem <- struct{}{}:
	default:
		return
	}

	c, err := w.Store.ClaimNextQueued(ctx, w.OutputRootParent)
	if err != nil {
		w.Logger.Error("claim next queued failed", "err", err)
		<-sem
		return
	}
	if c == nil {
		<-sem
		return
	}

	w.wg.Add(1)
	go func(claimed *store.Case) {
		defer w.wg.Done()
		defer func() { <-sem }()
		w.process(ctx, claimed)
	}(c)
}

func (w *Worker) process(ctx context.Context, c *store.Case) {
	log := w.Logger.With(
		"case_id", c.CaseID,
		"chain", c.Chain,
		"attempt_number", c.AttemptNumber,
	)
	log.Info("case dispatched")

	if c.OutputRoot == nil {
		// defensive — ClaimNextQueued always assigns this
		log.Error("claimed case has no output_root, treating as engine_error")
		if err := w.Store.MarkFailed(ctx, c.CaseID, "host_missing_output_root"); err != nil {
			log.Error("mark failed after missing output root", "err", err)
		}
		return
	}

	runStart := time.Now()
	res := w.Runner.Run(ctx, c.Chain, c.TxHash, *c.OutputRoot)
	runDuration := time.Since(runStart)
	metrics.LumoskitDurationSeconds.Observe(runDuration.Seconds())
	outcomeInput := outcome.Input{
		ExitCode:       res.ExitCode,
		SummaryBytes:   res.SummaryBytes,
		SummaryMissing: res.SummaryMissing,
		SummaryReadErr: res.SummaryReadErr,
	}
	mapped := outcome.Map(outcomeInput)
	eventPayload := outcome.TerminalEventPayload(mapped, outcomeInput)
	eventPayload["duration_ms"] = runDuration.Milliseconds()

	if len(res.Stderr) > 0 && (mapped.Outcome == outcome.OutcomeEngineError) {
		log.Warn("lumoskit stderr captured for engine_error case", "stderr_tail", string(res.Stderr))
	}

	switch mapped.State {
	case outcome.StateDone:
		if w.shouldAutoRerunPartial(mapped.Outcome, c.AttemptNumber) {
			child, queued, err := w.Store.MarkDoneAndQueueAutoRerun(ctx, c.CaseID, mapped.Outcome, eventPayload, "partial_auto_rerun", w.PartialAutoRerunMaxAttempts)
			if err != nil {
				log.Error("auto rerun queue failed", "err", err, "outcome", mapped.Outcome)
				return
			}
			if queued {
				log.Info("partial auto rerun queued",
					"parent_case_id", c.CaseID,
					"child_case_id", child.CaseID,
					"next_attempt_number", child.AttemptNumber,
					"max_attempts", w.PartialAutoRerunMaxAttempts,
				)
				return
			}
		}
		if err := w.Store.MarkDoneWithPayload(ctx, c.CaseID, mapped.Outcome, w.downstreamConfigured(), eventPayload); err != nil {
			log.Error("mark done failed", "err", err, "outcome", mapped.Outcome)
			return
		}
		log.Info("case complete", "state", mapped.State, "outcome", mapped.Outcome, "rule", mapped.Rule)
		w.publishGitHub(ctx, c, mapped.Outcome)
		w.runPreLumos(ctx, c, mapped.Outcome)
		// Trigger downstream fan-out asynchronously. With no URLs configured,
		// MarkDone already advanced the case to handed-off (handoff_status=skipped).
		if w.downstreamConfigured() {
			c.State = outcome.StateDone
			c.Outcome = &mapped.Outcome
			w.wg.Add(1)
			go func() {
				defer w.wg.Done()
				w.Dispatcher.Dispatch(ctx, c, nil)
			}()
		}
		w.notifyOutcome(ctx, c.CaseID, mapped.Outcome)
	case outcome.StateFailed:
		fk := ""
		if mapped.FailureKind != nil {
			fk = *mapped.FailureKind
		}
		if err := w.Store.MarkFailedWithPayload(ctx, c.CaseID, fk, eventPayload); err != nil {
			log.Error("mark failed update failed", "err", err, "failure_kind", fk)
			return
		}
		log.Info("case complete", "state", mapped.State, "outcome", mapped.Outcome, "failure_kind", fk, "rule", mapped.Rule)
		w.notifyOutcome(ctx, c.CaseID, mapped.Outcome)
	default:
		log.Error("outcome mapping returned unexpected state", "state", mapped.State)
	}
}

func (w *Worker) shouldAutoRerunPartial(mappedOutcome string, attemptNumber int) bool {
	return mappedOutcome == outcome.OutcomePartial && w.PartialAutoRerunMaxAttempts > 0 && attemptNumber < w.PartialAutoRerunMaxAttempts
}

func (w *Worker) publishGitHub(ctx context.Context, c *store.Case, mappedOutcome string) {
	if mappedOutcome != outcome.OutcomeVerified || w.GitHubPublisher == nil || !w.GitHubPublisher.Configured() || c.OutputRoot == nil {
		return
	}
	publishCase := githubpublish.Case{
		CaseID:     c.CaseID,
		Chain:      c.Chain,
		TxHash:     c.TxHash,
		OutputRoot: *c.OutputRoot,
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		res, err := w.GitHubPublisher.Publish(ctx, publishCase)
		if err != nil {
			w.Logger.Error("github publish failed", "case_id", publishCase.CaseID, "err", err)
			if recordErr := w.Store.AppendCaseEvent(ctx, publishCase.CaseID, "github_publish_failed", map[string]any{
				"published": false,
				"error":     err.Error(),
			}); recordErr != nil {
				w.Logger.Error("record github publish failure event failed", "case_id", publishCase.CaseID, "err", recordErr)
			}
			return
		}
		if err := w.Store.AppendCaseEvent(ctx, publishCase.CaseID, "github_publish", map[string]any{
			"published":   true,
			"commit_sha":  res.CommitSHA,
			"target_dir":  res.TargetDir,
			"poc_url":     res.PoCURL,
			"report_url":  res.ReportURL,
			"commit_url":  res.CommitURL,
			"target_urls": res.TargetURLs,
		}); err != nil {
			w.Logger.Error("record github publish event failed", "case_id", publishCase.CaseID, "err", err)
		}
		w.Logger.Info("github publish complete",
			"case_id", publishCase.CaseID,
			"commit_sha", res.CommitSHA,
			"target_dir", res.TargetDir,
		)
	}()
}

func (w *Worker) runPreLumos(ctx context.Context, c *store.Case, mappedOutcome string) {
	if mappedOutcome != outcome.OutcomeVerified || w.PreLumosRunner == nil || !w.PreLumosRunner.Configured() || c.OutputRoot == nil {
		return
	}
	preLumosCase := prelumos.Case{
		CaseID:     c.CaseID,
		OutputRoot: *c.OutputRoot,
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		res, err := w.PreLumosRunner.Run(ctx, preLumosCase)
		if err != nil {
			w.Logger.Error("pre-lumos sync failed", "case_id", preLumosCase.CaseID, "err", err, "stderr_tail", string(res.Stderr))
			if recordErr := w.Store.AppendCaseEvent(ctx, preLumosCase.CaseID, "pre_lumos_sync_failed", map[string]any{
				"synced":      false,
				"error":       err.Error(),
				"exit_code":   res.ExitCode,
				"status_path": res.StatusPath,
				"output_path": res.OutputPath,
			}); recordErr != nil {
				w.Logger.Error("record pre-lumos failure event failed", "case_id", preLumosCase.CaseID, "err", recordErr)
			}
			return
		}
		if err := w.Store.AppendCaseEvent(ctx, preLumosCase.CaseID, "pre_lumos_sync", map[string]any{
			"synced":       true,
			"row_count":    res.RowCount,
			"slugs":        res.Slugs,
			"target_files": res.TargetFiles,
			"status_path":  res.StatusPath,
			"output_path":  res.OutputPath,
			"dry_run":      res.DryRun,
		}); err != nil {
			w.Logger.Error("record pre-lumos event failed", "case_id", preLumosCase.CaseID, "err", err)
		}
		w.Logger.Info("pre-lumos sync complete",
			"case_id", preLumosCase.CaseID,
			"row_count", res.RowCount,
			"target_files", res.TargetFiles,
		)
	}()
}

// notifyOutcome fires the appropriate operator notification for an outcome.
// Per seed event names: verified | partial | unverified | engine_error.
// Notification is async so worker turnover stays fast. No-op when notifier
// is not configured.
func (w *Worker) notifyOutcome(ctx context.Context, caseID, outcomeStr string) {
	if w.Notifier == nil || !w.Notifier.Configured() {
		return
	}
	refreshed, err := w.Store.GetCase(ctx, caseID)
	if err != nil || refreshed == nil {
		w.Logger.Warn("notify skipped: could not refresh case", "case_id", caseID, "err", err)
		return
	}
	event := outcomeStr // outcome enum already matches notify event names 1:1 for these four
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.Notifier.Notify(ctx, refreshed, event)
	}()
}
