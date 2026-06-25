package worker

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/UPside-Lumos-V2/helios/internal/lumoskit"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

// drainSelectSubdir holds per-candidate cheap-prefix artifacts under the case
// output root. It is scratch for the selection decision, separate from the
// winner's full-RCA artifacts at the output-root top level.
const drainSelectSubdir = "_drain_select"

// candidateScore is the per-candidate economic summary recorded on the
// drain_tx_selected event for auditability.
type candidateScore struct {
	TxHash       string `json:"tx_hash"`
	ExitCode     int    `json:"exit_code"`
	NetFlowCount int    `json:"net_flow_count"`
	MaxAbsDelta  string `json:"max_abs_delta"`
}

type drainSelection struct {
	PrimaryTxHash string
	WinnerTxHash  string
	Ranking       []candidateScore
}

// candidateTxHashesFromMetadata pulls the optional candidate_tx_hashes list that
// hack-detector attaches when a single alert names more than one exploit tx.
func candidateTxHashesFromMetadata(metadata json.RawMessage) []string {
	var root map[string]json.RawMessage
	if len(metadata) == 0 || json.Unmarshal(metadata, &root) != nil {
		return nil
	}
	raw, ok := root["candidate_tx_hashes"]
	if !ok || len(raw) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	return list
}

// selectDrainTxIfNeeded runs drain selection for fresh multi-tx cases and, when
// the winner differs from the case's primary tx, re-points the case at it. It is
// a no-op for stage resumes (the tx is already chosen) and single-tx cases.
func (w *Worker) selectDrainTxIfNeeded(ctx context.Context, c *store.Case, runOptions lumoskit.RunOptions, log *slog.Logger) {
	if runOptions.Stage != "" {
		return // stage resume reuses the already-selected tx
	}
	if c.OutputRoot == nil {
		return
	}
	candidates := candidateTxHashesFromMetadata(c.Metadata)
	if len(candidates) < 2 {
		return
	}

	sel := w.selectDrainTx(ctx, c, candidates)
	if sel.WinnerTxHash != "" && sel.WinnerTxHash != c.TxHash {
		if err := w.Store.UpdateCaseTxHash(ctx, c.CaseID, sel.WinnerTxHash); err != nil {
			log.Warn("update case tx_hash to selected drain failed", "err", err, "selected", sel.WinnerTxHash)
		} else {
			log.Info("drain tx re-pointed",
				"from", sel.PrimaryTxHash, "to", sel.WinnerTxHash, "candidates", len(candidates))
			c.TxHash = sel.WinnerTxHash
		}
	}
	if err := w.Store.AppendCaseEvent(ctx, c.CaseID, "drain_tx_selected", map[string]any{
		"primary_tx_hash":  sel.PrimaryTxHash,
		"selected_tx_hash": sel.WinnerTxHash,
		"candidate_count":  len(candidates),
		"ranking":          sel.Ranking,
	}); err != nil {
		log.Warn("record drain_tx_selected event failed", "err", err)
	}
}

// selectDrainTx runs the cheap deterministic flow_context prefix
// (cefg→localize→lift→flow_context, no agent/LLM) on each candidate tx and picks
// the one whose net fund-flow movement is largest — the real asset-drain. A
// setup/authority tx moves ~no funds, so its net_flows are empty.
//
// Per ADR-0018, helios runs lumoskit only as a subprocess; it reads the
// resulting flow_context artifact and makes the routing decision itself (it does
// not perform analysis). The winner defaults to the primary tx, so a failed or
// movement-free selection never makes things worse than today's behaviour.
func (w *Worker) selectDrainTx(ctx context.Context, c *store.Case, candidates []string) drainSelection {
	sel := drainSelection{PrimaryTxHash: c.TxHash, WinnerTxHash: c.TxHash}

	type candResult struct {
		score  candidateScore
		maxAbs *big.Float
		ok     bool // prefix exited cleanly (ExitCode == 0)
	}
	results := make([]candResult, len(candidates))

	// Fan out the per-candidate cheap prefix concurrently. Each candidate writes
	// to an isolated output subdir (drainSelectSubdir/<tx>), so there is no
	// contention, and Runner holds no per-call state. Candidate count is capped
	// at maxCandidateTxHashes (3), so unbounded fan-out is bounded in practice.
	// Wall-clock drops from N×prefix to ~1×prefix.
	var wg sync.WaitGroup
	for i, tx := range candidates {
		wg.Add(1)
		go func(i int, tx string) {
			defer wg.Done()
			score := candidateScore{TxHash: tx, MaxAbsDelta: "0"}
			root := filepath.Join(*c.OutputRoot, drainSelectSubdir, txDirToken(tx))
			res := w.Runner.RunWithOptions(ctx, c.Chain, tx, root, lumoskit.RunOptions{Stage: "flow_context_select"})
			score.ExitCode = res.ExitCode
			maxAbs := new(big.Float)
			if res.ExitCode == 0 {
				count, m := readNetFlowMagnitude(root)
				score.NetFlowCount = count
				score.MaxAbsDelta = m.Text('f', 0)
				maxAbs = m
			}
			results[i] = candResult{score: score, maxAbs: maxAbs, ok: res.ExitCode == 0}
		}(i, tx)
	}
	wg.Wait()

	// Deterministic selection in candidate order (independent of completion
	// order): largest net movement wins, ties keep the earlier candidate.
	bestDelta := new(big.Float)
	bestFound := false
	for i := range candidates {
		r := results[i]
		sel.Ranking = append(sel.Ranking, r.score)
		if r.ok && (!bestFound || r.maxAbs.Cmp(bestDelta) > 0) {
			bestFound = true
			bestDelta = r.maxAbs
			sel.WinnerTxHash = candidates[i]
		}
	}
	return sel
}

func txDirToken(tx string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(tx)), "0x")
}

// readNetFlowMagnitude reads <root>/artifacts/flow_context/fund_flows.json and
// returns the number of net-flow entries and the largest absolute per-holder
// delta. This is a coarse drain-vs-setup discriminator, not a USD figure — the
// precise loss comes from the full RCA on the winner.
func readNetFlowMagnitude(outputRoot string) (count int, maxAbsDelta *big.Float) {
	maxAbsDelta = new(big.Float)
	path := filepath.Join(outputRoot, "artifacts", "flow_context", "fund_flows.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, maxAbsDelta
	}
	var doc struct {
		NetFlows []struct {
			Delta string `json:"delta"`
		} `json:"net_flows"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return 0, maxAbsDelta
	}
	for _, nf := range doc.NetFlows {
		abs := strings.TrimPrefix(strings.TrimSpace(nf.Delta), "-")
		v, ok := new(big.Float).SetString(abs)
		if !ok {
			continue
		}
		count++
		if v.Cmp(maxAbsDelta) > 0 {
			maxAbsDelta = v
		}
	}
	return count, maxAbsDelta
}
