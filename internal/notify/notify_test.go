package notify

import (
	"errors"
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
	p := Payload{
		Event:           EventEngineError,
		CaseID:          "case_xyz",
		Chain:           "ethereum",
		TxHash:          "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab",
		State:           "failed",
		Outcome:         "engine_error",
		FailureKind:     &failure,
		HandoffStatus:   "skipped",
		OutputRoot:      &root,
		SummaryJSONPath: &summary,
		AttemptNumber:   1,
	}
	text := renderTelegramText(p)
	mustContain := []string{
		"event=engine_error",
		"case_id=case_xyz",
		"chain=ethereum",
		"0xaaaaaaaa", // first 10 of tx
		"aaab",       // last 4 of tx
		"…",
		"state=failed",
		"outcome=engine_error",
		"failure_kind=rpc_timeout",
		"handoff_status=skipped",
		"summary=" + summary,
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
