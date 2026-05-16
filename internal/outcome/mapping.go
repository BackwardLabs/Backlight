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
	Failure SummaryFailure `json:"failure"`
}

type SummaryPoC struct {
	Status string `json:"status"` // verified | unverified | missing
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

func engineError(rule, kind string) Result {
	k := kind
	return Result{
		State:       StateFailed,
		Outcome:     OutcomeEngineError,
		FailureKind: &k,
		Rule:        rule,
	}
}
