package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaimNextQueuedUsesIncidentSlugOutputRoot(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	detectedAt := "2026-05-26T01:02:03Z"
	metadata := json.RawMessage(`{"protocol":"Curve"}`)
	c, _, err := s.SubmitCase(ctx, "ethereum", "0x"+strings.Repeat("a", 64), nil, &detectedAt, metadata, false)
	if err != nil {
		t.Fatalf("submit case: %v", err)
	}

	outputParent := t.TempDir()
	claimed, err := s.ClaimNextQueued(ctx, outputParent)
	if err != nil {
		t.Fatalf("claim case: %v", err)
	}
	if claimed.CaseID != c.CaseID {
		t.Fatalf("claimed case = %s, want %s", claimed.CaseID, c.CaseID)
	}
	wantRoot := filepath.Join(outputParent, "260526_eth_curve")
	if claimed.OutputRoot == nil || *claimed.OutputRoot != wantRoot {
		t.Fatalf("output_root = %v, want %s", claimed.OutputRoot, wantRoot)
	}
	wantSummary := filepath.Join(wantRoot, "summary.json")
	if claimed.SummaryJSONPath == nil || *claimed.SummaryJSONPath != wantSummary {
		t.Fatalf("summary_json_path = %v, want %s", claimed.SummaryJSONPath, wantSummary)
	}
	if strings.Contains(filepath.Base(*claimed.OutputRoot), claimed.TxHash[:10]) {
		t.Fatalf("output slug leaked tx hash: %s", *claimed.OutputRoot)
	}
}

func TestClaimNextQueuedAddsNumericSuffixForIncidentSlugCollisions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	detectedAt := "2026-05-26T01:02:03Z"
	metadata := json.RawMessage(`{"protocol":"Curve Finance"}`)
	for _, txHash := range []string{"0x" + strings.Repeat("a", 64), "0x" + strings.Repeat("b", 64)} {
		if _, _, err := s.SubmitCase(ctx, "ethereum", txHash, nil, &detectedAt, metadata, false); err != nil {
			t.Fatalf("submit case %s: %v", txHash, err)
		}
	}

	outputParent := t.TempDir()
	first, err := s.ClaimNextQueued(ctx, outputParent)
	if err != nil {
		t.Fatalf("claim first: %v", err)
	}
	second, err := s.ClaimNextQueued(ctx, outputParent)
	if err != nil {
		t.Fatalf("claim second: %v", err)
	}

	wantFirst := filepath.Join(outputParent, "260526_eth_curve_finance")
	wantSecond := filepath.Join(outputParent, "260526_eth_curve_finance-2")
	if first.OutputRoot == nil || *first.OutputRoot != wantFirst {
		t.Fatalf("first output_root = %v, want %s", first.OutputRoot, wantFirst)
	}
	if second.OutputRoot == nil || *second.OutputRoot != wantSecond {
		t.Fatalf("second output_root = %v, want %s", second.OutputRoot, wantSecond)
	}
}

func TestSubmitCaseUsesReadableCaseID(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	detectedAt := "2026-05-26T00:00:00Z"
	metadata := json.RawMessage(`{"protocol":"Euler V2"}`)
	c, _, err := s.SubmitCase(ctx, "ethereum", "0x"+strings.Repeat("d", 64), nil, &detectedAt, metadata, false)
	if err != nil {
		t.Fatalf("SubmitCase: %v", err)
	}
	if !strings.HasPrefix(c.CaseID, "case_260526_eth_euler_v2_a01_dddddddd_") {
		t.Fatalf("case_id = %q, want readable incident prefix", c.CaseID)
	}
}

func TestProtocolSlugNormalizesSeedExamples(t *testing.T) {
	tests := map[string]string{
		"Curve Finance":          "curve_finance",
		"Euler v2":               "euler_v2",
		"Balancer: boosted pool": "balancer_boosted_pool",
		"0xabc...":               "unknown",
		"":                       "unknown",
		"a__very---long protocol name with extra text": "a_very_long_protocol_name_with_e",
	}
	for input, want := range tests {
		if got := protocolSlug(input); got != want {
			t.Fatalf("protocolSlug(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestProtocolFromMetadataFindsNestedProjectName(t *testing.T) {
	metadata := json.RawMessage(`{"incident":{"project_name":"Euler v2"}}`)
	c := &Case{
		Chain:      "ethereum",
		DetectedAt: ptr("2026-05-26T01:02:03Z"),
		CreatedAt:  "2026-05-27T00:00:00Z",
		Metadata:   metadata,
	}
	if got, want := incidentOutputSlug(c), "260526_eth_euler_v2"; got != want {
		t.Fatalf("incidentOutputSlug = %q, want %q", got, want)
	}
}

func TestMarkDoneWithPayloadPersistsAnalysisDiagnostics(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	c, _, err := s.SubmitCase(ctx, "ethereum", "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("submit case: %v", err)
	}
	if _, err := s.ClaimNextQueued(ctx, t.TempDir()); err != nil {
		t.Fatalf("claim case: %v", err)
	}

	payload := map[string]any{
		"outcome":        "partial",
		"rule":           "O2",
		"summary_status": "partial",
		"poc": map[string]string{
			"status":            "unverified",
			"proof_kind":        "reachability_only",
			"forge_test_status": "pass",
		},
		"rca": map[string]string{
			"status":         "blocked",
			"blocker_code":   "economic_proof_gap",
			"blocker_reason": "proof_kind is reachability_only, expected economic_proof",
		},
	}
	if err := s.MarkDoneWithPayload(ctx, c.CaseID, "partial", false, payload); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	events, err := s.CaseEvents(ctx, c.CaseID)
	if err != nil {
		t.Fatalf("case events: %v", err)
	}
	var terminalPayload map[string]any
	for _, event := range events {
		if event.EventType == "state_transition" && event.FromState != nil && *event.FromState == StateRunning && event.ToState != nil && *event.ToState == StateDone {
			if err := json.Unmarshal(event.Payload, &terminalPayload); err != nil {
				t.Fatalf("unmarshal terminal payload: %v", err)
			}
			break
		}
	}
	if terminalPayload == nil {
		t.Fatalf("running→done state_transition not found in events: %#v", events)
	}
	if terminalPayload["outcome"] != "partial" || terminalPayload["rule"] != "O2" {
		t.Fatalf("unexpected terminal payload header: %#v", terminalPayload)
	}
	poc := terminalPayload["poc"].(map[string]any)
	if poc["proof_kind"] != "reachability_only" {
		t.Fatalf("unexpected poc diagnostics: %#v", poc)
	}
	rca := terminalPayload["rca"].(map[string]any)
	if rca["blocker_code"] != "economic_proof_gap" {
		t.Fatalf("unexpected rca diagnostics: %#v", rca)
	}
}

func TestAppendCaseEventPersistsSideEffectEvidence(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	c, _, err := s.SubmitCase(ctx, "ethereum", "0xabababababababababababababababababababababababababababababababab", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("submit case: %v", err)
	}
	if err := s.AppendCaseEvent(ctx, c.CaseID, "github_publish", map[string]any{
		"published":  true,
		"commit_sha": "abc123",
		"poc_url":    "https://github.com/example/poc",
	}); err != nil {
		t.Fatalf("append case event: %v", err)
	}

	events, err := s.CaseEvents(ctx, c.CaseID)
	if err != nil {
		t.Fatalf("case events: %v", err)
	}
	var found map[string]any
	for _, event := range events {
		if event.EventType == "github_publish" {
			if err := json.Unmarshal(event.Payload, &found); err != nil {
				t.Fatalf("unmarshal publish payload: %v", err)
			}
			break
		}
	}
	if found == nil || found["published"] != true || found["commit_sha"] != "abc123" {
		t.Fatalf("github publish payload = %#v", found)
	}
}

func TestMarkDoneAndQueueAutoRerunStopsAtMaxAttempts(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	root, _, err := s.SubmitCase(ctx, "ethereum", "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("submit case: %v", err)
	}
	first, err := s.ClaimNextQueued(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("claim first: %v", err)
	}
	if first.CaseID != root.CaseID {
		t.Fatalf("claimed first case = %s, want %s", first.CaseID, root.CaseID)
	}

	second, queued, err := s.MarkDoneAndQueueAutoRerun(ctx, first.CaseID, "partial", map[string]any{"rule": "O2"}, "partial_auto_rerun", 3)
	if err != nil {
		t.Fatalf("queue second: %v", err)
	}
	if !queued || second.AttemptNumber != 2 || second.ParentCaseID == nil || *second.ParentCaseID != first.CaseID {
		t.Fatalf("unexpected second attempt: queued=%v case=%+v", queued, second)
	}

	secondClaim, err := s.ClaimNextQueued(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("claim second: %v", err)
	}
	third, queued, err := s.MarkDoneAndQueueAutoRerun(ctx, secondClaim.CaseID, "partial", map[string]any{"rule": "O2"}, "partial_auto_rerun", 3)
	if err != nil {
		t.Fatalf("queue third: %v", err)
	}
	if !queued || third.AttemptNumber != 3 || third.ParentCaseID == nil || *third.ParentCaseID != secondClaim.CaseID {
		t.Fatalf("unexpected third attempt: queued=%v case=%+v", queued, third)
	}

	thirdClaim, err := s.ClaimNextQueued(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("claim third: %v", err)
	}
	if _, queued, err := s.MarkDoneAndQueueAutoRerun(ctx, thirdClaim.CaseID, "partial", map[string]any{"rule": "O2"}, "partial_auto_rerun", 3); err != nil {
		t.Fatalf("third auto rerun check: %v", err)
	} else if queued {
		t.Fatalf("third partial attempt queued another rerun despite max attempts")
	}

	parent, err := s.GetCase(ctx, first.CaseID)
	if err != nil {
		t.Fatalf("get first parent: %v", err)
	}
	if parent.State != StateHandedOff || parent.HandoffStatus != "skipped" || parent.Outcome == nil || *parent.Outcome != "partial" {
		t.Fatalf("first parent state = %+v", parent)
	}
}
