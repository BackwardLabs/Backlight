package outcome

import (
	"errors"
	"testing"
)

func TestMap_PrecedenceTable(t *testing.T) {
	// One row per rule; the table also exercises precedence by ordering
	// inputs that match multiple rules and asserting the seed-required winner.
	cases := []struct {
		name            string
		in              Input
		wantRule        string
		wantState       string
		wantOutcome     string
		wantFailureKind string // "" when nil
	}{
		// O5 — nonzero exit ignores summary contents
		{
			name:            "O5 nonzero exit ignores summary",
			in:              Input{ExitCode: 1, SummaryBytes: []byte(`{"status":"pass","poc":{"status":"verified"}}`)},
			wantRule:        "O5",
			wantState:       StateFailed,
			wantOutcome:     OutcomeEngineError,
			wantFailureKind: FailureLumoskitNonzeroExit,
		},
		// O6 — summary missing
		{
			name:            "O6 summary missing",
			in:              Input{ExitCode: 0, SummaryMissing: true},
			wantRule:        "O6",
			wantState:       StateFailed,
			wantOutcome:     OutcomeEngineError,
			wantFailureKind: FailureSummaryMissing,
		},
		// O7 — read error
		{
			name:            "O7 summary read error",
			in:              Input{ExitCode: 0, SummaryReadErr: errors.New("io: stat failed")},
			wantRule:        "O7",
			wantState:       StateFailed,
			wantOutcome:     OutcomeEngineError,
			wantFailureKind: FailureSummaryUnreadable,
		},
		// O7 — invalid JSON
		{
			name:            "O7 unparseable JSON",
			in:              Input{ExitCode: 0, SummaryBytes: []byte("not json{")},
			wantRule:        "O7",
			wantState:       StateFailed,
			wantOutcome:     OutcomeEngineError,
			wantFailureKind: FailureSummaryUnreadable,
		},
		// O4 — reported failure (engine wrote summary then flagged engine_error sentinel).
		// Real lumoskit only sets failure.kind="engine_error" when the pipeline itself
		// failed; other failure.kind values (poc_missing, custom_error_replay_gap, ...)
		// accompany normal partial/unverified outcomes and must NOT route through O4.
		{
			name:            "O4 reported engine_error",
			in:              Input{ExitCode: 0, SummaryBytes: []byte(`{"status":"fail","poc":{"status":"missing"},"failure":{"kind":"engine_error"}}`)},
			wantRule:        "O4",
			wantState:       StateFailed,
			wantOutcome:     OutcomeEngineError,
			wantFailureKind: FailureLumoskitReportedEngineError,
		},
		// Real lumoskit emits failure.kind=poc_missing on partial/unverified runs;
		// helios must NOT route those through O4 — they belong to O2 / O3.
		{
			name:        "O2 partial with non-engine failure kind (real lumoskit)",
			in:          Input{ExitCode: 0, SummaryBytes: []byte(`{"status":"partial","poc":{"status":"unverified"},"failure":{"kind":"custom_error_replay_gap"}}`)},
			wantRule:    "O2",
			wantState:   StateDone,
			wantOutcome: OutcomePartial,
		},
		{
			name:        "O3 fail with non-engine failure kind (real lumoskit)",
			in:          Input{ExitCode: 0, SummaryBytes: []byte(`{"status":"fail","poc":{"status":"missing"},"failure":{"kind":"poc_missing"}}`)},
			wantRule:    "O3",
			wantState:   StateDone,
			wantOutcome: OutcomeUnverified,
		},
		// O1 — verified
		{
			name:        "O1 verified",
			in:          Input{ExitCode: 0, SummaryBytes: []byte(`{"status":"pass","poc":{"status":"verified"}}`)},
			wantRule:    "O1",
			wantState:   StateDone,
			wantOutcome: OutcomeVerified,
		},
		// O2 — partial (failure absent)
		{
			name:        "O2 partial",
			in:          Input{ExitCode: 0, SummaryBytes: []byte(`{"status":"partial","poc":{"status":"unverified"}}`)},
			wantRule:    "O2",
			wantState:   StateDone,
			wantOutcome: OutcomePartial,
		},
		// O3 — fail + unverified
		{
			name:        "O3 fail-unverified",
			in:          Input{ExitCode: 0, SummaryBytes: []byte(`{"status":"fail","poc":{"status":"unverified"}}`)},
			wantRule:    "O3",
			wantState:   StateDone,
			wantOutcome: OutcomeUnverified,
		},
		// O3 — fail + missing
		{
			name:        "O3 fail-missing",
			in:          Input{ExitCode: 0, SummaryBytes: []byte(`{"status":"fail","poc":{"status":"missing"}}`)},
			wantRule:    "O3",
			wantState:   StateDone,
			wantOutcome: OutcomeUnverified,
		},
		// O8 — catch-all: unexpected status/poc combo, no failure
		{
			name:            "O8 unexpected shape",
			in:              Input{ExitCode: 0, SummaryBytes: []byte(`{"status":"weird","poc":{"status":"???"}}`)},
			wantRule:        "O8",
			wantState:       StateFailed,
			wantOutcome:     OutcomeEngineError,
			wantFailureKind: FailureLumoskitUnexpectedSummary,
		},
		// Precedence: O5 wins over a parseable-success-shaped summary.
		{
			name:            "precedence O5 > O1",
			in:              Input{ExitCode: 2, SummaryBytes: []byte(`{"status":"pass","poc":{"status":"verified"}}`)},
			wantRule:        "O5",
			wantState:       StateFailed,
			wantOutcome:     OutcomeEngineError,
			wantFailureKind: FailureLumoskitNonzeroExit,
		},
		// Precedence: O6 (missing) wins over read-error (impossible normally — Map prefers SummaryMissing first).
		{
			name:            "precedence O6 > O7",
			in:              Input{ExitCode: 0, SummaryMissing: true, SummaryReadErr: errors.New("won't be touched")},
			wantRule:        "O6",
			wantState:       StateFailed,
			wantOutcome:     OutcomeEngineError,
			wantFailureKind: FailureSummaryMissing,
		},
		// Precedence: O4 (engine_error sentinel) beats O1 even when the rest of the body says "pass".
		{
			name:            "precedence O4 > O1",
			in:              Input{ExitCode: 0, SummaryBytes: []byte(`{"status":"pass","poc":{"status":"verified"},"failure":{"kind":"engine_error"}}`)},
			wantRule:        "O4",
			wantState:       StateFailed,
			wantOutcome:     OutcomeEngineError,
			wantFailureKind: FailureLumoskitReportedEngineError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Map(tc.in)
			if got.Rule != tc.wantRule {
				t.Errorf("rule = %q, want %q", got.Rule, tc.wantRule)
			}
			if got.State != tc.wantState {
				t.Errorf("state = %q, want %q", got.State, tc.wantState)
			}
			if got.Outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", got.Outcome, tc.wantOutcome)
			}
			gotFK := ""
			if got.FailureKind != nil {
				gotFK = *got.FailureKind
			}
			if gotFK != tc.wantFailureKind {
				t.Errorf("failure_kind = %q, want %q", gotFK, tc.wantFailureKind)
			}
		})
	}
}

func TestMap_VerifiedAcceptsNonEngineFailureKind(t *testing.T) {
	// Real lumoskit may include a non-empty `failure` block even on a fully
	// verified run (defensive sanity write). Only failure.kind=="engine_error"
	// should demote a verified result; any other kind keeps O1.
	in := Input{ExitCode: 0, SummaryBytes: []byte(`{"status":"pass","poc":{"status":"verified"},"failure":{"kind":"poc_missing"}}`)}
	got := Map(in)
	if got.Rule != "O1" {
		t.Fatalf("expected O1 (non-engine failure kind tolerated), got rule=%s outcome=%s", got.Rule, got.Outcome)
	}
}
