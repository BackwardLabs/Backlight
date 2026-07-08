// Package artifacts reads a narrow, read-only subset of lumoskit output files.
package artifacts

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const DefaultMaxBytes int64 = 1 << 20           // 1 MiB
const DefaultECWExportMaxBytes int64 = 16 << 20 // 16 MiB

type artifactSpec struct {
	PublicPath string
	SourcePath string
}

var defaultArtifacts = []artifactSpec{
	{PublicPath: "REPORT.md", SourcePath: "report_bundle/report/REPORT.md"},
	{PublicPath: "RCA.md", SourcePath: "RCA.md"},
	{PublicPath: "PoC.t.sol", SourcePath: "report_bundle/poc/PoC.t.sol"},
	{PublicPath: "attack_flow.md", SourcePath: "artifacts/agent_poc/attack_flow.md"},
	{PublicPath: "multi_leg_reconciliation.md", SourcePath: "artifacts/agent_poc/multi_leg_reconciliation.md"},
	{PublicPath: "multi_leg_reconciliation.json", SourcePath: "artifacts/agent_poc/multi_leg_reconciliation.json"},
	{PublicPath: "report_bundle/README.md", SourcePath: "report_bundle/README.md"},
	{PublicPath: "report_bundle/manifest.json", SourcePath: "report_bundle/manifest.json"},
	{PublicPath: "report_bundle/report/REPORT.md", SourcePath: "report_bundle/report/REPORT.md"},
	{PublicPath: "report_bundle/report/RCA.md", SourcePath: "report_bundle/report/RCA.md"},
	{PublicPath: "report_bundle/report/report.json", SourcePath: "report_bundle/report/report.json"},
	{PublicPath: "report_bundle/report/run_summary.json", SourcePath: "report_bundle/report/run_summary.json"},
	{PublicPath: "report_bundle/poc/PoC.t.sol", SourcePath: "report_bundle/poc/PoC.t.sol"},
	{PublicPath: "report_bundle/poc/LumosPoCBase.sol", SourcePath: "report_bundle/poc/LumosPoCBase.sol"},
	{PublicPath: "report_bundle/evidence/asset_deltas.json", SourcePath: "report_bundle/evidence/asset_deltas.json"},
	{PublicPath: "report_bundle/evidence/fund_flows.json", SourcePath: "report_bundle/evidence/fund_flows.json"},
	{PublicPath: "report_bundle/visuals/asset_deltas.dot", SourcePath: "report_bundle/visuals/asset_deltas.dot"},
	{PublicPath: "report_bundle/visuals/fund_flows.dot", SourcePath: "report_bundle/visuals/fund_flows.dot"},
	{PublicPath: "x-feed-status.json", SourcePath: "x-feed-status.json"},
	{PublicPath: "x-feed-main-post.txt", SourcePath: "x-feed-main-post.txt"},
	{PublicPath: "x-feed-reply-post.txt", SourcePath: "x-feed-reply-post.txt"},
	{PublicPath: "x-feed-telegram-post.txt", SourcePath: "x-feed-telegram-post.txt"},
	{PublicPath: "x-feed-card-brief.md", SourcePath: "x-feed-card-brief.md"},
	{PublicPath: "x-feed-visuals/exploit-flow-card.svg", SourcePath: "x-feed-visuals/exploit-flow-card.svg"},
	{PublicPath: "artifacts/pre_lumos_result.json", SourcePath: "artifacts/pre_lumos_result.json"},
	{PublicPath: "artifacts/pre_lumos_result.md", SourcePath: "artifacts/pre_lumos_result.md"},
}

var ecwArtifacts = []artifactSpec{
	{PublicPath: "summary.md", SourcePath: "summary.md"},
	{PublicPath: "summary.json", SourcePath: "summary.json"},
	{PublicPath: "RCA.md", SourcePath: "RCA.md"},
	{PublicPath: "rca.md", SourcePath: "rca.md"},
	{PublicPath: "PoC.t.sol", SourcePath: "PoC.t.sol"},
	{PublicPath: "inputs/tx_metadata.json", SourcePath: "inputs/tx_metadata.json"},
	{PublicPath: "report_bundle/README.md", SourcePath: "report_bundle/README.md"},
	{PublicPath: "report_bundle/manifest.json", SourcePath: "report_bundle/manifest.json"},
	{PublicPath: "report_bundle/report/REPORT.md", SourcePath: "report_bundle/report/REPORT.md"},
	{PublicPath: "report_bundle/report/RCA.md", SourcePath: "report_bundle/report/RCA.md"},
	{PublicPath: "report_bundle/report/report.json", SourcePath: "report_bundle/report/report.json"},
	{PublicPath: "report_bundle/report/run_summary.json", SourcePath: "report_bundle/report/run_summary.json"},
	{PublicPath: "report_bundle/poc/PoC.t.sol", SourcePath: "report_bundle/poc/PoC.t.sol"},
	{PublicPath: "report_bundle/poc/LumosPoCBase.sol", SourcePath: "report_bundle/poc/LumosPoCBase.sol"},
	{PublicPath: "report_bundle/evidence/asset_deltas.json", SourcePath: "report_bundle/evidence/asset_deltas.json"},
	{PublicPath: "report_bundle/evidence/fund_flows.json", SourcePath: "report_bundle/evidence/fund_flows.json"},
	{PublicPath: "report_bundle/visuals/asset_deltas.dot", SourcePath: "report_bundle/visuals/asset_deltas.dot"},
	{PublicPath: "report_bundle/visuals/fund_flows.dot", SourcePath: "report_bundle/visuals/fund_flows.dot"},
	{PublicPath: "x-feed-status.json", SourcePath: "x-feed-status.json"},
	{PublicPath: "x-feed-main-post.txt", SourcePath: "x-feed-main-post.txt"},
	{PublicPath: "x-feed-reply-post.txt", SourcePath: "x-feed-reply-post.txt"},
	{PublicPath: "x-feed-telegram-post.txt", SourcePath: "x-feed-telegram-post.txt"},
	{PublicPath: "x-feed-card-brief.md", SourcePath: "x-feed-card-brief.md"},
	{PublicPath: "x-feed-visuals/exploit-flow-card.svg", SourcePath: "x-feed-visuals/exploit-flow-card.svg"},
	{PublicPath: "artifacts/pre_lumos_result.json", SourcePath: "artifacts/pre_lumos_result.json"},
	{PublicPath: "artifacts/pre_lumos_result.md", SourcePath: "artifacts/pre_lumos_result.md"},
	{PublicPath: "artifacts/agent_poc/attack_flow.md", SourcePath: "artifacts/agent_poc/attack_flow.md"},
	{PublicPath: "artifacts/agent_poc/multi_leg_reconciliation.md", SourcePath: "artifacts/agent_poc/multi_leg_reconciliation.md"},
	{PublicPath: "artifacts/agent_poc/multi_leg_reconciliation.json", SourcePath: "artifacts/agent_poc/multi_leg_reconciliation.json"},
	{PublicPath: "artifacts/agent_poc/result.json", SourcePath: "artifacts/agent_poc/result.json"},
	{PublicPath: "artifacts/agent_poc/summary.json", SourcePath: "artifacts/agent_poc/summary.json"},
	{PublicPath: "artifacts/agent_poc/foundry/foundry.toml", SourcePath: "artifacts/agent_poc/foundry/foundry.toml"},
	{PublicPath: "artifacts/agent_poc/foundry/test/PoC.t.sol", SourcePath: "artifacts/agent_poc/foundry/test/PoC.t.sol"},
	{PublicPath: "artifacts/agent_poc/foundry/test/LumosPoCBase.sol", SourcePath: "artifacts/agent_poc/foundry/test/LumosPoCBase.sol"},
	{PublicPath: "artifacts/agent_poc/foundry/lib/forge-std/src/Test.sol", SourcePath: "artifacts/agent_poc/foundry/lib/forge-std/src/Test.sol"},
	{PublicPath: "artifacts/poc_sketch/poc_sketch.sol", SourcePath: "artifacts/poc_sketch/poc_sketch.sol"},
	{PublicPath: "artifacts/poc_sketch/poc_context.json", SourcePath: "artifacts/poc_sketch/poc_context.json"},
	{PublicPath: "artifacts/poc_sketch/foundry_spec.json", SourcePath: "artifacts/poc_sketch/foundry_spec.json"},
	{PublicPath: "artifacts/poc_sketch/pseudo_test_plan.md", SourcePath: "artifacts/poc_sketch/pseudo_test_plan.md"},
	{PublicPath: "artifacts/rca/input/tx_metadata.json", SourcePath: "artifacts/rca/input/tx_metadata.json"},
	{PublicPath: "artifacts/rca/input/asset_deltas.json", SourcePath: "artifacts/rca/input/asset_deltas.json"},
	{PublicPath: "artifacts/rca/input/decompiled_code_context.json", SourcePath: "artifacts/rca/input/decompiled_code_context.json"},
	{PublicPath: "artifacts/rca/input/decompiled_pseudocode.md", SourcePath: "artifacts/rca/input/decompiled_pseudocode.md"},
	{PublicPath: "artifacts/rca/trace_read_model.json", SourcePath: "artifacts/rca/trace_read_model.json"},
	{PublicPath: "artifacts/rca/economic_frontier.json", SourcePath: "artifacts/rca/economic_frontier.json"},
	{PublicPath: "artifacts/rca/rca_frontier.json", SourcePath: "artifacts/rca/rca_frontier.json"},
	{PublicPath: "artifacts/rca/rpc_observations.json", SourcePath: "artifacts/rca/rpc_observations.json"},
	{PublicPath: "artifacts/rca/evidence_catalog.json", SourcePath: "artifacts/rca/evidence_catalog.json"},
	{PublicPath: "artifacts/rca/initial_context_pack.json", SourcePath: "artifacts/rca/initial_context_pack.json"},
	{PublicPath: "artifacts/rca/state_transition_index.json", SourcePath: "artifacts/rca/state_transition_index.json"},
	{PublicPath: "artifacts/rca/rca_retrieval_requests.json", SourcePath: "artifacts/rca/rca_retrieval_requests.json"},
	{PublicPath: "artifacts/rca/retrieval_pack.json", SourcePath: "artifacts/rca/retrieval_pack.json"},
	{PublicPath: "artifacts/rca/reasoning.md", SourcePath: "artifacts/rca/reasoning.md"},
	{PublicPath: "artifacts/rca/report.json", SourcePath: "artifacts/rca/report.json"},
	{PublicPath: "artifacts/rca/summary.json", SourcePath: "artifacts/rca/summary.json"},
	{PublicPath: "artifacts/rca/validation.json", SourcePath: "artifacts/rca/validation.json"},
	{PublicPath: "artifacts/rca/source_fetch_summary.json", SourcePath: "artifacts/rca/source_fetch_summary.json"},
	{PublicPath: "artifacts/asset_delta/asset_deltas.json", SourcePath: "artifacts/asset_delta/asset_deltas.json"},
	{PublicPath: "artifacts/asset_delta/accounting_notes.md", SourcePath: "artifacts/asset_delta/accounting_notes.md"},
	{PublicPath: "artifacts/flow_context/fund_flows.json", SourcePath: "artifacts/flow_context/fund_flows.json"},
	{PublicPath: "artifacts/enrich/selector_labels.json", SourcePath: "artifacts/enrich/selector_labels.json"},
	{PublicPath: "artifacts/localize/localized_call_graph.json", SourcePath: "artifacts/localize/localized_call_graph.json"},
	{PublicPath: "artifacts/semantic/pseudocode_compact.txt", SourcePath: "artifacts/semantic/pseudocode_compact.txt"},
}

// Reader enforces exact-path allowlisting and output-root containment.
type Reader struct {
	OutputBase string
	MaxBytes   int64
	Allowed    map[string]string
}

// CaseRef is the minimal case metadata needed to locate case artifacts.
type CaseRef struct {
	CaseID     string
	OutputRoot *string
}

// FileInfo describes one allowed artifact under a case output root.
type FileInfo struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Size   int64  `json:"size,omitempty"`
}

// ReadResult is returned by Read.
type ReadResult struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Text string `json:"text"`
}

func NewReader(outputBase string, maxBytes int64) (*Reader, error) {
	return newReader(outputBase, maxBytes, defaultArtifacts, DefaultMaxBytes)
}

func NewECWReader(outputBase string, maxBytes int64) (*Reader, error) {
	return newReader(outputBase, maxBytes, ecwArtifacts, DefaultECWExportMaxBytes)
}

func newReader(outputBase string, maxBytes int64, specs []artifactSpec, defaultMaxBytes int64) (*Reader, error) {
	if strings.TrimSpace(outputBase) == "" {
		return nil, fmt.Errorf("HELIOS_OUTPUT_BASE or HELIOS_OUTPUT_ROOT is required")
	}
	outputBase, err := filepath.Abs(outputBase)
	if err != nil {
		return nil, fmt.Errorf("resolve output base: %w", err)
	}
	outputBase = filepath.Clean(outputBase)
	if isFilesystemRoot(outputBase) {
		return nil, fmt.Errorf("output base must not be filesystem root")
	}
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	allowed := make(map[string]string, len(specs))
	for _, artifact := range specs {
		allowed[artifact.PublicPath] = artifact.SourcePath
	}
	return &Reader{OutputBase: outputBase, MaxBytes: maxBytes, Allowed: allowed}, nil
}

// AllowedPaths returns the exact artifact paths exposed by this reader.
func (r *Reader) AllowedPaths() []string {
	paths := make([]string, 0, len(r.Allowed))
	for p := range r.Allowed {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// List reports which allowlisted artifacts currently exist for a case.
func (r *Reader) List(c CaseRef) ([]FileInfo, error) {
	root, err := r.validOutputRoot(c)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(r.Allowed))
	for _, rel := range r.AllowedPaths() {
		info := FileInfo{Path: rel}
		path, err := r.safeFilePath(root, rel)
		if err == nil {
			if st, statErr := os.Stat(path); statErr == nil && !st.IsDir() {
				info.Exists = true
				info.Size = st.Size()
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// Read reads one exact allowlisted artifact for a case. perCallMax may lower the
// configured reader cap, but cannot raise it.
func (r *Reader) Read(c CaseRef, rel string, perCallMax int64) (*ReadResult, error) {
	root, err := r.validOutputRoot(c)
	if err != nil {
		return nil, err
	}
	path, err := r.safeFilePath(root, rel)
	if err != nil {
		return nil, err
	}
	maxBytes := r.MaxBytes
	if perCallMax > 0 && perCallMax < maxBytes {
		maxBytes = perCallMax
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("artifact %s exceeds max_bytes=%d", rel, maxBytes)
	}
	return &ReadResult{Path: rel, Size: int64(len(data)), Text: string(data)}, nil
}

func (r *Reader) validOutputRoot(c CaseRef) (string, error) {
	if c.OutputRoot == nil || strings.TrimSpace(*c.OutputRoot) == "" {
		return "", fmt.Errorf("case %s has no output_root", c.CaseID)
	}
	baseReal, err := filepath.EvalSymlinks(r.OutputBase)
	if err != nil {
		return "", fmt.Errorf("resolve HELIOS_OUTPUT_BASE: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(*c.OutputRoot)
	if err != nil {
		return "", fmt.Errorf("resolve case output_root: %w", err)
	}
	if !isWithin(baseReal, rootReal) {
		return "", fmt.Errorf("case output_root is outside HELIOS_OUTPUT_BASE")
	}
	return rootReal, nil
}

func (r *Reader) safeFilePath(root, rel string) (string, error) {
	sourceRel, ok := r.Allowed[rel]
	if !ok {
		return "", fmt.Errorf("artifact path %q is not allowlisted", rel)
	}
	if filepath.IsAbs(rel) || filepath.Clean(rel) != rel {
		return "", fmt.Errorf("artifact path %q is not a safe relative path", rel)
	}
	if filepath.IsAbs(sourceRel) || filepath.Clean(sourceRel) != sourceRel {
		return "", fmt.Errorf("artifact source path %q is not a safe relative path", sourceRel)
	}
	path := filepath.Join(root, sourceRel)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
		return "", err
	}
	if !isWithin(root, resolved) {
		return "", fmt.Errorf("artifact path escapes output_root")
	}
	resolvedRel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if filepath.Clean(resolvedRel) != sourceRel {
		return "", fmt.Errorf("artifact path %q resolves to non-allowlisted path", rel)
	}
	return resolved, nil
}

func isWithin(base, candidate string) bool {
	rel, err := filepath.Rel(base, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

func isFilesystemRoot(path string) bool {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	root := volume + string(filepath.Separator)
	return clean == root
}
