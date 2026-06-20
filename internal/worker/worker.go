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
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	"github.com/UPside-Lumos-V2/helios/internal/xfeed"
	"github.com/UPside-Lumos-V2/helios/internal/xpublish"
)

type Worker struct {
	Store                       *store.Store
	Runner                      *lumoskit.Runner
	Dispatcher                  *handoff.Dispatcher // nil iff no downstream URLs are configured
	Notifier                    *notify.Notifier    // nil iff no operator channel is configured
	GitHubPublisher             *githubpublish.Publisher
	XFeedRunner                 *xfeed.Runner
	XPublisher                  *xpublish.Publisher
	TelegramPublishEnabled      bool
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

	runOptions, resumePayload, err := w.prepareResume(ctx, c)
	if err != nil {
		log.Error("prepare resume failed", "err", err)
		if markErr := w.Store.MarkFailed(ctx, c.CaseID, "resume_prepare_failed"); markErr != nil {
			log.Error("mark failed after resume prepare error", "err", markErr)
		}
		return
	}
	if len(resumePayload) > 0 {
		log.Info("stage resume prepared", "stage", runOptions.Stage, "source_case_id", resumePayload["resume_source_case_id"])
	}

	if err := writeSignalContext(c); err != nil {
		log.Warn("write signal context failed", "err", err)
	}

	runStart := time.Now()
	res := w.Runner.RunWithOptions(ctx, c.Chain, c.TxHash, *c.OutputRoot, runOptions)
	runDuration := time.Since(runStart)
	metrics.LumoskitDurationSeconds.Observe(runDuration.Seconds())
	outcomeInput := outcome.Input{
		ExitCode:       res.ExitCode,
		SummaryBytes:   res.SummaryBytes,
		SummaryMissing: res.SummaryMissing,
		SummaryReadErr: res.SummaryReadErr,
		Stderr:         res.Stderr,
	}
	mapped := outcome.Map(outcomeInput)
	eventPayload := outcome.TerminalEventPayload(mapped, outcomeInput)
	eventPayload["duration_ms"] = runDuration.Milliseconds()
	if runOptions.Stage != "" {
		eventPayload["lumoskit_stage"] = runOptions.Stage
	}
	for key, value := range resumePayload {
		eventPayload[key] = value
	}
	w.mergeIncidentMetadata(ctx, c)
	if refreshed, err := w.Store.GetCase(ctx, c.CaseID); err == nil && refreshed != nil {
		c = refreshed
	} else if err != nil {
		log.Warn("refresh case after metadata merge failed", "err", err)
	}

	if len(res.Stderr) > 0 && (mapped.Outcome == outcome.OutcomeEngineError) {
		log.Warn("lumoskit stderr captured for engine_error case", "stderr_tail", string(res.Stderr))
	}
	w.annotateRerunEligibility(eventPayload, mapped, c.AttemptNumber)

	switch mapped.State {
	case outcome.StateDone:
		if w.shouldQueueRerun(mapped, c.AttemptNumber) {
			rerunReason := firstNonEmpty(mapped.RerunReason, mapped.AnalysisStage, mapped.RerunDecision, "rerun")
			resumeStage := autoRerunResumeStage(mapped)
			request := store.AutoRerunRequest{
				Reason:         rerunReason,
				ResumeStage:    resumeStage,
				Decision:       mapped.RerunDecision,
				RepairStrategy: rerunRepairStrategy(mapped),
			}
			child, queued, err := w.Store.MarkDoneAndQueueAutoRerun(ctx, c.CaseID, mapped.Outcome, eventPayload, request, w.PartialAutoRerunMaxAttempts)
			if err != nil {
				log.Error("auto rerun queue failed", "err", err, "outcome", mapped.Outcome)
				return
			}
			if queued {
				log.Info("rerun queued",
					"parent_case_id", c.CaseID,
					"child_case_id", child.CaseID,
					"next_attempt_number", child.AttemptNumber,
					"resume_stage", resumeStage,
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
		githubPublishQueued := w.publishGitHub(ctx, c, mapped)
		if !githubPublishQueued {
			w.publishX(ctx, c, mapped)
		}
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
		if !githubPublishQueued {
			w.notifyOutcome(ctx, c.CaseID, mapped.Outcome)
		}
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

func (w *Worker) mergeIncidentMetadata(ctx context.Context, c *store.Case) {
	if c.OutputRoot == nil {
		return
	}
	metadata := inferIncidentMetadata(*c.OutputRoot)
	if len(metadata) == 0 {
		return
	}
	if err := w.Store.MergeCaseMetadata(ctx, c.CaseID, metadata); err != nil {
		w.Logger.Warn("merge inferred incident metadata failed", "case_id", c.CaseID, "err", err)
	}
}

func (w *Worker) shouldQueueRerun(mapped outcome.Result, attemptNumber int) bool {
	return isQueuedRerunDecision(mapped.RerunDecision) && w.PartialAutoRerunMaxAttempts > 0 && attemptNumber < w.PartialAutoRerunMaxAttempts
}

func (w *Worker) annotateRerunEligibility(payload map[string]any, mapped outcome.Result, attemptNumber int) {
	if !isQueuedRerunDecision(mapped.RerunDecision) {
		return
	}
	eligible := w.shouldQueueRerun(mapped, attemptNumber)
	resumeStage := autoRerunResumeStage(mapped)
	payload["rerun_eligible"] = eligible
	payload["rerun_resume_stage"] = resumeStage
	payload["rerun_max_attempts"] = w.PartialAutoRerunMaxAttempts
	if strategy := rerunRepairStrategy(mapped); strategy != "" {
		payload["rerun_repair_strategy"] = strategy
	}
	// Preserve legacy auto_rerun fields for existing UI/tests/operators.
	payload["auto_rerun_eligible"] = eligible
	payload["auto_rerun_resume_stage"] = resumeStage
	payload["auto_rerun_max_attempts"] = w.PartialAutoRerunMaxAttempts
	if w.PartialAutoRerunMaxAttempts <= 0 {
		payload["rerun_blocked_reason"] = "disabled"
		payload["auto_rerun_blocked_reason"] = "disabled"
		return
	}
	if attemptNumber >= w.PartialAutoRerunMaxAttempts {
		payload["rerun_blocked_reason"] = "max_attempts_reached"
		payload["auto_rerun_blocked_reason"] = "max_attempts_reached"
	}
}

func isQueuedRerunDecision(decision string) bool {
	return decision == outcome.RerunDecisionAutoRerun || decision == outcome.RerunDecisionGuidedRepair
}

func rerunRepairStrategy(mapped outcome.Result) string {
	if mapped.RerunDecision == outcome.RerunDecisionGuidedRepair {
		return "economic_proof_guided_repair"
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type signalContextFile struct {
	Schema     string          `json:"schema"`
	CaseID     string          `json:"case_id"`
	Chain      string          `json:"chain"`
	TxHash     string          `json:"tx_hash"`
	Source     *string         `json:"source"`
	DetectedAt *string         `json:"detected_at"`
	Metadata   json.RawMessage `json:"metadata"`
	WrittenAt  string          `json:"written_at"`
}

func writeSignalContext(c *store.Case) error {
	if c == nil || c.OutputRoot == nil {
		return nil
	}
	metadata := c.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage("{}")
	}
	payload := signalContextFile{
		Schema:     "helios-signal-context-v1",
		CaseID:     c.CaseID,
		Chain:      c.Chain,
		TxHash:     c.TxHash,
		Source:     c.Source,
		DetectedAt: c.DetectedAt,
		Metadata:   metadata,
		WrittenAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*c.OutputRoot, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*c.OutputRoot, "helios_signal_context.json"), append(data, '\n'), 0o644)
}

func (w *Worker) publishGitHub(ctx context.Context, c *store.Case, mapped outcome.Result) bool {
	if !outcome.ShouldPublishGitHub(mapped) || w.GitHubPublisher == nil || !w.GitHubPublisher.Configured() || c.OutputRoot == nil {
		return false
	}
	publishCase := githubpublish.Case{
		CaseID:       c.CaseID,
		Chain:        c.Chain,
		TxHash:       c.TxHash,
		OutputRoot:   *c.OutputRoot,
		IncidentSlug: store.IncidentSlug(c),
		Outcome:      mapped.Outcome,
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer w.notifyOutcome(ctx, publishCase.CaseID, mapped.Outcome)
		res, err := w.GitHubPublisher.Publish(ctx, publishCase)
		if err != nil {
			w.Logger.Error("github publish failed", "case_id", publishCase.CaseID, "err", err)
			if recordErr := w.Store.AppendCaseEvent(ctx, publishCase.CaseID, "github_publish_failed", map[string]any{
				"published":    false,
				"error":        err.Error(),
				"outcome":      mapped.Outcome,
				"publish_tier": mapped.PublishTier,
				"poc_state":    mapped.PoCState,
				"rca_state":    mapped.RCAState,
			}); recordErr != nil {
				w.Logger.Error("record github publish failure event failed", "case_id", publishCase.CaseID, "err", recordErr)
			}
			return
		}
		if res != nil && !res.Published {
			if err := w.Store.AppendCaseEvent(ctx, publishCase.CaseID, "github_publish", map[string]any{
				"published":    false,
				"skipped":      true,
				"skip_reason":  res.SkipReason,
				"outcome":      mapped.Outcome,
				"publish_tier": mapped.PublishTier,
				"poc_state":    mapped.PoCState,
				"rca_state":    mapped.RCAState,
			}); err != nil {
				w.Logger.Error("record github publish skip event failed", "case_id", publishCase.CaseID, "err", err)
			}
			w.Logger.Info("github publish skipped",
				"case_id", publishCase.CaseID,
				"reason", res.SkipReason,
			)
			if !w.xFeedConfigured() {
				w.publishX(ctx, c, mapped)
			}
			return
		}
		if err := w.Store.AppendCaseEvent(ctx, publishCase.CaseID, "github_publish", map[string]any{
			"published":      true,
			"commit_sha":     res.CommitSHA,
			"target_dir":     res.TargetDir,
			"target_dir_url": res.TargetDirURL,
			"poc_url":        res.PoCURL,
			"report_url":     res.ReportURL,
			"commit_url":     res.CommitURL,
			"target_urls":    res.TargetURLs,
			"outcome":        mapped.Outcome,
			"publish_tier":   mapped.PublishTier,
			"poc_state":      mapped.PoCState,
			"rca_state":      mapped.RCAState,
		}); err != nil {
			w.Logger.Error("record github publish event failed", "case_id", publishCase.CaseID, "err", err)
		}
		if !w.publishXFeedThread(ctx, c, mapped, res) {
			w.publishX(ctx, c, mapped)
		}
		w.Logger.Info("github publish complete",
			"case_id", publishCase.CaseID,
			"commit_sha", res.CommitSHA,
			"target_dir", res.TargetDir,
		)
	}()
	return true
}

func (w *Worker) xFeedConfigured() bool {
	return w.XFeedRunner != nil && w.XFeedRunner.Configured()
}

func (w *Worker) publishXFeedThread(ctx context.Context, c *store.Case, mapped outcome.Result, githubRes *githubpublish.Result) bool {
	if !w.xFeedConfigured() || c.OutputRoot == nil {
		return false
	}
	if githubRes == nil || !githubRes.Published {
		return true
	}
	draft, err := w.XFeedRunner.Run(ctx, xfeed.Case{
		CaseID:       c.CaseID,
		Chain:        c.Chain,
		TxHash:       c.TxHash,
		OutputRoot:   *c.OutputRoot,
		IncidentSlug: store.IncidentSlug(c),
		Outcome:      mapped.Outcome,
		PublishTier:  mapped.PublishTier,
		PoCState:     mapped.PoCState,
		RCAState:     mapped.RCAState,
		ReportURL:    githubRes.ReportURL,
		PoCURL:       githubRes.PoCURL,
		GitHubURL:    githubRes.TargetDirURL,
	})
	if err != nil {
		w.Logger.Error("x feed draft failed", "case_id", c.CaseID, "err", err)
		if recordErr := w.Store.AppendCaseEvent(ctx, c.CaseID, "x_feed_draft_failed", map[string]any{
			"ready_to_publish": false,
			"error":            err.Error(),
			"outcome":          mapped.Outcome,
			"publish_tier":     mapped.PublishTier,
			"poc_state":        mapped.PoCState,
			"rca_state":        mapped.RCAState,
		}); recordErr != nil {
			w.Logger.Error("record x feed draft failure event failed", "case_id", c.CaseID, "err", recordErr)
		}
		return true
	}
	if err := w.Store.AppendCaseEvent(ctx, c.CaseID, "x_feed_draft", map[string]any{
		"ready_to_publish":   draft.ReadyToPublish,
		"status_label":       draft.StatusLabel,
		"main_post":          draft.MainPost,
		"reply_post":         draft.ReplyPost,
		"telegram_post":      draft.TelegramPost,
		"image_mode":         draft.ImageMode,
		"image_path":         draft.ImagePath,
		"card_brief_path":    draft.CardBriefPath,
		"card_svg_path":      draft.CardSVGPath,
		"card_png_path":      draft.CardPNGPath,
		"card_error":         draft.CardError,
		"format":             draft.Format,
		"source_format_path": draft.SourceFormat,
		"status_path":        draft.StatusPath,
		"main_post_path":     draft.MainPostPath,
		"reply_post_path":    draft.ReplyPostPath,
		"telegram_post_path": draft.TelegramPostPath,
		"explorer_url":       draft.ExplorerURL,
		"github_url":         draft.GitHubURL,
		"report_url":         draft.ReportURL,
		"poc_url":            draft.PoCURL,
		"blockers":           draft.Blockers,
		"outcome":            mapped.Outcome,
		"publish_tier":       mapped.PublishTier,
		"poc_state":          mapped.PoCState,
		"rca_state":          mapped.RCAState,
	}); err != nil {
		w.Logger.Error("record x feed draft event failed", "case_id", c.CaseID, "err", err)
	}
	if !draft.ReadyToPublish || !outcome.ShouldPublishX(mapped) || w.XPublisher == nil || !w.XPublisher.Configured() {
		return true
	}
	res, err := w.XPublisher.PublishThread(ctx, xpublish.Thread{
		MainText:  draft.MainPost,
		ReplyText: draft.ReplyPost,
		MediaPath: draft.ImagePath,
	})
	if err != nil {
		w.Logger.Error("x publish failed", "case_id", c.CaseID, "err", err)
		if recordErr := w.Store.AppendCaseEvent(ctx, c.CaseID, "x_publish_failed", map[string]any{
			"published":    false,
			"platform":     "x",
			"error":        err.Error(),
			"format":       draft.Format,
			"outcome":      mapped.Outcome,
			"publish_tier": mapped.PublishTier,
			"poc_state":    mapped.PoCState,
			"rca_state":    mapped.RCAState,
		}); recordErr != nil {
			w.Logger.Error("record x publish failure event failed", "case_id", c.CaseID, "err", recordErr)
		}
		return true
	}
	if err := w.Store.AppendCaseEvent(ctx, c.CaseID, "x_publish", map[string]any{
		"published":             res.Published,
		"dry_run":               res.DryRun,
		"platform":              res.Platform,
		"post_id":               res.PostID,
		"post_url":              res.PostURL,
		"reply_post_id":         res.ReplyPostID,
		"reply_post_url":        res.ReplyPostURL,
		"text":                  res.Text,
		"reply_text":            res.ReplyText,
		"template":              res.Template,
		"media_id":              res.MediaID,
		"refresh_returned":      res.RefreshReturned,
		"refresh_token_updated": res.RefreshTokenUpdated,
		"post_text_verified":    res.PostTextVerified,
		"reply_text_verified":   res.ReplyTextVerified,
		"outcome":               mapped.Outcome,
		"publish_tier":          mapped.PublishTier,
		"poc_state":             mapped.PoCState,
		"rca_state":             mapped.RCAState,
	}); err != nil {
		w.Logger.Error("record x publish event failed", "case_id", c.CaseID, "err", err)
	}
	w.Logger.Info("x publish complete",
		"case_id", c.CaseID,
		"published", res.Published,
		"dry_run", res.DryRun,
		"post_id", res.PostID,
		"reply_post_id", res.ReplyPostID,
	)
	if res.Published && !res.DryRun && w.TelegramPublishEnabled {
		w.publishTelegramPost(ctx, c, mapped, draft, res.PostURL)
	}
	return true
}

func (w *Worker) publishTelegramPost(ctx context.Context, c *store.Case, mapped outcome.Result, draft *xfeed.Result, xURL string) {
	text := telegramPostWithXURL(draft.TelegramPost, xURL)
	sent := false
	errText := ""
	if w.Notifier == nil || !w.Notifier.Configured() {
		errText = "telegram notifier is not configured"
	} else {
		refreshed, err := w.Store.GetCase(ctx, c.CaseID)
		if err != nil || refreshed == nil {
			errText = "could not refresh case before telegram publish"
			w.Logger.Warn("telegram publish skipped: could not refresh case", "case_id", c.CaseID, "err", err)
		} else {
			sent = w.Notifier.NotifyTelegramText(ctx, refreshed, notify.EventTelegramPublish, text)
			if !sent {
				errText = "telegram delivery failed or no telegram channel configured"
			}
		}
	}
	payload := map[string]any{
		"published":    sent,
		"platform":     "telegram",
		"text":         text,
		"github_url":   draft.GitHubURL,
		"x_url":        strings.TrimSpace(xURL),
		"report_url":   draft.ReportURL,
		"outcome":      mapped.Outcome,
		"publish_tier": mapped.PublishTier,
		"poc_state":    mapped.PoCState,
		"rca_state":    mapped.RCAState,
	}
	if errText != "" {
		payload["error"] = errText
	}
	if err := w.Store.AppendCaseEvent(ctx, c.CaseID, notify.EventTelegramPublish, payload); err != nil {
		w.Logger.Error("record telegram publish event failed", "case_id", c.CaseID, "err", err)
	}
}

func telegramPostWithXURL(text, xURL string) string {
	text = strings.TrimSpace(text)
	xURL = strings.TrimSpace(xURL)
	if xURL == "" {
		return text
	}
	if strings.Contains(text, "\nX:\n"+xURL) {
		return text
	}
	if text == "" {
		return "X:\n" + xURL
	}
	return text + "\n\nX:\n" + xURL
}

func (w *Worker) publishX(ctx context.Context, c *store.Case, mapped outcome.Result) bool {
	if !outcome.ShouldPublishX(mapped) || w.XPublisher == nil || !w.XPublisher.Configured() || c.OutputRoot == nil {
		return false
	}
	reportURL, pocURL := w.githubArtifactURLs(ctx, c.CaseID)
	publishCase := xpublish.Case{
		CaseID:       c.CaseID,
		Chain:        c.Chain,
		TxHash:       c.TxHash,
		OutputRoot:   *c.OutputRoot,
		IncidentSlug: store.IncidentSlug(c),
		ReportURL:    reportURL,
		PoCURL:       pocURL,
		Outcome:      mapped.Outcome,
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		res, err := w.XPublisher.Publish(ctx, publishCase)
		if err != nil {
			w.Logger.Error("x publish failed", "case_id", publishCase.CaseID, "err", err)
			if recordErr := w.Store.AppendCaseEvent(ctx, publishCase.CaseID, "x_publish_failed", map[string]any{
				"published":    false,
				"platform":     "x",
				"error":        err.Error(),
				"outcome":      mapped.Outcome,
				"publish_tier": mapped.PublishTier,
				"poc_state":    mapped.PoCState,
				"rca_state":    mapped.RCAState,
			}); recordErr != nil {
				w.Logger.Error("record x publish failure event failed", "case_id", publishCase.CaseID, "err", recordErr)
			}
			return
		}
		if err := w.Store.AppendCaseEvent(ctx, publishCase.CaseID, "x_publish", map[string]any{
			"published":             res.Published,
			"dry_run":               res.DryRun,
			"platform":              res.Platform,
			"post_id":               res.PostID,
			"post_url":              res.PostURL,
			"text":                  res.Text,
			"template":              res.Template,
			"refresh_returned":      res.RefreshReturned,
			"refresh_token_updated": res.RefreshTokenUpdated,
			"post_text_verified":    res.PostTextVerified,
			"outcome":               mapped.Outcome,
			"publish_tier":          mapped.PublishTier,
			"poc_state":             mapped.PoCState,
			"rca_state":             mapped.RCAState,
		}); err != nil {
			w.Logger.Error("record x publish event failed", "case_id", publishCase.CaseID, "err", err)
		}
		w.Logger.Info("x publish complete",
			"case_id", publishCase.CaseID,
			"published", res.Published,
			"dry_run", res.DryRun,
			"post_id", res.PostID,
		)
	}()
	return true
}

func (w *Worker) githubArtifactURLs(ctx context.Context, caseID string) (reportURL, pocURL string) {
	events, err := w.Store.CaseEvents(ctx, caseID)
	if err != nil {
		w.Logger.Warn("x publish could not read case events for github urls", "case_id", caseID, "err", err)
		return "", ""
	}
	for _, event := range events {
		if event.EventType != "github_publish" || len(event.Payload) == 0 {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(event.Payload, &payload) != nil {
			continue
		}
		if v, ok := payload["report_url"].(string); ok && v != "" {
			reportURL = v
		}
		if v, ok := payload["poc_url"].(string); ok && v != "" {
			pocURL = v
		}
	}
	return reportURL, pocURL
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
