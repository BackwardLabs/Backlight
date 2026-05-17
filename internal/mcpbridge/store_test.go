package mcpbridge

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/handoff"
	"github.com/UPside-Lumos-V2/helios/internal/heliosclient"
)

func TestStoreUpsertListAndGetCase(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	out := filepath.Join(t.TempDir(), "case_1")
	summary := filepath.Join(out, "summary.json")
	if err := st.UpsertPayload(ctx, handoff.Payload{
		CaseID:          "case_1",
		Chain:           "ethereum",
		TxHash:          "0xabc",
		State:           "done",
		Outcome:         "verified",
		OutputRoot:      &out,
		SummaryJSONPath: &summary,
		AttemptNumber:   1,
	}); err != nil {
		t.Fatal(err)
	}

	list, err := st.ListCases(ctx, heliosclient.ListOptions{Outcome: "verified"})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].CaseID != "case_1" {
		t.Fatalf("unexpected list response: %+v", list)
	}
	if list.Items[0].OutputRoot == nil || *list.Items[0].OutputRoot != out {
		t.Fatalf("output root not preserved: %+v", list.Items[0].OutputRoot)
	}

	detail, err := st.GetCase(ctx, "case_1")
	if err != nil {
		t.Fatal(err)
	}
	if detail.CaseID != "case_1" || detail.Outcome == nil || *detail.Outcome != "verified" {
		t.Fatalf("unexpected detail: %+v", detail)
	}
}

func TestStoreValidatesPayload(t *testing.T) {
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertPayload(context.Background(), handoff.Payload{}); err == nil {
		t.Fatal("empty payload accepted; want validation error")
	}
	if err := st.UpsertPayload(context.Background(), handoff.Payload{
		CaseID:        "case_1",
		Chain:         "ethereum",
		TxHash:        "0xabc",
		State:         "failed",
		Outcome:       "engine_error",
		AttemptNumber: 1,
	}); err == nil {
		t.Fatal("engine_error payload accepted; want validation error")
	}
}

func TestOpenReadOnlyCannotCreateMissingDB(t *testing.T) {
	_, err := OpenReadOnly(context.Background(), filepath.Join(t.TempDir(), "missing.db"))
	if err == nil {
		t.Fatal("OpenReadOnly created missing DB; want error")
	}
}
