package outcome

import (
	"encoding/json"
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
		// O5 — nonzero exit is generic unless lumoskit wrote a specific engine category.
		{
			name:            "O5 nonzero exit ignores summary",
			in:              Input{ExitCode: 1, SummaryBytes: []byte(`{"status":"pass","poc":{"status":"verified"}}`)},
			wantRule:        "O5",
			wantState:       StateFailed,
			wantOutcome:     OutcomeEngineError,
			wantFailureKind: FailureLumoskitNonzeroExit,
		},
		{
			name:            "O5 nonzero exit preserves specific engine category",
			in:              Input{ExitCode: 1, SummaryBytes: []byte(`{"status":"blocked","poc":{"status":"failed"},"failure":{"kind":"agent_poc_agent_runtime_error","category":"engine_error","detail_kind":"codex_sdk_auth_error","stage":"agent_poc"}}`)},
			wantRule:        "O5",
			wantState:       StateFailed,
			wantOutcome:     OutcomeEngineError,
			wantFailureKind: "agent_poc_agent_runtime_error",
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
		{
			name:            "O4 reported specific engine_error category",
			in:              Input{ExitCode: 0, SummaryBytes: []byte(`{"status":"blocked","poc":{"status":"verified"},"rca":{"status":"blocked","blocker_code":"rca_agent_runtime_error"},"failure":{"kind":"rca_agent_runtime_error","category":"engine_error","detail_kind":"codex_sdk_auth_error"}}`)},
			wantRule:        "O4",
			wantState:       StateFailed,
			wantOutcome:     OutcomeEngineError,
			wantFailureKind: "rca_agent_runtime_error",
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
		{
			name:        "O3 blocked no-working-poc with failed forge test",
			in:          Input{ExitCode: 0, SummaryBytes: []byte(`{"status":"blocked","poc":{"status":"unverified","execution_state":"no_working_poc","forge_build_status":"pass","forge_test_status":"fail","failure_kind":"test_failed"},"rca":{"status":"blocked","analysis_status":"blocked","blocker_reason":"PoC execution did not pass"},"failure":{"kind":"test_failed"}}`)},
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
		// O2 — blocked summary with analysis blocker is a partial result.
		{
			name:        "O2 blocked RCA with verified PoC",
			in:          Input{ExitCode: 0, SummaryBytes: []byte(`{"status":"blocked","poc":{"status":"verified"},"rca":{"status":"blocked","blocker_code":"root_cause_gap"},"failure":{"kind":"rca_blocked"}}`)},
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

func TestMapClassifiesAnalysisStageAndRerunDecision(t *testing.T) {
	cases := []struct {
		name         string
		summary      string
		wantOutcome  string
		wantStage    string
		wantPoC      string
		wantRCA      string
		wantTier     string
		wantDecision string
		wantReason   string
		wantGitHub   bool
		wantX        bool
	}{
		{
			name:         "economic poc and complete rca publishes publicly",
			summary:      `{"status":"pass","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"complete"}}`,
			wantOutcome:  OutcomeVerified,
			wantStage:    AnalysisStageSuccess,
			wantPoC:      PoCStateEconomic,
			wantRCA:      RCAStateComplete,
			wantTier:     PublishTierPublicVerified,
			wantDecision: RerunDecisionNoRerun,
			wantReason:   "verified_result",
			wantGitHub:   true,
			wantX:        true,
		},
		{
			name:         "economic poc with rca not run auto reruns rca",
			summary:      `{"status":"partial","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"not_run"}}`,
			wantOutcome:  OutcomePartial,
			wantStage:    AnalysisStageRCABlocked,
			wantPoC:      PoCStateEconomic,
			wantRCA:      RCAStateNotRun,
			wantTier:     PublishTierNoPublish,
			wantDecision: RerunDecisionAutoRerun,
			wantReason:   "not_run",
		},
		{
			name:         "economic poc with rca runtime error auto reruns rca",
			summary:      `{"status":"partial","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"blocked","blocker_code":"rca_agent_timeout"}}`,
			wantOutcome:  OutcomePartial,
			wantStage:    AnalysisStageRCABlocked,
			wantPoC:      PoCStateEconomic,
			wantRCA:      RCAStateRuntimeError,
			wantTier:     PublishTierNoPublish,
			wantDecision: RerunDecisionAutoRerun,
			wantReason:   "rca_agent_timeout",
		},
		{
			name:         "economic poc with low confidence rca does not rerun but can publish github",
			summary:      `{"status":"partial","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"blocked","blocker_code":"root_cause_gap"}}`,
			wantOutcome:  OutcomePartial,
			wantStage:    AnalysisStageRCABlocked,
			wantPoC:      PoCStateEconomic,
			wantRCA:      RCAStateLowConfidence,
			wantTier:     PublishTierEconomicIncompleteRCA,
			wantDecision: RerunDecisionNoRerun,
			wantReason:   "root_cause_gap",
			wantGitHub:   true,
		},
		{
			name:         "economic poc with missing rca evidence does not rerun but can publish github",
			summary:      `{"status":"partial","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"blocked","blocker_code":"source_gap"}}`,
			wantOutcome:  OutcomePartial,
			wantStage:    AnalysisStageRCABlocked,
			wantPoC:      PoCStateEconomic,
			wantRCA:      RCAStateMissingEvidence,
			wantTier:     PublishTierEconomicIncompleteRCA,
			wantDecision: RerunDecisionNoRerun,
			wantReason:   "source_gap",
			wantGitHub:   true,
		},
		{
			name:         "economic poc with no patchable root cause evidence is proof boundary not rerun",
			summary:      `{"status":"partial","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"partial","analysis_status":"partial","root_cause_mode":"unknown","proof_boundary_kind":"no_patchable_root_cause_evidence","blocker_reason":"PoC profit is reproduced, but supplied trace/source/RPC artifacts do not identify a patchable contract/function/branch failed invariant."}}`,
			wantOutcome:  OutcomePartial,
			wantStage:    AnalysisStageRCABlocked,
			wantPoC:      PoCStateEconomic,
			wantRCA:      RCAStateNoPatchableEvidence,
			wantTier:     PublishTierEconomicIncompleteRCA,
			wantDecision: RerunDecisionNoRerun,
			wantReason:   "no_patchable_root_cause_evidence",
			wantGitHub:   true,
		},
		{
			name:         "economic poc with proof boundary phrase but no explicit proof kind",
			summary:      `{"status":"partial","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"partial","analysis_status":"partial","root_cause_mode":"unknown","blocker_reason":"Closed-world artifacts prove a profitable path but do not prove a source/pseudocode-backed invariant-breaking branch."}}`,
			wantOutcome:  OutcomePartial,
			wantStage:    AnalysisStageRCABlocked,
			wantPoC:      PoCStateEconomic,
			wantRCA:      RCAStateNoPatchableEvidence,
			wantTier:     PublishTierEconomicIncompleteRCA,
			wantDecision: RerunDecisionNoRerun,
			wantReason:   "Closed-world artifacts prove a profitable path but do not prove a source/pseudocode-backed invariant-breaking branch.",
			wantGitHub:   true,
		},
		{
			name:         "economic poc with scope limited rca can publish github and x",
			summary:      `{"status":"partial","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"partial","blocker_code":"scope_limited"}}`,
			wantOutcome:  OutcomePartial,
			wantStage:    AnalysisStageRCABlocked,
			wantPoC:      PoCStateEconomic,
			wantRCA:      RCAStateScopeLimited,
			wantTier:     PublishTierEconomicIncompleteRCA,
			wantDecision: RerunDecisionNoRerun,
			wantReason:   "scope_limited",
			wantGitHub:   true,
			wantX:        true,
		},
		{
			name:         "economic poc with generic partial rca reports classified rca state",
			summary:      `{"status":"partial","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"partial","analysis_status":"partial"}}`,
			wantOutcome:  OutcomePartial,
			wantStage:    AnalysisStageRCABlocked,
			wantPoC:      PoCStateEconomic,
			wantRCA:      RCAStateScopeLimited,
			wantTier:     PublishTierEconomicIncompleteRCA,
			wantDecision: RerunDecisionNoRerun,
			wantReason:   RCAStateScopeLimited,
			wantGitHub:   true,
			wantX:        true,
		},
		{
			name:         "economic poc with conflicting rca evidence requires manual review",
			summary:      `{"status":"partial","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"blocked","blocker_code":"conflicting_evidence"}}`,
			wantOutcome:  OutcomePartial,
			wantStage:    AnalysisStageRCABlocked,
			wantPoC:      PoCStateEconomic,
			wantRCA:      RCAStateConflictingEvidence,
			wantTier:     PublishTierNoPublish,
			wantDecision: RerunDecisionManualReview,
			wantReason:   "conflicting_evidence",
		},
		{
			name:         "reachable poc with complete rca queues guided repair but does not publish",
			summary:      `{"status":"partial","poc":{"status":"unverified","execution_state":"reachable_poc","proof_kind":"reachability_only","forge_build_status":"pass","forge_test_status":"pass","failure_kind":"missing_profit_or_economic_oracle"},"rca":{"status":"complete","analysis_status":"complete"}}`,
			wantOutcome:  OutcomePartial,
			wantStage:    AnalysisStageReachablePoC,
			wantPoC:      PoCStateReachable,
			wantRCA:      RCAStateComplete,
			wantTier:     PublishTierNoPublish,
			wantDecision: RerunDecisionGuidedRepair,
			wantReason:   "missing_profit_or_economic_oracle",
		},
		{
			name:         "poc missing is manual review and no publish",
			summary:      `{"status":"fail","poc":{"status":"missing","execution_state":"no_working_poc","failure_kind":"poc_missing"},"failure":{"kind":"poc_missing"}}`,
			wantOutcome:  OutcomeUnverified,
			wantStage:    AnalysisStagePoCMissing,
			wantPoC:      PoCStateMissing,
			wantRCA:      RCAStateNotRun,
			wantTier:     PublishTierNoPublish,
			wantDecision: RerunDecisionManualReview,
			wantReason:   "poc_missing",
		},
		{
			name:         "poc failed is manual review and no publish",
			summary:      `{"status":"partial","poc":{"status":"unverified","execution_state":"no_working_poc","failure_kind":"forge_test_failed"},"rca":{"status":"not_run"}}`,
			wantOutcome:  OutcomeUnverified,
			wantStage:    AnalysisStagePoCFailed,
			wantPoC:      PoCStateFailed,
			wantRCA:      RCAStateNotRun,
			wantTier:     PublishTierNoPublish,
			wantDecision: RerunDecisionManualReview,
			wantReason:   "forge_test_failed",
		},
		{
			name:         "engine error is manual review",
			summary:      `{"status":"fail","poc":{"status":"missing"},"failure":{"kind":"engine_error"}}`,
			wantOutcome:  OutcomeEngineError,
			wantStage:    AnalysisStageEngineError,
			wantDecision: RerunDecisionManualReview,
			wantReason:   FailureLumoskitReportedEngineError,
		},
		{
			name:         "specific engine error keeps detail reason",
			summary:      `{"status":"blocked","poc":{"status":"verified"},"rca":{"status":"blocked","blocker_code":"rca_agent_runtime_error"},"failure":{"kind":"rca_agent_runtime_error","category":"engine_error","detail_kind":"codex_sdk_auth_error"}}`,
			wantOutcome:  OutcomeEngineError,
			wantStage:    AnalysisStageEngineError,
			wantDecision: RerunDecisionManualReview,
			wantReason:   "codex_sdk_auth_error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Map(Input{ExitCode: 0, SummaryBytes: []byte(tc.summary)})
			if got.Outcome != tc.wantOutcome || got.AnalysisStage != tc.wantStage || got.RerunDecision != tc.wantDecision || got.RerunReason != tc.wantReason {
				t.Fatalf("got outcome/stage/decision/reason = %s/%s/%s/%s, want %s/%s/%s/%s", got.Outcome, got.AnalysisStage, got.RerunDecision, got.RerunReason, tc.wantOutcome, tc.wantStage, tc.wantDecision, tc.wantReason)
			}
			if tc.wantPoC != "" && got.PoCState != tc.wantPoC {
				t.Fatalf("poc_state = %s, want %s", got.PoCState, tc.wantPoC)
			}
			if tc.wantRCA != "" && got.RCAState != tc.wantRCA {
				t.Fatalf("rca_state = %s, want %s", got.RCAState, tc.wantRCA)
			}
			if tc.wantTier != "" && got.PublishTier != tc.wantTier {
				t.Fatalf("publish_tier = %s, want %s", got.PublishTier, tc.wantTier)
			}
			if ShouldPublishGitHub(got) != tc.wantGitHub {
				t.Fatalf("ShouldPublishGitHub = %v, want %v", ShouldPublishGitHub(got), tc.wantGitHub)
			}
			if ShouldPublishX(got) != tc.wantX {
				t.Fatalf("ShouldPublishX = %v, want %v", ShouldPublishX(got), tc.wantX)
			}
		})
	}
}

func TestTerminalEventPayloadIncludesPoCAndRCADiagnostics(t *testing.T) {
	in := Input{ExitCode: 0, SummaryBytes: []byte(`{
		"status":"partial",
		"failure":{
			"kind":"missing_profit_or_economic_oracle",
			"message":"economic proof could not be verified"
		},
		"poc":{
			"status":"unverified",
			"proof_kind":"reachability_only",
			"forge_build_status":"pass",
			"forge_test_status":"pass",
			"failure_kind":"missing_profit_or_economic_oracle"
		},
		"rca":{
			"status":"complete",
			"analysis_status":"complete"
		}
	}`)}

	mapped := Map(in)
	payload := TerminalEventPayload(mapped, in)

	var got map[string]any
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if got["outcome"] != OutcomePartial {
		t.Fatalf("outcome = %v, want %s", got["outcome"], OutcomePartial)
	}
	if got["rule"] != "O2" {
		t.Fatalf("rule = %v, want O2", got["rule"])
	}
	if got["summary_status"] != "partial" {
		t.Fatalf("summary_status = %v, want partial", got["summary_status"])
	}
	if got["analysis_stage"] != AnalysisStageReachablePoC {
		t.Fatalf("analysis_stage = %v, want %s", got["analysis_stage"], AnalysisStageReachablePoC)
	}
	if got["poc_state"] != PoCStateReachable {
		t.Fatalf("poc_state = %v, want %s", got["poc_state"], PoCStateReachable)
	}
	if got["rca_state"] != RCAStateComplete {
		t.Fatalf("rca_state = %v, want %s", got["rca_state"], RCAStateComplete)
	}
	if got["publish_tier"] != PublishTierNoPublish || got["github_publish_eligible"] != false || got["x_publish_eligible"] != false {
		t.Fatalf("publish payload = tier %v github %v x %v", got["publish_tier"], got["github_publish_eligible"], got["x_publish_eligible"])
	}
	if got["rerun_decision"] != RerunDecisionGuidedRepair {
		t.Fatalf("rerun_decision = %v, want %s", got["rerun_decision"], RerunDecisionGuidedRepair)
	}
	if got["rerun_reason"] != "missing_profit_or_economic_oracle" {
		t.Fatalf("rerun_reason = %v", got["rerun_reason"])
	}
	poc := got["poc"].(map[string]any)
	if poc["status"] != "unverified" || poc["proof_kind"] != "reachability_only" || poc["forge_test_status"] != "pass" {
		t.Fatalf("unexpected poc payload: %#v", poc)
	}
	rca := got["rca"].(map[string]any)
	if rca["status"] != "complete" || rca["analysis_status"] != "complete" {
		t.Fatalf("unexpected rca payload: %#v", rca)
	}
	failure := got["failure"].(map[string]any)
	if failure["kind"] != "missing_profit_or_economic_oracle" {
		t.Fatalf("unexpected failure payload: %#v", failure)
	}
}

func TestTerminalEventPayloadIncludesRCAProofBoundary(t *testing.T) {
	in := Input{ExitCode: 0, SummaryBytes: []byte(`{
		"status":"partial",
		"poc":{
			"status":"verified",
			"execution_state":"economic_poc"
		},
		"rca":{
			"status":"partial",
			"analysis_status":"partial",
			"root_cause_mode":"unknown",
			"proof_boundary_kind":"no_patchable_root_cause_evidence",
			"blocker_reason":"PoC profit is reproduced, but supplied trace/source/RPC artifacts do not identify a patchable contract/function/branch failed invariant."
		}
	}`)}

	mapped := Map(in)
	payload := TerminalEventPayload(mapped, in)

	if payload["outcome"] != OutcomePartial {
		t.Fatalf("outcome = %v, want %s", payload["outcome"], OutcomePartial)
	}
	if payload["poc_state"] != PoCStateEconomic {
		t.Fatalf("poc_state = %v, want %s", payload["poc_state"], PoCStateEconomic)
	}
	if payload["rca_state"] != RCAStateNoPatchableEvidence {
		t.Fatalf("rca_state = %v, want %s", payload["rca_state"], RCAStateNoPatchableEvidence)
	}
	if payload["publish_tier"] != PublishTierEconomicIncompleteRCA || payload["github_publish_eligible"] != true || payload["x_publish_eligible"] != false {
		t.Fatalf("publish payload = tier %v github %v x %v", payload["publish_tier"], payload["github_publish_eligible"], payload["x_publish_eligible"])
	}
	if payload["rerun_decision"] != RerunDecisionNoRerun {
		t.Fatalf("rerun_decision = %v, want %s", payload["rerun_decision"], RerunDecisionNoRerun)
	}
	if payload["rerun_reason"] != "no_patchable_root_cause_evidence" {
		t.Fatalf("rerun_reason = %v", payload["rerun_reason"])
	}
	rca := payload["rca"].(map[string]string)
	if rca["state"] != RCAStateNoPatchableEvidence || rca["root_cause_mode"] != "unknown" || rca["proof_boundary_kind"] != "no_patchable_root_cause_evidence" {
		t.Fatalf("unexpected rca payload: %#v", rca)
	}
}

func TestTerminalEventPayloadFallsBackToRuleForMissingSummary(t *testing.T) {
	in := Input{ExitCode: 0, SummaryMissing: true}
	mapped := Map(in)
	payload := TerminalEventPayload(mapped, in)

	if payload["outcome"] != OutcomeEngineError {
		t.Fatalf("outcome = %v, want %s", payload["outcome"], OutcomeEngineError)
	}
	if payload["rule"] != "O6" {
		t.Fatalf("rule = %v, want O6", payload["rule"])
	}
	if payload["failure_kind"] != FailureSummaryMissing {
		t.Fatalf("failure_kind = %v, want %s", payload["failure_kind"], FailureSummaryMissing)
	}
	if payload["analysis_stage"] != AnalysisStageEngineError {
		t.Fatalf("analysis_stage = %v, want %s", payload["analysis_stage"], AnalysisStageEngineError)
	}
	if payload["rerun_decision"] != RerunDecisionManualReview {
		t.Fatalf("rerun_decision = %v, want %s", payload["rerun_decision"], RerunDecisionManualReview)
	}
	if _, ok := payload["poc"]; ok {
		t.Fatalf("poc payload should be absent when summary is missing: %#v", payload)
	}
}

func TestTerminalEventPayloadIncludesSpecificEngineFailure(t *testing.T) {
	in := Input{ExitCode: 0, SummaryBytes: []byte(`{
		"status":"blocked",
		"poc":{"status":"verified"},
		"rca":{"status":"blocked","blocker_code":"rca_agent_runtime_error"},
		"failure":{
			"kind":"rca_agent_runtime_error",
			"category":"engine_error",
			"detail_kind":"codex_sdk_auth_error",
			"message":"RCA agent runtime error (codex_sdk_auth_error): refresh token expired"
		}
	}`)}
	mapped := Map(in)
	payload := TerminalEventPayload(mapped, in)

	if payload["outcome"] != OutcomeEngineError {
		t.Fatalf("outcome = %v, want %s", payload["outcome"], OutcomeEngineError)
	}
	if payload["failure_kind"] != "rca_agent_runtime_error" {
		t.Fatalf("failure_kind = %v", payload["failure_kind"])
	}
	if payload["rerun_reason"] != "codex_sdk_auth_error" {
		t.Fatalf("rerun_reason = %v", payload["rerun_reason"])
	}
	failure := payload["failure"].(map[string]string)
	if failure["category"] != "engine_error" || failure["detail_kind"] != "codex_sdk_auth_error" {
		t.Fatalf("unexpected failure payload: %#v", failure)
	}
}
