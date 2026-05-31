package githubpublish

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildTargetSpecUsesReportProtocolAndMonth(t *testing.T) {
	root := writeProductArtifacts(t, `# yETH Incident Report

Protocol: yETH
Date: 2026-01-25
`, `{"status":"pass","poc":{"status":"verified"}}`)

	spec, err := buildTargetSpec(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Month != "2026-01" || spec.Protocol != "yETH" {
		t.Fatalf("target spec = month %q protocol %q", spec.Month, spec.Protocol)
	}
	want := []string{
		"test/2026-01/yETH/yETH.t.sol",
		"test/2026-01/yETH/README.md",
	}
	for i, path := range want {
		if spec.Files[i].Path != path {
			t.Fatalf("target path[%d] = %q, want %q", i, spec.Files[i].Path, path)
		}
	}
}

func TestBuildTargetSpecUsesReportBundleArtifacts(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "report_bundle", "poc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "report_bundle", "report"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "poc", "PoC.t.sol"), []byte("contract BundlePoC {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "report", "REPORT.md"), []byte(`# Bundle Protocol Incident Report

Protocol: Bundle Protocol
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "report", "run_summary.json"), []byte(`{"status":"pass","poc":{"status":"verified"},"incident":{"timestamp":"2026-02-14T00:00:00Z"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	spec, err := buildTargetSpec(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Month != "2026-02" || spec.Protocol != "Bundle-Protocol" {
		t.Fatalf("target spec = month %q protocol %q", spec.Month, spec.Protocol)
	}
	if string(spec.Files[0].Content) != "contract BundlePoC {}\n" {
		t.Fatalf("unexpected PoC content: %q", string(spec.Files[0].Content))
	}
}

func TestBuildTargetSpecPrefersIncidentSlugAndTransactionTimestamp(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "report_bundle", "poc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "report_bundle", "report"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "poc", "PoC.t.sol"), []byte("contract FPCPoC {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "report", "REPORT.md"), []byte(`# LumosKit Run Report — bsc 0x3a9dd216…9f5937

- **Finding**: FPC transfer hook mutates and syncs its own AMM pair reserves during pool transfers
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report_bundle", "report", "run_summary.json"), []byte(`{"created_at_unix_ms":1779811222022,"orchestrator_wall_time":1235.19,"tx_timestamp":1751466319}`), 0o644); err != nil {
		t.Fatal(err)
	}

	spec, err := buildTargetSpec(root, "260526_bsc_fpc")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Month != "2025-07" || spec.Protocol != "fpc" {
		t.Fatalf("target spec = month %q protocol %q", spec.Month, spec.Protocol)
	}
	want := []string{
		"test/2025-07/fpc/fpc.t.sol",
		"test/2025-07/fpc/README.md",
	}
	for i, path := range want {
		if spec.Files[i].Path != path {
			t.Fatalf("target path[%d] = %q, want %q", i, spec.Files[i].Path, path)
		}
	}
}

func TestBuildTargetSpecSkipsGenericLumosProtocol(t *testing.T) {
	root := writeProductArtifacts(t, `# LumosKit Run Report

Protocol: LumosKit Run
Date: 2026-05-26
`, `{"status":"pass","poc":{"status":"verified"}}`)

	_, err := buildTargetSpec(root, "")
	if !errors.Is(err, errSkipPublish) {
		t.Fatalf("buildTargetSpec error = %v, want skip publish", err)
	}
}

func TestBuildTargetSpecSkipsMissingIncidentMonth(t *testing.T) {
	root := writeProductArtifacts(t, `# yETH Incident Report

Protocol: yETH
`, `{"status":"pass","poc":{"status":"verified"}}`)

	_, err := buildTargetSpec(root, "")
	if !errors.Is(err, errSkipPublish) {
		t.Fatalf("buildTargetSpec error = %v, want skip publish", err)
	}
}

func TestPublishSkipsGenericLumosKitRunWithoutGitHubRequest(t *testing.T) {
	root := writeProductArtifacts(t, `# LumosKit Run Report — bsc 0x3a9dd216…9f5937

- **Finding**: generic generated report heading
`, `{"status":"pass","poc":{"status":"verified"},"tx_timestamp":1779811222}`)
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "unexpected github request", http.StatusInternalServerError)
	}))
	defer server.Close()

	pub := New(Config{Token: "test-token", APIBase: server.URL})
	pub.Client = server.Client()
	res, err := pub.Publish(context.Background(), Case{CaseID: "case_123", OutputRoot: root, IncidentSlug: "260526_bsc_unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("GitHub API was called for a generic LumosKit run target")
	}
	if res == nil || res.Published || res.SkipReason == "" {
		t.Fatalf("publish result = %+v, want skipped result", res)
	}
}

func TestBuildTargetSpecRequiresReport(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "PoC.t.sol"), []byte("contract PoC {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := buildTargetSpec(root, ""); err == nil || !strings.Contains(err.Error(), "Report.md is required") {
		t.Fatalf("buildTargetSpec error = %v, want Report.md requirement", err)
	}
}

func TestPublishCreatesSingleCommitForPocAndReadme(t *testing.T) {
	root := writeProductArtifacts(t, `# yETH Incident Report

Protocol: yETH
Date: 2026-01-25
`, `{"status":"pass","poc":{"status":"verified"}}`)

	var treeReq struct {
		BaseTree string      `json:"base_tree"`
		Tree     []treeEntry `json:"tree"`
	}
	var commitReq struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}
	var updateReq struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}
	blobCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/UPside-Lumos-V2/Q1-2026/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"base-commit"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/UPside-Lumos-V2/Q1-2026/git/commits/base-commit":
			_, _ = w.Write([]byte(`{"sha":"base-commit","tree":{"sha":"base-tree"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/UPside-Lumos-V2/Q1-2026/git/blobs":
			blobCount++
			_, _ = w.Write([]byte(`{"sha":"blob-` + string(rune('0'+blobCount)) + `"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/UPside-Lumos-V2/Q1-2026/git/trees":
			if err := json.NewDecoder(r.Body).Decode(&treeReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"sha":"next-tree"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/UPside-Lumos-V2/Q1-2026/git/commits":
			if err := json.NewDecoder(r.Body).Decode(&commitReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"sha":"next-commit","tree":{"sha":"next-tree"}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/UPside-Lumos-V2/Q1-2026/git/refs/heads/main":
			if err := json.NewDecoder(r.Body).Decode(&updateReq); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"ref":"refs/heads/main"}`))
		default:
			t.Fatalf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	pub := New(Config{Token: "test-token", APIBase: server.URL})
	pub.Client = server.Client()
	result, err := pub.Publish(context.Background(), Case{CaseID: "case_123", OutputRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Published || result.CommitSHA != "next-commit" || result.TargetDir != "test/2026-01/yETH" {
		t.Fatalf("publish result = %+v", result)
	}
	if result.PoCURL != "https://github.com/UPside-Lumos-V2/Q1-2026/blob/main/test/2026-01/yETH/yETH.t.sol" {
		t.Fatalf("poc url = %q", result.PoCURL)
	}
	if result.ReportURL != "https://github.com/UPside-Lumos-V2/Q1-2026/blob/main/test/2026-01/yETH/README.md" {
		t.Fatalf("report url = %q", result.ReportURL)
	}
	if result.CommitURL != "https://github.com/UPside-Lumos-V2/Q1-2026/commit/next-commit" {
		t.Fatalf("commit url = %q", result.CommitURL)
	}
	if treeReq.BaseTree != "base-tree" {
		t.Fatalf("base tree = %q", treeReq.BaseTree)
	}
	gotPaths := []string{treeReq.Tree[0].Path, treeReq.Tree[1].Path}
	wantPaths := []string{"test/2026-01/yETH/yETH.t.sol", "test/2026-01/yETH/README.md"}
	if strings.Join(gotPaths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("tree paths = %v, want %v", gotPaths, wantPaths)
	}
	if commitReq.Tree != "next-tree" || len(commitReq.Parents) != 1 || commitReq.Parents[0] != "base-commit" {
		t.Fatalf("commit request = %+v", commitReq)
	}
	if !strings.Contains(commitReq.Message, "Publish verified yETH incident artifact bundle") {
		t.Fatalf("commit message = %q", commitReq.Message)
	}
	if updateReq.SHA != "next-commit" || updateReq.Force {
		t.Fatalf("update ref request = %+v", updateReq)
	}
}

func writeProductArtifacts(t *testing.T, report, summary string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "PoC.t.sol"), []byte("contract PoC {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Report.md"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "summary.json"), []byte(summary), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
