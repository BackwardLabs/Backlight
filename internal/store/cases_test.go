package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

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
