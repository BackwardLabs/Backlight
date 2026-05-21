// Package outcome maps a lumoskit child-process exit + its summary.json into
// the (state, outcome, failure_kind) triple defined by seeds/v1.yaml.
//
// The seed names eight rules (O1..O8). They MUST be evaluated in this strict
// order so the first matching rule wins:
//
//	O5 → O6 → O7 → O4 → O1 → O2 → O3 → O8
package outcome

import (
	"encoding/json"
)

// Result is what the worker writes back onto the case row after lumoskit exits.
type Result struct {
	State       string  // queued | running | done | handed-off | failed
	Outcome     string  // verified | partial | unverified | engine_error
	FailureKind *string // populated iff Outcome == engine_error
	Rule        string  // O1..O8 — the rule that fired (debug/audit)
}

const (
	StateDone   = "done"
	StateFailed = "failed"

	OutcomeVerified    = "verified"
	OutcomePartial     = "partial"
	OutcomeUnverified  = "unverified"
	OutcomeEngineError = "engine_error"

	FailureLumoskitNonzeroExit         = "lumoskit_nonzero_exit"
	FailureSummaryMissing              = "summary_missing"
	FailureSummaryUnreadable           = "summary_unreadable"
	FailureLumoskitReportedEngineError = "lumoskit_reported_engine_error"
	FailureLumoskitUnexpectedSummary   = "lumoskit_unexpected_summary_shape"
)

// Summary mirrors the subset of lumoskit's summary.json that helios reads.
// Per ADR-0018 the engine always writes this file, including failure paths.
type Summary struct {
	Status  string         `json:"status"` // pass | partial | fail
	PoC     SummaryPoC     `json:"poc"`
	RCA     SummaryRCA     `json:"rca"`
	Failure SummaryFailure `json:"failure"`
}

type SummaryPoC struct {
	Status           string `json:"status"` // verified | unverified | missing
	ProofKind        string `json:"proof_kind"`
	ForgeBuildStatus string `json:"forge_build_status"`
	ForgeTestStatus  string `json:"forge_test_status"`
	FailureKind      string `json:"failure_kind"`
}

type SummaryRCA struct {
	Status        string `json:"status"`
	BlockerCode   string `json:"blocker_code"`
	BlockerReason string `json:"blocker_reason"`
}

// SummaryFailure is "set" when Kind != "" (Go zero-value detection).
type SummaryFailure struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// Input bundles what the lumoskit runner observed after the child process
// exited. summaryBytes/summaryReadErr capture how the summary.json read went
// without coupling the mapping rules to the filesystem.
type Input struct {
	ExitCode       int
	SummaryBytes   []byte // raw bytes; nil when SummaryReadErr != nil
	SummaryMissing bool   // os.IsNotExist(err) on the expected path
	SummaryReadErr error  // any non-nil read error other than missing
}

// Map applies rules O5..O8 in the seed's required precedence.
//
//	O5: exit != 0                                       → failed/engine_error/lumoskit_nonzero_exit
//	O6: summary.json missing                            → failed/engine_error/summary_missing
//	O7: summary.json unparseable                        → failed/engine_error/summary_unreadable
//	O4: summary parseable + failure.kind == "engine_error"
//	                                                    → failed/engine_error/lumoskit_reported_engine_error
//	O1: status=pass + poc=verified                      → done/verified
//	O2: status=partial                                  → done/partial
//	O3: status=fail + poc∈{unverified,missing}          → done/unverified
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
	// O5
	if in.ExitCode != 0 {
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
	// O4 — only the explicit engine_error sentinel counts as a pipeline failure.
	if s.Failure.Kind == "engine_error" {
		return engineError("O4", FailureLumoskitReportedEngineError)
	}
	// O1, O2, O3
	switch {
	case s.Status == "pass" && s.PoC.Status == "verified":
		return Result{State: StateDone, Outcome: OutcomeVerified, Rule: "O1"}
	case s.Status == "partial":
		return Result{State: StateDone, Outcome: OutcomePartial, Rule: "O2"}
	case s.Status == "fail" && (s.PoC.Status == "unverified" || s.PoC.Status == "missing"):
		return Result{State: StateDone, Outcome: OutcomeUnverified, Rule: "O3"}
	}
	// O8
	return engineError("O8", FailureLumoskitUnexpectedSummary)
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

	s, ok := parseSummaryForPayload(in)
	if !ok {
		return payload
	}
	if s.Status != "" {
		payload["summary_status"] = s.Status
	}
	if poc := s.PoC.eventPayload(); len(poc) > 0 {
		payload["poc"] = poc
	}
	if rca := s.RCA.eventPayload(); len(rca) > 0 {
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

func (p SummaryPoC) eventPayload() map[string]string {
	out := map[string]string{}
	if p.Status != "" {
		out["status"] = p.Status
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

func (r SummaryRCA) eventPayload() map[string]string {
	out := map[string]string{}
	if r.Status != "" {
		out["status"] = r.Status
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
	if f.Message != "" {
		out["message"] = f.Message
	}
	return out
}

func engineError(rule, kind string) Result {
	k := kind
	return Result{
		State:       StateFailed,
		Outcome:     OutcomeEngineError,
		FailureKind: &k,
		Rule:        rule,
	}
}
