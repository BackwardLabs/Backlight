package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/artifacts"
	"github.com/UPside-Lumos-V2/helios/internal/config"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

func TestECWExportUsesDedicatedTokenAndCompleteProfile(t *testing.T) {
	ctx := context.Background()
	outputRoot := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "ethereum", strings.Repeat("1", 66), nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err = st.ClaimNextQueued(ctx, outputRoot)
	if err != nil {
		t.Fatal(err)
	}
	writeCaseFile(t, *c.OutputRoot, "report_bundle/report/REPORT.md", "# report\n")
	writeCaseFile(t, *c.OutputRoot, "artifacts/rca/rca_frontier.json", `{"schema_version":"root-cause-frontier/v1"}`)
	writeCaseFile(t, *c.OutputRoot, "artifacts/poc_sketch/poc_context.json", `{"case":"context"}`)
	writeCaseFile(t, *c.OutputRoot, "artifacts/agent_poc/result.json", `{"status":"pass"}`)
	writeCaseFile(t, *c.OutputRoot, "artifacts/agent_poc/foundry/test/PoC.t.sol", "contract PoC {}\n")
	writeCaseFile(t, *c.OutputRoot, "artifacts/secret.json", `{"secret":true}`)

	h := NewServer(&config.Config{APIToken: "api-token", ECWExportToken: "ecw-token", OutputRoot: outputRoot}, st, nil).Handler()
	req := httptest.NewRequest(http.MethodGet, "/ecw/cases/"+c.CaseID+"/export", nil)
	req.Header.Set("Authorization", "Bearer ecw-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("ECW export status = %d: %s", rr.Code, rr.Body.String())
	}

	var body ECWExportResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Profile != ecwExportProfile {
		t.Fatalf("profile = %q, want %q", body.Profile, ecwExportProfile)
	}
	if body.Case.CaseID != c.CaseID || body.Case.Chain != "ethereum" {
		t.Fatalf("unexpected case metadata: %+v", body.Case)
	}
	for _, rel := range []string{
		"report_bundle/report/REPORT.md",
		"artifacts/rca/rca_frontier.json",
		"artifacts/poc_sketch/poc_context.json",
		"artifacts/agent_poc/result.json",
		"artifacts/agent_poc/foundry/test/PoC.t.sol",
	} {
		if !slices.Contains(body.Allowed, rel) {
			t.Fatalf("allowed profile missing %q", rel)
		}
		if !ecwExportContainsArtifact(body.Artifacts, rel) {
			t.Fatalf("export missing artifact %q", rel)
		}
	}
	if ecwExportContainsArtifact(body.Artifacts, "artifacts/secret.json") {
		t.Fatal("ECW export included non-profile artifacts/secret.json")
	}
	if !slices.Contains(body.Missing, "artifacts/poc_sketch/foundry_spec.json") {
		t.Fatalf("missing list does not include absent profile file: %+v", body.Missing)
	}

	var raw map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	caseRaw, ok := raw["case"].(map[string]any)
	if !ok {
		t.Fatalf("case body has unexpected shape: %#v", raw["case"])
	}
	if _, ok := caseRaw["output_root"]; ok {
		t.Fatal("ECW case metadata exposed output_root; want minimal metadata")
	}
}

func TestECWExportTokenIsIsolatedFromCoreAPIToken(t *testing.T) {
	ctx := context.Background()
	outputRoot := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, _, err := st.SubmitCase(ctx, "ethereum", strings.Repeat("2", 66), nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimNextQueued(ctx, outputRoot); err != nil {
		t.Fatal(err)
	}

	h := NewServer(&config.Config{APIToken: "api-token", ECWExportToken: "ecw-token", OutputRoot: outputRoot}, st, nil).Handler()

	for _, tc := range []struct {
		name   string
		token  string
		status int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "wrong", token: "wrong-token", status: http.StatusForbidden},
		{name: "core-api-token", token: "api-token", status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ecw/cases/"+c.CaseID+"/export", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/cases", nil)
	req.Header.Set("Authorization", "Bearer ecw-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("ECW token authorized /cases status = %d, want 403", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/cases", nil)
	req.Header.Set("Authorization", "Bearer api-token")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("core API token /cases status = %d: %s", rr.Code, rr.Body.String())
	}
}

func TestECWExportDisabledWhenTokenUnset(t *testing.T) {
	h := NewServer(&config.Config{APIToken: "api-token", OutputRoot: t.TempDir()}, nil, nil).Handler()
	req := httptest.NewRequest(http.MethodGet, "/ecw/cases/case_1/export", nil)
	req.Header.Set("Authorization", "Bearer api-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("disabled ECW export status = %d, want 403: %s", rr.Code, rr.Body.String())
	}
}

func TestExistingArtifactAPIDoesNotExposeECWProfile(t *testing.T) {
	ctx := context.Background()
	outputRoot := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, _, err := st.SubmitCase(ctx, "ethereum", strings.Repeat("3", 66), nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err = st.ClaimNextQueued(ctx, outputRoot)
	if err != nil {
		t.Fatal(err)
	}
	writeCaseFile(t, *c.OutputRoot, "report_bundle/report/REPORT.md", "# report\n")
	writeCaseFile(t, *c.OutputRoot, "artifacts/rca/rca_frontier.json", `{}`)

	h := NewServer(&config.Config{APIToken: "api-token", ECWExportToken: "ecw-token", OutputRoot: outputRoot}, st, nil).Handler()
	req := httptest.NewRequest(http.MethodGet, "/cases/"+c.CaseID+"/artifacts", nil)
	req.Header.Set("Authorization", "Bearer api-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list artifacts status = %d: %s", rr.Code, rr.Body.String())
	}
	var body ArtifactListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(body.Allowed, "artifacts/rca/rca_frontier.json") {
		t.Fatal("existing artifact API exposes ECW RCA frontier; want product profile unchanged")
	}
}

func ecwExportContainsArtifact(items []artifacts.ReadResult, rel string) bool {
	for _, item := range items {
		if item.Path == rel {
			return true
		}
	}
	return false
}

func writeCaseFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
