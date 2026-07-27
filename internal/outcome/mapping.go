// Package outcome maps a lumoskit child-process exit + its run summary into
// the (state, outcome, failure_kind) triple defined by seeds/v1.yaml.
//
// The seed names eight rules (O1..O8). They MUST be evaluated in this strict
// order so the first matching rule wins:
//
//	O5 → O6 → O7 → O4 → O1 → O2 → O3 → O8
package outcome

import (
	"encoding/json"
	"strings"
)

// Result is what the worker writes back onto the case row after lumoskit exits.
type Result struct {
	State         string  // queued | running | done | handed-off | failed
	Outcome       string  // verified | partial | unverified | engine_error
	FailureKind   *string // populated iff Outcome == engine_error
	Rule          string  // O1..O8 — the rule that fired (debug/audit)
	AnalysisStage string  // success | reachable_poc | poc_missing | poc_failed | rca_blocked | engine_error | unknown
	PoCState      string  // economic_poc | reachable_poc | poc_missing | poc_failed | poc_unknown
	RCAState      string  // rca_complete | rca_not_run | rca_runtime_error | rca_low_confidence | ...
	PublishTier   string  // public_verified | economic_incomplete_rca | internal_only | no_publish
	RerunDecision string  // no_rerun | auto_rerun | guided_repair | manual_review
	RerunReason   string  // short reason code for the decision
}

const (
	StateDone   = "done"
	StateFailed = "failed"

	OutcomeVerified    = "verified"
	OutcomePartial     = "partial"
	OutcomeUnverified  = "unverified"
	OutcomeEngineError = "engine_error"

	AnalysisStageSuccess      = "success"
	AnalysisStageReachablePoC = "reachable_poc"
	AnalysisStagePoCMissing   = "poc_missing"
	AnalysisStagePoCFailed    = "poc_failed"
	AnalysisStagePoCBlocked   = "poc_blocked"
	AnalysisStageRCABlocked   = "rca_blocked"
	AnalysisStagePartial      = "partial"
	AnalysisStageEngineError  = "engine_error"
	AnalysisStageUnknown      = "unknown"

	PoCStateEconomic  = "economic_poc"
	PoCStateReachable = "reachable_poc"
	PoCStateMissing   = "poc_missing"
	PoCStateFailed    = "poc_failed"
	PoCStateUnknown   = "poc_unknown"

	RCAStateComplete            = "rca_complete"
	RCAStateNotRun              = "rca_not_run"
	RCAStateRuntimeError        = "rca_runtime_error"
	RCAStateLowConfidence       = "rca_low_confidence"
	RCAStateMissingEvidence     = "rca_missing_evidence"
	RCAStateNoPatchableEvidence = "rca_no_patchable_root_cause_evidence"
	RCAStatePoCDependent        = "rca_poc_dependent"
	RCAStateScopeLimited        = "rca_scope_limited"
	RCAStateConflictingEvidence = "rca_conflicting_evidence"
	RCAStateUnknown             = "rca_unknown"

	PublishTierPublicVerified        = "public_verified"
	PublishTierEconomicIncompleteRCA = "economic_incomplete_rca"
	PublishTierInternalOnly          = "internal_only"
	PublishTierNoPublish             = "no_publish"

	RerunDecisionNoRerun      = "no_rerun"
	RerunDecisionAutoRerun    = "auto_rerun"
	RerunDecisionGuidedRepair = "guided_repair"
	RerunDecisionManualReview = "manual_review"

	FailureLumoskitNonzeroExit         = "lumoskit_nonzero_exit"
	FailureSummaryMissing              = "summary_missing"
	FailureSummaryUnreadable           = "summary_unreadable"
	FailureLumoskitReportedEngineError = "lumoskit_reported_engine_error"
	FailureLumoskitUnexpectedSummary   = "lumoskit_unexpected_summary_shape"
)

// Summary mirrors the subset of lumoskit's run summary that helios reads.
// Per ADR-0018 the engine always writes this file, including failure paths.
type Summary struct {
	Status  string         `json:"status"` // pass | partial | fail
	PoC     SummaryPoC     `json:"poc"`
	RCA     SummaryRCA     `json:"rca"`
	Failure SummaryFailure `json:"failure"`
}

type SummaryPoC struct {
	Status           string `json:"status"` // verified | unverified | missing
	ExecutionState   string `json:"execution_state"`
	ProofKind        string `json:"proof_kind"`
	ForgeBuildStatus string `json:"forge_build_status"`
	ForgeTestStatus  string `json:"forge_test_status"`
	FailureKind      string `json:"failure_kind"`
}

type SummaryRCA struct {
	Status            string `json:"status"`
	AnalysisStatus    string `json:"analysis_status"`
	RootCauseMode     string `json:"root_cause_mode"`
	ProofBoundaryKind string `json:"proof_boundary_kind"`
	BlockerCode       string `json:"blocker_code"`
	BlockerReason     string `json:"blocker_reason"`
}

// SummaryFailure is "set" when Kind != "" (Go zero-value detection).
type SummaryFailure struct {
	Kind       string `json:"kind"`
	Category   string `json:"category"`
	DetailKind string `json:"detail_kind"`
	Stage      string `json:"stage"`
	Message    string `json:"message"`
}

// Input bundles what the lumoskit runner observed after the child process
// exited. summaryBytes/summaryReadErr capture how the run summary read went
// without coupling the mapping rules to the filesystem.
type Input struct {
	ExitCode       int
	SummaryBytes   []byte // raw bytes; nil when SummaryReadErr != nil
	SummaryMissing bool   // os.IsNotExist(err) on the expected path
	SummaryReadErr error  // any non-nil read error other than missing
	Stderr         []byte // captured lumoskit stderr tail; classified into a finer engine-error kind
}

// Map applies rules O5..O8 in the seed's required precedence.
//
//	O5: exit != 0                                       → failed/engine_error/lumoskit_nonzero_exit
//	O6: run summary missing                            → failed/engine_error/summary_missing
//	O7: run summary unparseable                        → failed/engine_error/summary_unreadable
//	O4: summary parseable + failure.kind == "engine_error"
//	                                                    → failed/engine_error/lumoskit_reported_engine_error
//	O1: status=pass + poc=verified                      → done/verified
//	O2: status=partial OR RCA-blocked analysis          → done/partial
//	O3: terminal summary + failed/missing PoC           → done/unverified
//	O8: catch-all (parseable summary, none of above)    → failed/engine_error/lumoskit_unexpected_summary_shape
//
// NOTE on the divergence from seeds/v1.yaml: the seed assumed `failure` would
// be absent on success/partial/unverified runs, but real lumoskit always sets
// `failure` whenever status != "pass" (with kind="poc_missing",
// "poc_unverified", "custom_error_replay_gap", etc.). Treating any non-empty
// failure as engine_error misclassified valid partial/unverified outcomes,
// so O4 now requires the specific kind="engine_error" sentinel that lumoskit
// emits for genuine pipeline failures.
func Map(in Input) Result {
	// O5 — refine the opaque nonzero exit into a finer engine-error kind when
	// stderr/summary make the cause clear (tx_not_found, rpc_unavailable, ...).
	if in.ExitCode != 0 {
		// Prefer lumoskit's structured summary.failure (category=engine_error with
		// a specific kind) as the high-confidence signal; fall back to the
		// stderr-tail heuristic, then to the opaque lumoskit_nonzero_exit.
		if failure, ok := specificEngineFailureFromSummary(in); ok {
			return engineErrorFromSummary("O5", failure)
		}
		if kind := classifyEngineError(in); kind != "" {
			return engineError("O5", kind)
		}
		return engineError("O5", FailureLumoskitNonzeroExit)
	}
	// O6
	if in.SummaryMissing {
		return engineError("O6", FailureSummaryMissing)
	}
	// O7
	if in.SummaryReadErr != nil {
		return engineError("O7", FailureSummaryUnreadable)
	}
	var s Summary
	if err := json.Unmarshal(in.SummaryBytes, &s); err != nil {
		return engineError("O7", FailureSummaryUnreadable)
	}
	// O4 — only explicit engine-error markers count as pipeline failures.
	// Specific failures can set category=engine_error while keeping a precise
	// kind such as rca_agent_runtime_error for operator-facing diagnosis.
	if isEngineFailure(s.Failure) {
		return engineErrorFromSummary("O4", s.Failure)
	}
	// O1, O2, O3
	switch {
	case s.Status == "pass" && s.PoC.Status == "verified":
		return withRerunDecision(Result{State: StateDone, Outcome: OutcomeVerified, Rule: "O1"}, s)
	case isTerminalUnverifiedPoC(s):
		return withRerunDecision(Result{State: StateDone, Outcome: OutcomeUnverified, Rule: "O3"}, s)
	case s.Status == "partial" || isBlockedAnalysis(s):
		return withRerunDecision(Result{State: StateDone, Outcome: OutcomePartial, Rule: "O2"}, s)
	case s.Status == "fail" && (s.PoC.Status == "unverified" || s.PoC.Status == "missing"):
		return withRerunDecision(Result{State: StateDone, Outcome: OutcomeUnverified, Rule: "O3"}, s)
	}
	// O8
	return engineError("O8", FailureLumoskitUnexpectedSummary)
}

func specificEngineFailureFromSummary(in Input) (SummaryFailure, bool) {
	if len(in.SummaryBytes) == 0 || in.SummaryReadErr != nil || in.SummaryMissing {
		return SummaryFailure{}, false
	}
	var s Summary
	if err := json.Unmarshal(in.SummaryBytes, &s); err != nil {
		return SummaryFailure{}, false
	}
	if s.Failure.Category != "engine_error" || s.Failure.Kind == "" || s.Failure.Kind == "engine_error" {
		return SummaryFailure{}, false
	}
	return s.Failure, true
}

func withRerunDecision(result Result, s Summary) Result {
	result.PoCState = classifyPoCState(s.PoC)
	result.RCAState = classifyRCAState(s)
	result.PublishTier = classifyPublishTier(result.PoCState, result.RCAState)
	switch result.Outcome {
	case OutcomeVerified:
		if isRCAIncompleteState(result.RCAState) {
			result.AnalysisStage = AnalysisStageRCABlocked
			result.RerunDecision = rerunDecisionForRCAState(result.RCAState)
			result.RerunReason = rcaIncompleteReason(result.RCAState, s)
		} else {
			result.AnalysisStage = AnalysisStageSuccess
			result.RerunDecision = RerunDecisionNoRerun
			result.RerunReason = "verified_result"
		}
	case OutcomePartial:
		if result.PoCState == PoCStateMissing {
			result.RerunReason = firstNonEmpty(s.PoC.FailureKind, s.Failure.Kind, "poc_missing")
			if isRecoverablePoCFailure(result.RerunReason) {
				result.AnalysisStage = AnalysisStagePoCBlocked
				result.RerunDecision = RerunDecisionAutoRerun
			} else {
				result.AnalysisStage = AnalysisStagePoCMissing
				result.RerunDecision = RerunDecisionManualReview
			}
		} else if result.PoCState == PoCStateReachable {
			result.AnalysisStage = AnalysisStageReachablePoC
			result.RerunDecision = RerunDecisionGuidedRepair
			result.RerunReason = firstNonEmpty(s.PoC.FailureKind, s.Failure.Kind, s.PoC.ProofKind, "economic_proof_missing")
		} else if result.PoCState != PoCStateEconomic {
			result.RerunReason = firstNonEmpty(s.PoC.FailureKind, s.Failure.Kind, s.PoC.Status, "poc_failed")
			if isRecoverablePoCFailure(result.RerunReason) {
				result.AnalysisStage = AnalysisStagePoCBlocked
				result.RerunDecision = RerunDecisionAutoRerun
			} else {
				result.AnalysisStage = AnalysisStagePoCFailed
				result.RerunDecision = RerunDecisionManualReview
			}
		} else if isRCAIncompleteState(result.RCAState) {
			result.AnalysisStage = AnalysisStageRCABlocked
			result.RerunDecision = rerunDecisionForRCAState(result.RCAState)
			result.RerunReason = rcaIncompleteReason(result.RCAState, s)
		} else {
			result.AnalysisStage = AnalysisStagePartial
			result.RerunDecision = RerunDecisionNoRerun
			result.RerunReason = firstNonEmpty(s.Failure.Kind, "partial_result")
		}
	case OutcomeUnverified:
		if result.PoCState == PoCStateMissing {
			result.RerunReason = firstNonEmpty(s.PoC.FailureKind, s.Failure.Kind, "poc_missing")
			if isRecoverablePoCFailure(result.RerunReason) {
				result.AnalysisStage = AnalysisStagePoCBlocked
				result.RerunDecision = RerunDecisionAutoRerun
			} else {
				result.AnalysisStage = AnalysisStagePoCMissing
				result.RerunDecision = RerunDecisionManualReview
			}
		} else {
			result.RerunReason = firstNonEmpty(s.PoC.FailureKind, s.Failure.Kind, s.PoC.Status, "poc_failed")
			if isRecoverablePoCFailure(result.RerunReason) {
				result.AnalysisStage = AnalysisStagePoCBlocked
				result.RerunDecision = RerunDecisionAutoRerun
			} else {
				result.AnalysisStage = AnalysisStagePoCFailed
				result.RerunDecision = RerunDecisionManualReview
			}
		}
	default:
		result.AnalysisStage = AnalysisStageUnknown
		result.RerunDecision = RerunDecisionManualReview
		result.RerunReason = "unknown_outcome"
	}
	return result
}

func rcaIncompleteReason(state string, s Summary) string {
	if state == RCAStateNoPatchableEvidence {
		return firstNonEmpty(s.RCA.ProofBoundaryKind, s.RCA.BlockerCode, s.RCA.BlockerReason, state)
	}
	if reason := firstNonEmpty(s.RCA.BlockerCode, s.RCA.BlockerReason); reason != "" {
		return reason
	}
	if reason := nonGenericRCAReason(s.RCA.Status); reason != "" {
		return reason
	}
	if reason := nonGenericRCAReason(s.RCA.AnalysisStatus); reason != "" {
		return reason
	}
	if s.Failure.Kind != "" {
		return s.Failure.Kind
	}
	return firstNonEmpty(state, s.RCA.Status, s.RCA.AnalysisStatus, "rca_blocked")
}

func nonGenericRCAReason(reason string) string {
	switch strings.TrimSpace(strings.ToLower(reason)) {
	case "", "partial", "blocked":
		return ""
	default:
		return reason
	}
}

func classifyPoCState(p SummaryPoC) string {
	switch {
	case p.ExecutionState == PoCStateEconomic:
		return PoCStateEconomic
	case p.ExecutionState == PoCStateReachable:
		return PoCStateReachable
	case p.ExecutionState == "no_working_poc" && p.Status == "missing":
		return PoCStateMissing
	case p.ExecutionState == "no_working_poc":
		return PoCStateFailed
	case p.Status == "verified":
		return PoCStateEconomic
	case p.Status == "missing":
		return PoCStateMissing
	case isReachablePoC(p):
		return PoCStateReachable
	case p.Status == "unverified" || p.Status == "failed" || p.Status == "invalid" || p.FailureKind != "":
		return PoCStateFailed
	default:
		return PoCStateUnknown
	}
}

func isReachablePoC(p SummaryPoC) bool {
	if p.ExecutionState != "" {
		return false
	}
	return p.Status == "unverified" &&
		p.ForgeBuildStatus == "pass" &&
		p.ForgeTestStatus == "pass" &&
		p.ProofKind != "" &&
		p.ProofKind != "economic_proof"
}

func classifyRCAState(s Summary) string {
	r := s.RCA
	status := strings.TrimSpace(strings.ToLower(firstNonEmpty(r.AnalysisStatus, r.Status)))
	rootCauseMode := strings.TrimSpace(strings.ToLower(r.RootCauseMode))
	proofBoundaryKind := strings.TrimSpace(strings.ToLower(r.ProofBoundaryKind))
	text := strings.ToLower(strings.Join([]string{
		status,
		rootCauseMode,
		proofBoundaryKind,
		r.BlockerCode,
		r.BlockerReason,
		s.Failure.Kind,
		s.Failure.DetailKind,
		s.Failure.Stage,
		s.Failure.Message,
	}, " "))

	switch status {
	case "complete", "pass":
		return RCAStateComplete
	case "", "not_run", "missing":
		return RCAStateNotRun
	}
	if proofBoundaryKind == "no_patchable_root_cause_evidence" ||
		(rootCauseMode == "unknown" && containsAny(text, "no_patchable_root_cause_evidence", "no source-backed patchable root cause", "do not identify a patchable", "do not prove a patchable", "patchable vulnerable branch", "invariant-breaking branch")) {
		return RCAStateNoPatchableEvidence
	}
	if containsAny(text, "runtime", "timeout", "rate_limit", "rate limited", "malformed", "sdk", "output_missing") {
		return RCAStateRuntimeError
	}
	if classifyPoCState(s.PoC) != PoCStateEconomic && containsAny(text, "poc", "proof", "economic") {
		return RCAStatePoCDependent
	}
	if containsAny(text, "conflict", "conflicting", "contradict", "inconsistent") {
		return RCAStateConflictingEvidence
	}
	if containsAny(text, "confidence", "low_confidence", "below_threshold", "uncertain", "root_cause_gap") {
		return RCAStateLowConfidence
	}
	if (status == "partial" || status == "blocked") &&
		(rootCauseMode == "direct_asset_loss_logic" || rootCauseMode == "loss_enabling_state_change") {
		return RCAStateScopeLimited
	}
	if containsAny(text, "missing", "insufficient", "source", "abi", "trace", "storage", "delta", "provenance", "evidence", "context") {
		return RCAStateMissingEvidence
	}
	if containsAny(text, "scope", "limited", "symptom", "flow", "incomplete", "not_full") {
		return RCAStateScopeLimited
	}
	if status == "partial" || status == "blocked" {
		return RCAStateScopeLimited
	}
	return RCAStateUnknown
}

func classifyPublishTier(pocState, rcaState string) string {
	switch {
	case pocState == PoCStateEconomic && rcaState == RCAStateComplete:
		return PublishTierPublicVerified
	case pocState == PoCStateEconomic && (rcaState == RCAStateLowConfidence || rcaState == RCAStateMissingEvidence || rcaState == RCAStateNoPatchableEvidence || rcaState == RCAStateScopeLimited):
		return PublishTierEconomicIncompleteRCA
	case (pocState == PoCStateMissing || pocState == PoCStateFailed) && rcaState == RCAStateComplete:
		return PublishTierInternalOnly
	default:
		return PublishTierNoPublish
	}
}

func isRCAIncompleteState(state string) bool {
	switch state {
	case RCAStateNotRun, RCAStateRuntimeError, RCAStateLowConfidence, RCAStateMissingEvidence, RCAStateNoPatchableEvidence, RCAStatePoCDependent, RCAStateScopeLimited, RCAStateConflictingEvidence:
		return true
	default:
		return false
	}
}

func rerunDecisionForRCAState(state string) string {
	switch state {
	case RCAStateNotRun, RCAStateRuntimeError:
		return RerunDecisionAutoRerun
	case RCAStateConflictingEvidence:
		return RerunDecisionManualReview
	default:
		return RerunDecisionNoRerun
	}
}

func isRecoverablePoCFailure(reason string) bool {
	switch strings.TrimSpace(strings.ToLower(reason)) {
	case "static_validation_failed",
		"forge_fmt_failed",
		"forge_build_failed",
		"forge_test_failed",
		"abi_selector_compatibility_failed",
		"protocol_revert_with_oracle_gap":
		return true
	default:
		return false
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func isPoCBlocked(p SummaryPoC) bool {
	switch p.Status {
	case "unverified", "missing":
		return true
	case "verified":
		return false
	}
	return p.FailureKind != ""
}

func isRCABlocked(r SummaryRCA) bool {
	return r.Status == "blocked" || r.BlockerCode != "" || r.BlockerReason != ""
}

func isBlockedAnalysis(s Summary) bool {
	pocState := classifyPoCState(s.PoC)
	return s.Status == "blocked" &&
		pocState != PoCStateMissing &&
		pocState != PoCStateFailed &&
		isRCAIncompleteState(classifyRCAState(s))
}

func isTerminalUnverifiedPoC(s Summary) bool {
	switch strings.TrimSpace(strings.ToLower(s.Status)) {
	case "fail", "failed":
		return s.PoC.Status == "unverified" || s.PoC.Status == "missing" || classifyPoCState(s.PoC) == PoCStateFailed
	case "blocked":
		pocState := classifyPoCState(s.PoC)
		return pocState == PoCStateMissing || pocState == PoCStateFailed
	case "partial":
		return s.PoC.ExecutionState == "no_working_poc" ||
			s.PoC.Status == "missing" ||
			strings.TrimSpace(strings.ToLower(s.PoC.ForgeTestStatus)) == "fail" ||
			strings.TrimSpace(strings.ToLower(s.PoC.ForgeTestStatus)) == "failed"
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func isEngineFailure(f SummaryFailure) bool {
	return f.Kind == "engine_error" || f.Category == "engine_error"
}

func engineErrorFromSummary(rule string, f SummaryFailure) Result {
	kind := FailureLumoskitReportedEngineError
	if f.Kind != "" && f.Kind != "engine_error" {
		kind = f.Kind
	}
	result := engineError(rule, kind)
	if f.DetailKind != "" {
		result.RerunReason = f.DetailKind
	}
	return result
}

// TerminalEventPayload builds the diagnostic payload stored on terminal
// state_transition events. The state machine criteria stay in Map; this
// function only copies the small operator-facing subset that explains which
// outcome rule fired and where PoC/RCA blocked.
func TerminalEventPayload(result Result, in Input) map[string]any {
	payload := map[string]any{
		"outcome": result.Outcome,
	}
	if result.Rule != "" {
		payload["rule"] = result.Rule
	}
	if result.FailureKind != nil && *result.FailureKind != "" {
		payload["failure_kind"] = *result.FailureKind
	}
	if result.AnalysisStage != "" {
		payload["analysis_stage"] = result.AnalysisStage
	}
	if result.PoCState != "" {
		payload["poc_state"] = result.PoCState
	}
	if result.RCAState != "" {
		payload["rca_state"] = result.RCAState
	}
	if result.PublishTier != "" {
		payload["publish_tier"] = result.PublishTier
		payload["github_publish_eligible"] = ShouldPublishGitHub(result)
		payload["x_publish_eligible"] = ShouldPublishX(result)
	}
	if result.RerunDecision != "" {
		payload["rerun_decision"] = result.RerunDecision
	}
	if result.RerunReason != "" {
		payload["rerun_reason"] = result.RerunReason
	}

	// Engine-error diagnosis + redacted stderr tail so an operator can decide
	// the next action from GET /cases/{id} (and Telegram) without SSH/journal.
	if result.Outcome == OutcomeEngineError && result.FailureKind != nil && *result.FailureKind != "" {
		payload["engine_error_kind"] = *result.FailureKind
		if d, ok := DiagnosisFor(*result.FailureKind); ok {
			payload["diagnosis"] = map[string]any{
				"category":           d.Category,
				"owner":              d.Owner,
				"retryable":          d.Retryable,
				"recommended_action": d.Action,
			}
		}
		if tail := redactStderr(in.Stderr); tail != "" {
			payload["stderr_tail"] = tail
		}
	}

	s, ok := parseSummaryForPayload(in)
	if !ok {
		return payload
	}
	if s.Status != "" {
		payload["summary_status"] = s.Status
	}
	pocState := result.PoCState
	if pocState == "" {
		pocState = classifyPoCState(s.PoC)
		if pocState != "" {
			payload["poc_state"] = pocState
		}
	}
	rcaState := result.RCAState
	if rcaState == "" {
		rcaState = classifyRCAState(s)
		if rcaState != "" {
			payload["rca_state"] = rcaState
		}
	}
	if tier := firstNonEmpty(result.PublishTier, classifyPublishTier(pocState, rcaState)); tier != "" {
		payload["publish_tier"] = tier
		eligibility := result
		eligibility.PublishTier = tier
		eligibility.PoCState = pocState
		eligibility.RCAState = rcaState
		payload["github_publish_eligible"] = ShouldPublishGitHub(eligibility)
		payload["x_publish_eligible"] = ShouldPublishX(eligibility)
	}
	if poc := s.PoC.eventPayload(pocState); len(poc) > 0 {
		payload["poc"] = poc
	}
	if rca := s.RCA.eventPayload(rcaState); len(rca) > 0 {
		payload["rca"] = rca
	}
	if failure := s.Failure.eventPayload(); len(failure) > 0 {
		payload["failure"] = failure
	}
	return payload
}

func parseSummaryForPayload(in Input) (Summary, bool) {
	if in.SummaryMissing || in.SummaryReadErr != nil || len(in.SummaryBytes) == 0 {
		return Summary{}, false
	}
	var s Summary
	if err := json.Unmarshal(in.SummaryBytes, &s); err != nil {
		return Summary{}, false
	}
	return s, true
}

func (p SummaryPoC) eventPayload(state string) map[string]string {
	out := map[string]string{}
	if state != "" {
		out["state"] = state
	}
	if p.Status != "" {
		out["status"] = p.Status
	}
	if p.ExecutionState != "" {
		out["execution_state"] = p.ExecutionState
	}
	if p.ProofKind != "" {
		out["proof_kind"] = p.ProofKind
	}
	if p.ForgeBuildStatus != "" {
		out["forge_build_status"] = p.ForgeBuildStatus
	}
	if p.ForgeTestStatus != "" {
		out["forge_test_status"] = p.ForgeTestStatus
	}
	if p.FailureKind != "" {
		out["failure_kind"] = p.FailureKind
	}
	return out
}

func (r SummaryRCA) eventPayload(state string) map[string]string {
	out := map[string]string{}
	if state != "" {
		out["state"] = state
	}
	if r.Status != "" {
		out["status"] = r.Status
	}
	if r.AnalysisStatus != "" {
		out["analysis_status"] = r.AnalysisStatus
	}
	if r.RootCauseMode != "" {
		out["root_cause_mode"] = r.RootCauseMode
	}
	if r.ProofBoundaryKind != "" {
		out["proof_boundary_kind"] = r.ProofBoundaryKind
	}
	if r.BlockerCode != "" {
		out["blocker_code"] = r.BlockerCode
	}
	if r.BlockerReason != "" {
		out["blocker_reason"] = r.BlockerReason
	}
	return out
}

func (f SummaryFailure) eventPayload() map[string]string {
	out := map[string]string{}
	if f.Kind != "" {
		out["kind"] = f.Kind
	}
	if f.Category != "" {
		out["category"] = f.Category
	}
	if f.DetailKind != "" {
		out["detail_kind"] = f.DetailKind
	}
	if f.Stage != "" {
		out["stage"] = f.Stage
	}
	if f.Message != "" {
		out["message"] = f.Message
	}
	return out
}

func ShouldPublishGitHub(result Result) bool {
	return result.PublishTier == PublishTierPublicVerified || result.PublishTier == PublishTierEconomicIncompleteRCA
}

func ShouldPublishX(result Result) bool {
	return result.PublishTier == PublishTierPublicVerified ||
		(result.PublishTier == PublishTierEconomicIncompleteRCA && result.RCAState == RCAStateScopeLimited)
}

func engineError(rule, kind string) Result {
	k := kind
	return Result{
		State:         StateFailed,
		Outcome:       OutcomeEngineError,
		FailureKind:   &k,
		Rule:          rule,
		AnalysisStage: AnalysisStageEngineError,
		RerunDecision: RerunDecisionManualReview,
		RerunReason:   kind,
	}
}
