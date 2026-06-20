package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/UPside-Lumos-V2/helios/internal/lumoskit"
	"github.com/UPside-Lumos-V2/helios/internal/outcome"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

type autoRerunResumeMetadata struct {
	Reason         string `json:"reason"`
	ResumeStage    string `json:"resume_stage"`
	Decision       string `json:"decision"`
	RepairStrategy string `json:"repair_strategy"`
	SourceCaseID   string `json:"source_case_id"`
}

func autoRerunResumeStage(mapped outcome.Result) string {
	switch mapped.AnalysisStage {
	case outcome.AnalysisStageReachablePoC:
		return "agent_poc_repair"
	case outcome.AnalysisStageRCABlocked:
		return "rca"
	case outcome.AnalysisStagePoCBlocked:
		return "agent_poc"
	default:
		return "all"
	}
}

func (w *Worker) prepareResume(ctx context.Context, c *store.Case) (lumoskit.RunOptions, map[string]any, error) {
	meta := autoRerunResumeFromMetadata(c.Metadata)
	stage := normalizeResumeStage(meta.ResumeStage)
	if stage == "" || stage == "all" {
		return lumoskit.RunOptions{}, nil, nil
	}
	if c.OutputRoot == nil || *c.OutputRoot == "" {
		return lumoskit.RunOptions{}, nil, fmt.Errorf("resume case has no output_root")
	}

	sourceCaseID := meta.SourceCaseID
	if sourceCaseID == "" && c.ParentCaseID != nil {
		sourceCaseID = *c.ParentCaseID
	}
	if sourceCaseID == "" {
		return lumoskit.RunOptions{}, nil, fmt.Errorf("resume case has no source case")
	}
	source, err := w.Store.GetCase(ctx, sourceCaseID)
	if err != nil {
		return lumoskit.RunOptions{}, nil, fmt.Errorf("load resume source case: %w", err)
	}
	if source == nil || source.OutputRoot == nil || *source.OutputRoot == "" {
		return lumoskit.RunOptions{}, nil, fmt.Errorf("resume source case has no output_root")
	}
	if err := prepareResumeOutput(*source.OutputRoot, *c.OutputRoot, stage); err != nil {
		return lumoskit.RunOptions{}, nil, err
	}

	payload := map[string]any{
		"resume_stage":              stage,
		"resume_source_case_id":     source.CaseID,
		"resume_source_output_root": *source.OutputRoot,
	}
	if meta.Reason != "" {
		payload["resume_reason"] = meta.Reason
	}
	if meta.Decision != "" {
		payload["resume_decision"] = meta.Decision
	}
	if meta.RepairStrategy != "" {
		payload["resume_repair_strategy"] = meta.RepairStrategy
	}
	return lumoskit.RunOptions{Stage: stage}, payload, nil
}

func autoRerunResumeFromMetadata(metadata json.RawMessage) autoRerunResumeMetadata {
	var root map[string]json.RawMessage
	if len(metadata) == 0 || json.Unmarshal(metadata, &root) != nil {
		return autoRerunResumeMetadata{}
	}
	raw := root[store.AutoRerunMetadataKey]
	if len(raw) == 0 {
		return autoRerunResumeMetadata{}
	}
	var meta autoRerunResumeMetadata
	_ = json.Unmarshal(raw, &meta)
	return meta
}

func normalizeResumeStage(stage string) string {
	switch strings.TrimSpace(stage) {
	case "", "all", "pipeline":
		return "all"
	case "poc", "agent_poc":
		return "agent_poc"
	case "agent_poc_repair", "poc_repair", "economic_proof_repair":
		return "agent_poc_repair"
	case "rca":
		return "rca"
	default:
		return ""
	}
}

func prepareResumeOutput(sourceRoot, destRoot, stage string) error {
	if filepath.Clean(sourceRoot) == filepath.Clean(destRoot) {
		return nil
	}
	if err := copyDir(sourceRoot, destRoot); err != nil {
		return fmt.Errorf("copy resume artifacts: %w", err)
	}
	if stage == "agent_poc_repair" {
		if err := preserveAgentPoCRepairPriorProductSummary(destRoot); err != nil {
			return fmt.Errorf("preserve prior product summary: %w", err)
		}
	}
	if err := pruneResumeOutputs(destRoot, stage); err != nil {
		return fmt.Errorf("prune stale resume artifacts: %w", err)
	}
	return nil
}

func preserveAgentPoCRepairPriorProductSummary(root string) error {
	candidates := []string{
		filepath.Join("report_bundle", "report", "run_summary.json"),
		"summary.json",
	}
	dest := filepath.Join(root, "artifacts", "agent_poc", "prior_product_summary.json")
	for _, rel := range candidates {
		source := filepath.Join(root, rel)
		info, err := os.Stat(source)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return copyFile(source, dest, info.Mode().Perm())
	}
	return nil
}

func copyDir(sourceRoot, destRoot string) error {
	info, err := os.Stat(sourceRoot)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source root is not a directory: %s", sourceRoot)
	}
	return filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(destRoot, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		target := filepath.Join(destRoot, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(source, dest string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func pruneResumeOutputs(root, stage string) error {
	paths := []string{
		"summary.json",
		"summary.md",
		"Report.md",
		"RCA.md",
		"rca.md",
		filepath.Join("report_bundle", "report"),
	}
	switch stage {
	case "agent_poc":
		paths = append(paths,
			"PoC.t.sol",
			filepath.Join("artifacts", "agent_poc"),
			filepath.Join("artifacts", "rca"),
			filepath.Join("report_bundle", "poc"),
		)
	case "agent_poc_repair":
		paths = append(paths,
			"PoC.t.sol",
			filepath.Join("artifacts", "rca"),
			filepath.Join("report_bundle", "poc"),
		)
	case "rca":
		paths = append(paths, filepath.Join("artifacts", "rca"))
	}
	for _, rel := range paths {
		if err := os.RemoveAll(filepath.Join(root, rel)); err != nil {
			return err
		}
	}
	return nil
}
