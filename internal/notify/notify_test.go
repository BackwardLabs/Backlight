package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
		ReportURL:            "https://github.com/BackwardLabs/Q1-2026/blob/main/test/case/README.md",
		PoCURL:               "https://github.com/BackwardLabs/Q1-2026/blob/main/test/case/PoC.t.sol",
		CommitURL:            "https://github.com/BackwardLabs/Q1-2026/commit/abc123",
	}
	text := renderTelegramText(p)
	mustContain := []string{
		"[Backlight] RCA blocked",
		"Incident: Test Case on Ethereum",
		"Tx: 0xaaaaaaaa…aaab",
		"Result: RCA blocked · auto rerun",
		"Reason: source_gap",
		"0xaaaaaaaa", // first 10 of tx
		"aaab",       // last 4 of tx
		"…",
		"Report: https://github.com/BackwardLabs/Q1-2026/blob/main/test/case/README.md",
		"Completed: 2026-06-03 03:25 UTC",
	}
	for _, m := range mustContain {
		if !strings.Contains(text, m) {
			t.Errorf("rendered text missing %q\ntext:\n%s", m, text)
		}
	}
}

func TestRenderTelegramText_EngineErrorIncludesDiagnosisLines(t *testing.T) {
	failure := "tx_not_found"
	retryable := false
	p := Payload{
		Event:              EventEngineError,
		CaseID:             "case_260611_arb_nova_finance_a02_4d5e6f7a_420a",
		IncidentSlug:       "260611_arb_nova_finance",
		Chain:              "arbitrum",
		TxHash:             "0x4d5e6f7a" + strings.Repeat("0", 56),
		State:              "failed",
		Outcome:            "engine_error",
		FailureKind:        &failure,
		AnalysisStage:      "engine_error",
		RerunDecision:      "manual_review",
		RerunReason:        "tx_not_found",
		EngineErrorKind:    "tx_not_found",
		DiagnosisOwner:     "upstream_input",
		DiagnosisRetryable: &retryable,
		DiagnosisAction:    "verify the tx hash exists on the submitted chain",
		CompletedAt:        "2026-06-13T14:23:00Z",
	}
	text := renderTelegramText(p)
	mustContain := []string{
		"[Backlight] Engine error",
		"Reason: tx_not_found",
		"Owner: upstream_input",
		"Retryable: no",
		"Action: verify the tx hash exists on the submitted chain",
		"Case: case_260611_arb_nova_finance_a02_4d5e6f7a_420a",
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

func TestRenderTelegramText_PoCMissingIsIncompleteNotFailed(t *testing.T) {
	failure := "localize_reconstruction_blocked"
	p := Payload{
		Event:          EventUnverified,
		CaseID:         "case_missing",
		IncidentSlug:   "260725_eth_missing",
		Chain:          "ethereum",
		TxHash:         "0x" + strings.Repeat("a", 64),
		State:          "handed-off",
		Outcome:        "unverified",
		FailureKind:    &failure,
		AnalysisStage:  "poc_missing",
		PoCState:       "poc_missing",
		PoCStatus:      "missing",
		PoCFailureKind: failure,
		RerunDecision:  "manual_review",
		RerunReason:    failure,
	}

	text := renderTelegramText(p)

	for _, marker := range []string{
		"[Backlight] PoC incomplete",
		"Result: PoC missing (localize reconstruction blocked) · manual review",
		"Reason: localize_reconstruction_blocked",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("telegram text missing %q:\n%s", marker, text)
		}
	}
	if strings.Contains(text, "PoC failed") {
		t.Fatalf("missing evidence was mislabeled as a failed PoC:\n%s", text)
	}
}

func TestRenderTelegramText_ForgeFailureRemainsPoCFailed(t *testing.T) {
	failure := "forge_test_failed"
	p := Payload{
		Event:          EventUnverified,
		CaseID:         "case_failed",
		IncidentSlug:   "260725_eth_failed",
		Chain:          "ethereum",
		TxHash:         "0x" + strings.Repeat("b", 64),
		State:          "handed-off",
		Outcome:        "unverified",
		FailureKind:    &failure,
		AnalysisStage:  "poc_failed",
		PoCState:       "poc_failed",
		PoCStatus:      "unverified",
		PoCFailureKind: failure,
		RerunDecision:  "manual_review",
		RerunReason:    failure,
	}

	text := renderTelegramText(p)

	if !strings.Contains(text, "[Backlight] PoC failed") {
		t.Fatalf("real Forge failure was not preserved:\n%s", text)
	}
	if strings.Contains(text, "[Backlight] PoC incomplete") {
		t.Fatalf("real Forge failure was mislabeled as incomplete:\n%s", text)
	}
}

func TestNotifierSuppressesOnlyIdenticalSuccessfulManualRerunOutcome(t *testing.T) {
	tests := []struct {
		name           string
		previousStatus string
		currentReason  string
		currentEvent   string
		wantDeliveries int
		wantSuppressed bool
	}{
		{
			name:           "identical successful prior notification is suppressed",
			previousStatus: "succeeded",
			currentReason:  "localize_reconstruction_blocked",
			wantDeliveries: 0,
			wantSuppressed: true,
		},
		{
			name:           "changed diagnosis is delivered",
			previousStatus: "succeeded",
			currentReason:  "downstream_position_not_reproduced",
			wantDeliveries: 1,
		},
		{
			name:           "failed prior notification is retried",
			previousStatus: "failed",
			currentReason:  "localize_reconstruction_blocked",
			wantDeliveries: 1,
		},
		{
			name:           "handoff failure is never suppressed",
			previousStatus: "succeeded",
			currentReason:  "localize_reconstruction_blocked",
			currentEvent:   EventHandoffFailed,
			wantDeliveries: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()

			txHash := "0x" + strings.Repeat("c", 64)
			terminalPayload := func(reason string) map[string]any {
				return map[string]any{
					"outcome":        "unverified",
					"summary_status": "fail",
					"analysis_stage": "poc_missing",
					"rerun_decision": "manual_review",
					"rerun_reason":   reason,
					"poc_state":      "poc_missing",
					"rca_state":      "rca_poc_dependent",
					"poc": map[string]any{
						"state":        "poc_missing",
						"status":       "missing",
						"failure_kind": reason,
					},
					"rca": map[string]any{
						"state":  "rca_poc_dependent",
						"status": "blocked",
					},
				}
			}
			complete := func(force bool, reason string) *store.Case {
				c, _, err := st.SubmitCase(ctx, "ethereum", txHash, nil, nil, nil, force)
				if err != nil {
					t.Fatal(err)
				}
				c, err = st.ClaimNextQueued(ctx, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				if err := st.MarkDoneWithPayload(ctx, c.CaseID, "unverified", false, terminalPayload(reason)); err != nil {
					t.Fatal(err)
				}
				c, err = st.GetCase(ctx, c.CaseID)
				if err != nil {
					t.Fatal(err)
				}
				return c
			}

			previous := complete(false, "localize_reconstruction_blocked")
			if err := st.SetNotificationStatus(ctx, previous.CaseID, tc.previousStatus); err != nil {
				t.Fatal(err)
			}
			if tc.previousStatus == "succeeded" {
				if err := st.RecordNotificationAttempt(ctx, store.NotificationAttempt{
					AttemptID:    store.NewID("noa"),
					CaseID:       previous.CaseID,
					Channel:      "telegram",
					Event:        EventUnverified,
					AttemptedAt:  time.Now().UTC().Format(time.RFC3339Nano),
					Result:       "success",
					AttemptIndex: 1,
				}); err != nil {
					t.Fatal(err)
				}
			}
			current := complete(true, tc.currentReason)

			deliveries := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				deliveries++
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()
			notifier := &Notifier{
				Store:       st,
				Channels:    []Channel{&TelegramChannel{BotToken: "token", ChatID: "chat", APIBase: server.URL, Client: server.Client()}},
				MaxAttempts: 1,
				Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			}

			currentEvent := tc.currentEvent
			if currentEvent == "" {
				currentEvent = EventUnverified
			}
			notifier.Notify(ctx, current, currentEvent)

			if deliveries != tc.wantDeliveries {
				t.Fatalf("deliveries = %d, want %d", deliveries, tc.wantDeliveries)
			}
			events, err := st.CaseEvents(ctx, current.CaseID)
			if err != nil {
				t.Fatal(err)
			}
			suppressed := false
			for _, event := range events {
				if event.EventType == "notification_suppressed" {
					suppressed = true
				}
			}
			if suppressed != tc.wantSuppressed {
				t.Fatalf("notification_suppressed event = %v, want %v", suppressed, tc.wantSuppressed)
			}
		})
	}
}

func TestTelegramDeliverUsesExactPublishText(t *testing.T) {
	want := "[Backlight Initial Analysis]\n\nmain body\n\nGitHub:\nhttps://github.com/BackwardLabs/Q1-2026/tree/main/test/2026-01/truebit"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/sendMessage" {
			http.NotFound(w, r)
			return
		}
		var body telegramRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ChatID != "chat" || body.Text != want || body.ParseMode != "" {
			t.Fatalf("telegram body = %+v", body)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	ch := &TelegramChannel{BotToken: "token", ChatID: "chat", APIBase: server.URL, Client: server.Client()}
	status, err := ch.Deliver(context.Background(), Payload{Event: EventTelegramPublish, TelegramText: want})
	if err != nil || status != http.StatusOK {
		t.Fatalf("deliver status=%d err=%v", status, err)
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
	for _, marker := range []string{"[Backlight] RCA blocked", "Result: PoC verified; RCA blocked (missing allowance provenance) · auto rerun", "Reason: missing allowance provenance"} {
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

func TestPayloadAnalysisEnrichmentInfersReachablePoCOverEconomicProofGap(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "bsc", "0x"+strings.Repeat("e", 64), nil, nil, json.RawMessage(`{"protocol":"test"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendCaseEvent(ctx, c.CaseID, "state_transition", map[string]any{
		"outcome":        "partial",
		"summary_status": "partial",
		"poc": map[string]any{
			"status":            "unverified",
			"execution_state":   "reachable_poc",
			"proof_kind":        "reachability_only",
			"forge_test_status": "pass",
			"failure_kind":      "missing_profit_or_economic_oracle",
		},
		"rca": map[string]any{
			"status":         "blocked",
			"blocker_code":   "economic_proof_gap",
			"blocker_reason": "proof_kind is reachability_only, expected economic_proof",
		},
	}); err != nil {
		t.Fatal(err)
	}

	payload := PayloadFromCase(c, EventPartial)
	n := &Notifier{Store: st}
	if err := n.enrichWithAnalysisPayload(ctx, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AnalysisStage != "reachable_poc" || payload.RerunDecision != "guided_repair" || payload.RerunReason != "missing_profit_or_economic_oracle" {
		t.Fatalf("legacy economic proof gap not inferred as reachable_poc: %+v", payload)
	}
	text := renderTelegramText(payload)
	for _, marker := range []string{"[Backlight] Reachable PoC", "Result: PoC reachable (reachability only, forge test pass, missing profit or economic oracle); RCA blocked (economic proof gap) · guided repair", "Reason: missing_profit_or_economic_oracle"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("telegram text missing %q:\n%s", marker, text)
		}
	}
}

func TestPayloadAnalysisEnrichmentShowsPoCAndPartialRCA(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "ethereum", "0x"+strings.Repeat("8", 64), nil, nil, json.RawMessage(`{"protocol":"taico"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendCaseEvent(ctx, c.CaseID, "state_transition", map[string]any{
		"outcome":        "partial",
		"summary_status": "partial",
		"analysis_stage": "rca_blocked",
		"rerun_decision": "no_rerun",
		"rerun_reason":   "partial",
		"poc_state":      "economic_poc",
		"rca_state":      "rca_scope_limited",
		"poc": map[string]any{
			"state":              "economic_poc",
			"status":             "verified",
			"proof_kind":         "economic_proof",
			"forge_build_status": "pass",
			"forge_test_status":  "pass",
		},
		"rca": map[string]any{
			"state":           "rca_scope_limited",
			"status":          "partial",
			"analysis_status": "partial",
		},
	}); err != nil {
		t.Fatal(err)
	}

	payload := PayloadFromCase(c, EventPartial)
	n := &Notifier{Store: st}
	if err := n.enrichWithAnalysisPayload(ctx, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PoCState != "economic_poc" || payload.RCAState != "rca_scope_limited" || payload.RCAStatus != "partial" {
		t.Fatalf("PoC/RCA status not enriched: %+v", payload)
	}
	text := renderTelegramText(payload)
	for _, marker := range []string{
		"[Backlight] Partial result",
		"Result: PoC verified (economic proof, forge test pass); RCA partial (scope limited) · no rerun",
		"Reason: rca_scope_limited",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("telegram text missing %q:\n%s", marker, text)
		}
	}
	if strings.Contains(text, "[Backlight] RCA blocked") || strings.Contains(text, "Result: RCA blocked") {
		t.Fatalf("telegram text still collapses partial RCA to blocked:\n%s", text)
	}
}

func TestPayloadAnalysisEnrichmentShowsRCAAgentRuntimeError(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "bsc", "0x"+strings.Repeat("d", 64), nil, nil, json.RawMessage(`{"protocol":"test"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendCaseEvent(ctx, c.CaseID, "state_transition", map[string]any{
		"outcome":        "engine_error",
		"failure_kind":   "rca_agent_runtime_error",
		"analysis_stage": "engine_error",
		"rerun_decision": "manual_review",
		"rerun_reason":   "codex_sdk_auth_error",
		"rca": map[string]any{
			"status":       "blocked",
			"blocker_code": "rca_agent_runtime_error",
		},
		"failure": map[string]any{
			"kind":        "rca_agent_runtime_error",
			"category":    "engine_error",
			"detail_kind": "codex_sdk_auth_error",
			"message":     "RCA agent runtime error (codex_sdk_auth_error): refresh token expired",
		},
	}); err != nil {
		t.Fatal(err)
	}

	failureKind := "rca_agent_runtime_error"
	outcome := "engine_error"
	c.Outcome = &outcome
	c.FailureKind = &failureKind
	payload := PayloadFromCase(c, EventEngineError)
	n := &Notifier{Store: st}
	if err := n.enrichWithAnalysisPayload(ctx, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.FailureCategory != "engine_error" || payload.FailureDetailKind != "codex_sdk_auth_error" {
		t.Fatalf("failure details not enriched: %+v", payload)
	}
	text := renderTelegramText(payload)
	for _, marker := range []string{
		"[Backlight] RCA agent runtime error",
		"Result: RCA agent runtime error · manual review",
		"Reason: codex_sdk_auth_error",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("telegram text missing %q:\n%s", marker, text)
		}
	}
	if strings.Contains(text, "[Backlight] Engine error") || strings.Contains(text, "Result: engine error") {
		t.Fatalf("telegram text used generic engine error:\n%s", text)
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
		"analysis_stage":          "reachable_poc",
		"rerun_decision":          "guided_repair",
		"rerun_reason":            "missing_profit_or_economic_oracle",
		"auto_rerun_eligible":     true,
		"auto_rerun_resume_stage": "agent_poc_repair",
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
		"report_url": "https://github.com/BackwardLabs/Q1-2026/blob/main/test/case/README.md",
		"poc_url":    "https://github.com/BackwardLabs/Q1-2026/blob/main/test/case/PoC.t.sol",
		"commit_url": "https://github.com/BackwardLabs/Q1-2026/commit/abc123",
	}); err != nil {
		t.Fatal(err)
	}

	payload = PayloadFromCase(c, EventPartial)
	if err := n.enrichWithAnalysisPayload(ctx, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AnalysisStage != "reachable_poc" || payload.RerunDecision != "guided_repair" || payload.AutoRerunResumeStage != "agent_poc_repair" {
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
