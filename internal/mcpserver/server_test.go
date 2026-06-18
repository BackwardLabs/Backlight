package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/api"
	"github.com/UPside-Lumos-V2/helios/internal/artifacts"
	"github.com/UPside-Lumos-V2/helios/internal/heliosclient"
)

func TestToolsListExposesOnlyReadOnlyBacklightTools(t *testing.T) {
	s := testServer()
	id := json.RawMessage(`1`)

	resp := s.handleRequest(context.Background(), rpcRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "tools/list",
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", resp.Result)
	}
	tools, ok := result["tools"].([]toolDef)
	if !ok {
		t.Fatalf("tools type = %T", result["tools"])
	}
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
		if tool.Annotations["readOnlyHint"] != true || tool.Annotations["destructiveHint"] != false {
			t.Fatalf("tool %s annotations are not read-only: %+v", tool.Name, tool.Annotations)
		}
	}
	for _, want := range []string{"helios.list_cases", "helios.get_case", "helios.list_artifacts", "helios.read_artifact"} {
		if !names[want] {
			t.Fatalf("missing tool %q in %+v", want, names)
		}
	}
	if len(tools) != 4 {
		t.Fatalf("tool count = %d, want 4", len(tools))
	}
}

func TestReadArtifactToolReturnsStructuredContent(t *testing.T) {
	s := testServer()
	args := json.RawMessage(`{"case_id":"case_1","path":"REPORT.md","max_bytes":128}`)
	params, err := json.Marshal(toolCallParams{Name: "helios.read_artifact", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.callTool(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsError {
		t.Fatalf("tool returned error: %+v", got.Content)
	}
	if len(got.Content) != 1 || !strings.Contains(got.Content[0].Text, `"REPORT.md"`) {
		t.Fatalf("unexpected content: %+v", got.Content)
	}
	structured, ok := got.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content type = %T", got.StructuredContent)
	}
	artifact, ok := structured["artifact"].(*artifacts.ReadResult)
	if !ok || artifact.Path != "REPORT.md" || artifact.Text != "hello" {
		t.Fatalf("unexpected artifact structured content: %#v", structured["artifact"])
	}
}

func testServer() *Server {
	return &Server{
		Client: fakeClient{
			detail: &api.CaseDetailResponse{CaseSummary: api.CaseSummary{CaseID: "case_1", State: "done"}},
		},
		Artifacts: fakeArtifacts{},
	}
}

type fakeClient struct {
	detail *api.CaseDetailResponse
}

func (f fakeClient) ListCases(context.Context, heliosclient.ListOptions) (*api.CaseListResponse, error) {
	return &api.CaseListResponse{Items: []api.CaseSummary{f.detail.CaseSummary}, Total: 1, Limit: 50, Order: "created_at DESC"}, nil
}

func (f fakeClient) GetCase(context.Context, string) (*api.CaseDetailResponse, error) {
	return f.detail, nil
}

type fakeArtifacts struct{}

func (fakeArtifacts) AllowedPaths() []string {
	return []string{"REPORT.md", "RCA.md", "PoC.t.sol", "attack_flow.md", "multi_leg_reconciliation.md"}
}

func (fakeArtifacts) List(artifacts.CaseRef) ([]artifacts.FileInfo, error) {
	return []artifacts.FileInfo{{Path: "REPORT.md", Exists: true, Size: 5}}, nil
}

func (fakeArtifacts) Read(artifacts.CaseRef, string, int64) (*artifacts.ReadResult, error) {
	return &artifacts.ReadResult{Path: "REPORT.md", Size: 5, Text: "hello"}, nil
}
