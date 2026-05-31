package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/config"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

func TestCreateCaseFallsBackToUnknownWithoutIncidentIdentity(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	h := NewServer(&config.Config{APIToken: "secret"}, st, nil).Handler()
	body := strings.NewReader(`{"chain":"ethereum","tx_hash":"0x` + strings.Repeat("1", 64) + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/cases", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("POST /cases status = %d, want %d: %s", rr.Code, http.StatusAccepted, rr.Body.String())
	}
	var out SubmissionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode submission response: %v", err)
	}
	if !strings.Contains(out.CaseID, "_unknown_") {
		t.Fatalf("case_id = %q, want unknown fallback", out.CaseID)
	}
}

func TestCreateCaseUsesProtocolMetadataForInitialCaseID(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	h := NewServer(&config.Config{APIToken: "secret"}, st, nil).Handler()
	body := strings.NewReader(`{"chain":"ethereum","tx_hash":"0x` + strings.Repeat("2", 64) + `","detected_at":"2026-05-26T00:00:00Z","metadata":{"protocol":"FPC"}}`)
	req := httptest.NewRequest(http.MethodPost, "/cases", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("POST /cases status = %d, want %d: %s", rr.Code, http.StatusAccepted, rr.Body.String())
	}
	var out SubmissionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode submission response: %v", err)
	}
	if !strings.HasPrefix(out.CaseID, "case_260526_eth_fpc_a01_22222222_") {
		t.Fatalf("case_id = %q, want initial protocol slug", out.CaseID)
	}
	if strings.Contains(out.CaseID, "unknown") {
		t.Fatalf("case_id contains unknown: %s", out.CaseID)
	}
}

func TestSignalRequiresHackDetectorContract(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	h := NewServer(&config.Config{APIToken: "secret"}, st, nil).Handler()
	body := strings.NewReader(`{"chain":"ethereum","tx_hash":"0x` + strings.Repeat("3", 64) + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/signals", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /signals status = %d, want %d: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "source is required") || !strings.Contains(rr.Body.String(), "metadata is required") {
		t.Fatalf("validation body did not mention required signal fields: %s", rr.Body.String())
	}
}

func TestSignalUsesRequiredMetadataForCaseAndIncomingSignal(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	h := NewServer(&config.Config{APIToken: "secret"}, st, nil).Handler()
	body := strings.NewReader(`{
		"chain":"arbitrum",
		"tx_hash":"0x` + strings.Repeat("5", 64) + `",
		"source":"hack-detector:twitter:TenArmorAlert",
		"detected_at":"2026-05-18T03:58:43+00:00",
		"metadata":{
			"schema_version":"lumos_signal_to_helios.v1",
			"lumos_signal_id":"sig-1",
			"incident_group_id":"group-1",
			"protocol_name":"SEA",
			"loss_usd":153000,
			"source_ref":{
				"source":"twitter",
				"source_id":"tw:2056222926840500384",
				"source_url":"https://x.com/TenArmorAlert/status/2056222926840500384",
				"published_at":"2026-05-18T03:58:43+00:00"
			}
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/signals", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("POST /signals status = %d, want %d: %s", rr.Code, http.StatusAccepted, rr.Body.String())
	}
	var out SubmissionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode submission response: %v", err)
	}
	if !strings.HasPrefix(out.CaseID, "case_260518_arb_sea_a01_55555555_") {
		t.Fatalf("case_id = %q, want signal protocol/date slug", out.CaseID)
	}

	var protocolName, sourceURL, metadataText string
	if err := st.DB().QueryRowContext(ctx, `SELECT protocol_name, source_url, metadata FROM incoming_signals WHERE lumos_signal_id = ?`, "sig-1").Scan(&protocolName, &sourceURL, &metadataText); err != nil {
		t.Fatalf("query incoming signal: %v", err)
	}
	if protocolName != "SEA" || sourceURL != "https://x.com/TenArmorAlert/status/2056222926840500384" {
		t.Fatalf("incoming signal protocol/source = %q/%q", protocolName, sourceURL)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(metadataText), &metadata); err != nil {
		t.Fatalf("decode incoming signal metadata: %v", err)
	}
	if metadata["protocol"] != "SEA" || metadata["protocol_name"] != "SEA" {
		t.Fatalf("metadata protocol normalization missing: %#v", metadata)
	}

	events, err := st.CaseEvents(ctx, out.CaseID)
	if err != nil {
		t.Fatalf("case events: %v", err)
	}
	var sawSignal bool
	for _, event := range events {
		if event.EventType == "signal_received" {
			sawSignal = true
		}
	}
	if !sawSignal {
		t.Fatalf("signal_received event not found: %#v", events)
	}
}

type fakeIncidentResolver struct {
	metadata json.RawMessage
}

func (f fakeIncidentResolver) EnrichIncidentMetadata(ctx context.Context, chain, txHash string, metadata json.RawMessage) (json.RawMessage, error) {
	if len(f.metadata) > 0 {
		return f.metadata, nil
	}
	return metadata, nil
}

func TestCreateCaseUsesResolverMetadataBeforeCaseID(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	srv := NewServer(&config.Config{APIToken: "secret"}, st, nil)
	srv.Resolver = fakeIncidentResolver{metadata: json.RawMessage(`{"protocol":"Resolved FPC"}`)}
	h := srv.Handler()
	body := strings.NewReader(`{"chain":"bsc","tx_hash":"0x` + strings.Repeat("4", 64) + `","detected_at":"2026-05-26T00:00:00Z"}`)
	req := httptest.NewRequest(http.MethodPost, "/cases", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("POST /cases status = %d, want %d: %s", rr.Code, http.StatusAccepted, rr.Body.String())
	}
	var out SubmissionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode submission response: %v", err)
	}
	if !strings.HasPrefix(out.CaseID, "case_260526_bsc_resolved_fpc_a01_44444444_") {
		t.Fatalf("case_id = %q, want resolver protocol slug", out.CaseID)
	}
}
