package notify

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/store"
)

func TestBackoff_ExponentialUpToCeiling(t *testing.T) {
	n := &Notifier{
		BackoffBase: 2 * time.Second,
		BackoffMax:  60 * time.Second,
	}
	cases := []struct {
		completedAttempt int
		want             time.Duration
	}{
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{4, 16 * time.Second},
		{6, 60 * time.Second}, // capped
	}
	for _, tc := range cases {
		got := n.backoff(tc.completedAttempt)
		if got != tc.want {
			t.Errorf("backoff(%d) = %v, want %v", tc.completedAttempt, got, tc.want)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		status int
		err    error
		want   string
	}{
		{"2xx success", 200, nil, "success"},
		{"4xx permanent", 403, errors.New("..."), "permanent_failure"},
		{"5xx retryable", 500, errors.New("..."), "retryable_failure"},
		{"transport error", 0, errors.New("connection refused"), "retryable_failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.status, tc.err); got != tc.want {
				t.Errorf("classify(%d, %v) = %q, want %q", tc.status, tc.err, got, tc.want)
			}
		})
	}
}

func TestRenderTelegramText_TruncatesTxAndIncludesEverything(t *testing.T) {
	failure := "rpc_timeout"
	root := "/var/helios/outputs/case_xyz"
	summary := "/var/helios/outputs/case_xyz/summary.json"
	eligible := true
	p := Payload{
		Event:                EventEngineError,
		CaseID:               "case_xyz",
		IncidentSlug:         "260603_eth_test_case",
		Chain:                "ethereum",
		TxHash:               "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab",
		State:                "failed",
		Outcome:              "engine_error",
		FailureKind:          &failure,
		HandoffStatus:        "skipped",
		OutputRoot:           &root,
		SummaryJSONPath:      &summary,
		AttemptNumber:        1,
		SummaryStatus:        "partial",
		AnalysisStage:        "rca_blocked",
		RerunDecision:        "auto_rerun",
		RerunReason:          "source_gap",
		AutoRerunEligible:    &eligible,
		AutoRerunResumeStage: "rca",
		CompletedAt:          "2026-06-03T03:25:16Z",
		ReportURL:            "https://github.com/UPside-Lumos-V2/Q1-2026/blob/main/test/case/README.md",
		PoCURL:               "https://github.com/UPside-Lumos-V2/Q1-2026/blob/main/test/case/PoC.t.sol",
		CommitURL:            "https://github.com/UPside-Lumos-V2/Q1-2026/commit/abc123",
	}
	text := renderTelegramText(p)
	mustContain := []string{
		"[helios] RCA blocked",
		"Incident: Test Case on Ethereum",
		"Tx: 0xaaaaaaaa…aaab",
		"Result: RCA blocked · auto rerun",
		"Reason: source_gap",
		"0xaaaaaaaa", // first 10 of tx
		"aaab",       // last 4 of tx
		"…",
		"Report: https://github.com/UPside-Lumos-V2/Q1-2026/blob/main/test/case/README.md",
		"Completed: 2026-06-03 03:25 UTC",
	}
	for _, m := range mustContain {
		if !strings.Contains(text, m) {
			t.Errorf("rendered text missing %q\ntext:\n%s", m, text)
		}
	}
}

func TestRenderTelegramText_OmitsEmptyOptionalFields(t *testing.T) {
	p := Payload{
		Event:   EventVerified,
		CaseID:  "case_abc",
		Chain:   "ethereum",
		TxHash:  "0x" + strings.Repeat("a", 64),
		State:   "handed-off",
		Outcome: "verified",
	}
	text := renderTelegramText(p)
	if strings.Contains(text, "failure_kind=") {
		t.Errorf("expected no failure_kind line, got:\n%s", text)
	}
	if strings.Contains(text, "summary=") {
		t.Errorf("expected no summary line, got:\n%s", text)
	}
	for _, removed := range []string{"poc_url=", "commit_url=", "handoff_status=", "stored_outcome=", "case_id="} {
		if strings.Contains(text, removed) {
			t.Errorf("expected no noisy field %q, got:\n%s", removed, text)
		}
	}
}

func TestPayloadAnalysisEnrichmentInfersRCABlockedFromLegacyPayload(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "bsc", "0x"+strings.Repeat("c", 64), nil, nil, json.RawMessage(`{"protocol":"test"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendCaseEvent(ctx, c.CaseID, "state_transition", map[string]any{
		"outcome":        "engine_error",
		"failure_kind":   "lumoskit_unexpected_summary_shape",
		"summary_status": "blocked",
		"poc": map[string]any{
			"status": "verified",
		},
		"rca": map[string]any{
			"status":         "blocked",
			"blocker_reason": "missing allowance provenance",
		},
		"failure": map[string]any{
			"kind": "rca_blocked",
		},
	}); err != nil {
		t.Fatal(err)
	}

	payload := PayloadFromCase(c, EventEngineError)
	n := &Notifier{Store: st}
	if err := n.enrichWithAnalysisPayload(ctx, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AnalysisStage != "rca_blocked" || payload.RerunDecision != "auto_rerun" || payload.RerunReason != "missing allowance provenance" {
		t.Fatalf("legacy payload not inferred as rca_blocked: %+v", payload)
	}
	text := renderTelegramText(payload)
	for _, marker := range []string{"[helios] RCA blocked", "Result: RCA blocked · auto rerun", "Reason: missing allowance provenance"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("telegram text missing %q:\n%s", marker, text)
		}
	}
	for _, removed := range []string{"event=engine_error", "stored_outcome=", "summary="} {
		if strings.Contains(text, removed) {
			t.Fatalf("telegram text still contains noisy field %q:\n%s", removed, text)
		}
	}
}

func TestPayloadAnalysisEnrichmentFromCaseEvents(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "ethereum", "0x"+strings.Repeat("b", 64), nil, nil, json.RawMessage(`{"protocol":"test"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendCaseEvent(ctx, c.CaseID, "state_transition", map[string]any{
		"summary_status":          "partial",
		"analysis_stage":          "poc_blocked",
		"rerun_decision":          "auto_rerun",
		"rerun_reason":            "missing_profit_or_economic_oracle",
		"auto_rerun_eligible":     true,
		"auto_rerun_resume_stage": "agent_poc",
	}); err != nil {
		t.Fatal(err)
	}

	payload := PayloadFromCase(c, EventPartial)
	n := &Notifier{Store: st}
	if err := n.enrichWithAnalysisPayload(ctx, &payload); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendCaseEvent(ctx, c.CaseID, "github_publish", map[string]any{
		"published":  true,
		"report_url": "https://github.com/UPside-Lumos-V2/Q1-2026/blob/main/test/case/README.md",
		"poc_url":    "https://github.com/UPside-Lumos-V2/Q1-2026/blob/main/test/case/PoC.t.sol",
		"commit_url": "https://github.com/UPside-Lumos-V2/Q1-2026/commit/abc123",
	}); err != nil {
		t.Fatal(err)
	}

	payload = PayloadFromCase(c, EventPartial)
	if err := n.enrichWithAnalysisPayload(ctx, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AnalysisStage != "poc_blocked" || payload.RerunDecision != "auto_rerun" || payload.AutoRerunResumeStage != "agent_poc" {
		t.Fatalf("analysis payload not enriched: %+v", payload)
	}
	if payload.ReportURL == "" || payload.PoCURL == "" || payload.CommitURL == "" {
		t.Fatalf("github links not enriched: %+v", payload)
	}
	if payload.AutoRerunEligible == nil || *payload.AutoRerunEligible != true {
		t.Fatalf("auto_rerun_eligible = %v", payload.AutoRerunEligible)
	}
}

func TestPayloadFromCaseMatchesEventField(t *testing.T) {
	c := &store.Case{
		CaseID:        "case_abc",
		Chain:         "ethereum",
		TxHash:        "0x" + strings.Repeat("a", 64),
		State:         "failed",
		AttemptNumber: 1,
		HandoffStatus: "skipped",
	}
	p := PayloadFromCase(c, EventHandoffFailed)
	if p.Event != EventHandoffFailed {
		t.Fatalf("expected event=%s, got %s", EventHandoffFailed, p.Event)
	}
	if p.CaseID != c.CaseID {
		t.Fatalf("CaseID wired wrong: %s", p.CaseID)
	}
	if p.HandoffStatus != "skipped" {
		t.Fatalf("HandoffStatus wired wrong: %s", p.HandoffStatus)
	}
}
