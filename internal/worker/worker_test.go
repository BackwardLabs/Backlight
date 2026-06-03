package worker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/githubpublish"
	"github.com/UPside-Lumos-V2/helios/internal/lumoskit"
	"github.com/UPside-Lumos-V2/helios/internal/outcome"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

func TestWriteSignalContextIncludesCaseMetadata(t *testing.T) {
	outputRoot := t.TempDir()
	source := "hack-detector:twitter:TenArmorAlert"
	detectedAt := "2026-05-18T03:58:43Z"
	c := &store.Case{
		CaseID:     "case_260518_arb_sea_a01_55555555_abcd",
		Chain:      "arbitrum",
		TxHash:     "0x" + strings.Repeat("5", 64),
		Source:     &source,
		DetectedAt: &detectedAt,
		Metadata:   json.RawMessage(`{"protocol_name":"SEA","lumos_signal_id":"sig-1"}`),
		OutputRoot: &outputRoot,
	}

	if err := writeSignalContext(c); err != nil {
		t.Fatalf("write signal context: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outputRoot, "helios_signal_context.json"))
	if err != nil {
		t.Fatalf("read signal context: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode signal context: %v", err)
	}
	if got["schema"] != "helios-signal-context-v1" || got["case_id"] != c.CaseID || got["source"] != source {
		t.Fatalf("signal context core fields = %#v", got)
	}
	metadata := got["metadata"].(map[string]any)
	if metadata["protocol_name"] != "SEA" || metadata["lumos_signal_id"] != "sig-1" {
		t.Fatalf("signal context metadata = %#v", metadata)
	}
}

func TestWorkerPublishesVerifiedProductArtifacts(t *testing.T) {
	ctx := context.Background()
	outputParent := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "ethereum", "0x"+strings.Repeat("1", 64), nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err = st.ClaimNextQueued(ctx, outputParent)
	if err != nil {
		t.Fatal(err)
	}

	github := newFakeGitHub(t)
	defer github.server.Close()

	publisher := githubpublish.New(githubpublish.Config{
		Token:   "test-token",
		APIBase: github.server.URL,
	})
	publisher.Client = github.server.Client()
	w := &Worker{
		Store:           st,
		Runner:          &lumoskit.Runner{Binary: writePublishLumoskit(t, t.TempDir())},
		GitHubPublisher: publisher,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	w.process(ctx, c)
	w.Wait()

	github.mu.Lock()
	defer github.mu.Unlock()
	if github.updateSHA != "next-commit" {
		t.Fatalf("github ref update sha = %q, want next-commit", github.updateSHA)
	}
	gotPaths := []string{github.treePaths[0], github.treePaths[1]}
	wantPaths := []string{"test/2026-01/yETH/yETH.t.sol", "test/2026-01/yETH/README.md"}
	if strings.Join(gotPaths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("published paths = %v, want %v", gotPaths, wantPaths)
	}
	events, err := st.CaseEvents(ctx, c.CaseID)
	if err != nil {
		t.Fatal(err)
	}
	var sawPublish, sawDuration bool
	for _, event := range events {
		var payload map[string]any
		if len(event.Payload) > 0 {
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("unmarshal event %s payload: %v", event.EventType, err)
			}
		}
		if event.EventType == "state_transition" && event.FromState != nil && *event.FromState == store.StateRunning && event.ToState != nil && *event.ToState == store.StateDone {
			if _, ok := payload["duration_ms"]; ok {
				sawDuration = true
			}
		}
		if event.EventType == "github_publish" {
			sawPublish = true
			if payload["published"] != true || payload["commit_sha"] != "next-commit" {
				t.Fatalf("github publish payload = %#v", payload)
			}
			if !strings.Contains(payload["poc_url"].(string), "test/2026-01/yETH/yETH.t.sol") {
				t.Fatalf("github publish poc_url = %#v", payload["poc_url"])
			}
			if !strings.Contains(payload["report_url"].(string), "test/2026-01/yETH/README.md") {
				t.Fatalf("github publish report_url = %#v", payload["report_url"])
			}
			if !strings.Contains(payload["commit_url"].(string), "/commit/next-commit") {
				t.Fatalf("github publish commit_url = %#v", payload["commit_url"])
			}
		}
	}
	if !sawDuration {
		t.Fatalf("running done state_transition did not include duration_ms: %#v", events)
	}
	if !sawPublish {
		t.Fatalf("github_publish event not found: %#v", events)
	}
}

func TestWorkerPublishesPartialProductArtifacts(t *testing.T) {
	ctx := context.Background()
	outputParent := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "ethereum", "0x"+strings.Repeat("4", 64), nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err = st.ClaimNextQueued(ctx, outputParent)
	if err != nil {
		t.Fatal(err)
	}

	github := newFakeGitHub(t)
	defer github.server.Close()

	publisher := githubpublish.New(githubpublish.Config{
		Token:   "test-token",
		APIBase: github.server.URL,
	})
	publisher.Client = github.server.Client()
	w := &Worker{
		Store:           st,
		Runner:          &lumoskit.Runner{Binary: writePartialPublishLumoskit(t, t.TempDir())},
		GitHubPublisher: publisher,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	w.process(ctx, c)
	w.Wait()

	github.mu.Lock()
	updateSHA := github.updateSHA
	treePaths := append([]string(nil), github.treePaths...)
	github.mu.Unlock()
	if updateSHA != "next-commit" {
		t.Fatalf("github ref update sha = %q, want next-commit", updateSHA)
	}
	wantPaths := []string{"test/2026-01/yETH/yETH.t.sol", "test/2026-01/yETH/README.md"}
	if strings.Join(treePaths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("published paths = %v, want %v", treePaths, wantPaths)
	}

	updated, err := st.GetCase(ctx, c.CaseID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Outcome == nil || *updated.Outcome != "partial" {
		t.Fatalf("case outcome = %+v, want partial", updated)
	}
	events, err := st.CaseEvents(ctx, c.CaseID)
	if err != nil {
		t.Fatal(err)
	}
	var sawPublish bool
	for _, event := range events {
		if event.EventType != "github_publish" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("unmarshal github_publish payload: %v", err)
		}
		if payload["published"] == true && payload["outcome"] == "partial" {
			sawPublish = true
		}
	}
	if !sawPublish {
		t.Fatalf("partial github_publish event not found: %#v", events)
	}
}

func TestWorkerSkipsGenericGitHubPublishWithoutFailingCase(t *testing.T) {
	ctx := context.Background()
	outputParent := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "bsc", "0x"+strings.Repeat("3", 64), nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err = st.ClaimNextQueued(ctx, outputParent)
	if err != nil {
		t.Fatal(err)
	}

	github := newFakeGitHub(t)
	defer github.server.Close()
	publisher := githubpublish.New(githubpublish.Config{
		Token:   "test-token",
		APIBase: github.server.URL,
	})
	publisher.Client = github.server.Client()
	w := &Worker{
		Store:           st,
		Runner:          &lumoskit.Runner{Binary: writeGenericPublishLumoskit(t, t.TempDir())},
		GitHubPublisher: publisher,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	w.process(ctx, c)
	w.Wait()

	github.mu.Lock()
	requests := github.requests
	github.mu.Unlock()
	if requests != 0 {
		t.Fatalf("GitHub requests = %d, want 0 for skipped generic publish", requests)
	}
	updated, err := st.GetCase(ctx, c.CaseID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Outcome == nil || *updated.Outcome != "verified" || updated.FailureKind != nil {
		t.Fatalf("case after skipped publish = %+v", updated)
	}
	events, err := st.CaseEvents(ctx, c.CaseID)
	if err != nil {
		t.Fatal(err)
	}
	var sawSkip bool
	for _, event := range events {
		if event.EventType == "github_publish_failed" {
			t.Fatalf("unexpected github publish failure event: %+v", event)
		}
		if event.EventType != "github_publish" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("unmarshal github_publish payload: %v", err)
		}
		if payload["published"] == false && payload["skipped"] == true && payload["skip_reason"] != "" {
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Fatalf("github publish skip event not found: %#v", events)
	}
}

func TestWorkerAutoRerunsPartialBeforePublishOrHandoff(t *testing.T) {
	ctx := context.Background()
	outputParent := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "ethereum", "0x"+strings.Repeat("2", 64), nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err = st.ClaimNextQueued(ctx, outputParent)
	if err != nil {
		t.Fatal(err)
	}

	w := &Worker{
		Store:                       st,
		Runner:                      &lumoskit.Runner{Binary: writePartialLumoskit(t, t.TempDir())},
		PartialAutoRerunMaxAttempts: 3,
		Logger:                      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	w.process(ctx, c)

	parent, err := st.GetCase(ctx, c.CaseID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.State != store.StateHandedOff || parent.HandoffStatus != "skipped" || parent.Outcome == nil || *parent.Outcome != "partial" {
		t.Fatalf("parent after auto rerun = %+v", parent)
	}
	payload := terminalStatePayload(t, ctx, st, c.CaseID)
	if payload["analysis_stage"] != outcome.AnalysisStagePoCBlocked {
		t.Fatalf("analysis_stage = %v, want %s", payload["analysis_stage"], outcome.AnalysisStagePoCBlocked)
	}
	if payload["rerun_decision"] != outcome.RerunDecisionAutoRerun {
		t.Fatalf("rerun_decision = %v, want %s", payload["rerun_decision"], outcome.RerunDecisionAutoRerun)
	}
	if payload["auto_rerun_eligible"] != true {
		t.Fatalf("auto_rerun_eligible = %v, want true", payload["auto_rerun_eligible"])
	}
	items, total, err := st.ListCases(ctx, store.CaseListFilter{TxHash: &c.TxHash, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total cases = %d, want 2; items=%+v", total, items)
	}
	var child *store.Case
	for i := range items {
		if items[i].ParentCaseID != nil && *items[i].ParentCaseID == c.CaseID {
			child = &items[i]
		}
	}
	if child == nil || child.State != store.StateQueued || child.AttemptNumber != 2 {
		t.Fatalf("auto rerun child = %+v", child)
	}
	auto := autoRerunMetadata(t, child)
	if auto["resume_stage"] != "agent_poc" {
		t.Fatalf("child resume_stage = %v, want agent_poc; metadata=%#v", auto["resume_stage"], auto)
	}
}

func TestWorkerStageResumesRCABlockedChild(t *testing.T) {
	ctx := context.Background()
	outputParent := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "ethereum", "0x"+strings.Repeat("7", 64), nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err = st.ClaimNextQueued(ctx, outputParent)
	if err != nil {
		t.Fatal(err)
	}

	w := &Worker{
		Store:                       st,
		Runner:                      &lumoskit.Runner{Binary: writeRCABlockedThenStageAwareLumoskit(t, t.TempDir())},
		PartialAutoRerunMaxAttempts: 3,
		Logger:                      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	w.process(ctx, c)

	items, _, err := st.ListCases(ctx, store.CaseListFilter{TxHash: &c.TxHash, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var queuedChild *store.Case
	for i := range items {
		if items[i].ParentCaseID != nil && *items[i].ParentCaseID == c.CaseID {
			queuedChild = &items[i]
		}
	}
	if queuedChild == nil {
		t.Fatal("auto-rerun child not found")
	}
	auto := autoRerunMetadata(t, queuedChild)
	if auto["resume_stage"] != "rca" {
		t.Fatalf("child resume_stage = %v, want rca; metadata=%#v", auto["resume_stage"], auto)
	}

	child, err := st.ClaimNextQueued(ctx, outputParent)
	if err != nil {
		t.Fatal(err)
	}
	w.process(ctx, child)

	updated, err := st.GetCase(ctx, child.CaseID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Outcome == nil || *updated.Outcome != outcome.OutcomeVerified {
		t.Fatalf("child outcome = %+v, want verified", updated)
	}
	payload := terminalStatePayload(t, ctx, st, child.CaseID)
	if payload["lumoskit_stage"] != "rca" || payload["resume_stage"] != "rca" {
		t.Fatalf("child resume payload = %#v", payload)
	}
	stages := strings.TrimSpace(mustRead(t, filepath.Join(*child.OutputRoot, "stages.txt")))
	if !strings.HasSuffix(stages, "rca") {
		t.Fatalf("child stages = %q, want rca suffix", stages)
	}
}

func TestWorkerDoesNotAutoRerunPoCFailed(t *testing.T) {
	ctx := context.Background()
	outputParent := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "ethereum", "0x"+strings.Repeat("6", 64), nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err = st.ClaimNextQueued(ctx, outputParent)
	if err != nil {
		t.Fatal(err)
	}

	w := &Worker{
		Store:                       st,
		Runner:                      &lumoskit.Runner{Binary: writePoCFailedLumoskit(t, t.TempDir())},
		PartialAutoRerunMaxAttempts: 3,
		Logger:                      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	w.process(ctx, c)

	updated, err := st.GetCase(ctx, c.CaseID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Outcome == nil || *updated.Outcome != outcome.OutcomeUnverified {
		t.Fatalf("case outcome = %+v, want unverified", updated)
	}
	payload := terminalStatePayload(t, ctx, st, c.CaseID)
	if payload["analysis_stage"] != outcome.AnalysisStagePoCFailed {
		t.Fatalf("analysis_stage = %v, want %s", payload["analysis_stage"], outcome.AnalysisStagePoCFailed)
	}
	if payload["rerun_decision"] != outcome.RerunDecisionManualReview {
		t.Fatalf("rerun_decision = %v, want %s", payload["rerun_decision"], outcome.RerunDecisionManualReview)
	}
	items, total, err := st.ListCases(ctx, store.CaseListFilter{TxHash: &c.TxHash, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("total cases = %d, want no auto-rerun child; items=%+v", total, items)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func autoRerunMetadata(t *testing.T, c *store.Case) map[string]any {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal(c.Metadata, &metadata); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	auto, ok := metadata[store.AutoRerunMetadataKey].(map[string]any)
	if !ok {
		t.Fatalf("missing %s metadata: %#v", store.AutoRerunMetadataKey, metadata)
	}
	return auto
}

func terminalStatePayload(t *testing.T, ctx context.Context, st *store.Store, caseID string) map[string]any {
	t.Helper()
	events, err := st.CaseEvents(ctx, caseID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventType != "state_transition" || event.FromState == nil || event.ToState == nil {
			continue
		}
		if *event.FromState != store.StateRunning || *event.ToState != store.StateDone {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("unmarshal terminal payload: %v", err)
		}
		return payload
	}
	t.Fatalf("terminal running->done payload not found for %s: %#v", caseID, events)
	return nil
}

type fakeGitHub struct {
	server    *httptest.Server
	mu        sync.Mutex
	requests  int
	treePaths []string
	updateSHA string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{}
	blobCount := 0
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		f.mu.Unlock()
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
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
			var req struct {
				Tree []struct {
					Path string `json:"path"`
				} `json:"tree"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.treePaths = nil
			for _, entry := range req.Tree {
				f.treePaths = append(f.treePaths, entry.Path)
			}
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"sha":"next-tree"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/UPside-Lumos-V2/Q1-2026/git/commits":
			_, _ = w.Write([]byte(`{"sha":"next-commit","tree":{"sha":"next-tree"}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/UPside-Lumos-V2/Q1-2026/git/refs/heads/main":
			var req struct {
				SHA string `json:"sha"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.updateSHA = req.SHA
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"ref":"refs/heads/main"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return f
}

func writePublishLumoskit(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-lumoskit")
	script := `#!/bin/sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-root) out="$2"; shift 2 ;;
    --tx) shift 2 ;;
    --chain) shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$out"
cat > "$out/summary.json" <<'JSON'
{"status":"pass","poc":{"status":"verified"}}
JSON
printf '%s\n' '// SPDX-License-Identifier: UNLICENSED' 'contract PoC {}' > "$out/PoC.t.sol"
printf '%s\n' '# yETH Incident Report' 'Protocol: yETH' 'Date: 2026-01-25' > "$out/Report.md"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writePartialPublishLumoskit(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-partial-publish-lumoskit")
	script := `#!/bin/sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-root) out="$2"; shift 2 ;;
    --tx) shift 2 ;;
    --chain) shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$out"
cat > "$out/summary.json" <<'JSON'
{"status":"partial","poc":{"status":"verified","proof_kind":"economic_proof"},"rca":{"status":"partial","blocker_code":"missing_assumption"}}
JSON
printf '%s\n' '// SPDX-License-Identifier: UNLICENSED' 'contract PoC {}' > "$out/PoC.t.sol"
printf '%s\n' '# yETH Incident Report' 'Protocol: yETH' 'Date: 2026-01-25' > "$out/Report.md"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeGenericPublishLumoskit(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-generic-lumoskit")
	script := `#!/bin/sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-root) out="$2"; shift 2 ;;
    --tx) shift 2 ;;
    --chain) shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$out"
cat > "$out/summary.json" <<'JSON'
{"status":"pass","poc":{"status":"verified"},"tx_timestamp":1779811222}
JSON
printf '%s\n' '// SPDX-License-Identifier: UNLICENSED' 'contract PoC {}' > "$out/PoC.t.sol"
printf '%s\n' '# LumosKit Run Report — bsc 0x33333333…33333333' '- **Finding**: generic generated report heading' > "$out/Report.md"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writePartialLumoskit(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-partial-lumoskit")
	script := `#!/bin/sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-root) out="$2"; shift 2 ;;
    --tx) shift 2 ;;
    --chain) shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$out"
cat > "$out/summary.json" <<'JSON'
{"status":"partial","poc":{"status":"unverified","proof_kind":"reachability_only"},"rca":{"status":"blocked","blocker_code":"economic_proof_gap"}}
JSON
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeRCABlockedThenStageAwareLumoskit(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-rca-stage-lumoskit")
	script := `#!/bin/sh
set -eu
out=""
stage="all"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --stage) stage="$2"; shift 2 ;;
    --output-root) out="$2"; shift 2 ;;
    --tx) shift 2 ;;
    --chain) shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$out/report_bundle/report"
printf '%s\n' "$stage" >> "$out/stages.txt"
if [ "$stage" = "rca" ]; then
  test -f "$out/artifacts/agent_poc/result.json"
  mkdir -p "$out/artifacts/rca"
  cat > "$out/report_bundle/report/run_summary.json" <<'JSON'
{"status":"pass","poc":{"status":"verified"},"rca":{"status":"complete"}}
JSON
  exit 0
fi
mkdir -p "$out/artifacts/agent_poc"
cat > "$out/artifacts/agent_poc/result.json" <<'JSON'
{"status":"pass","poc_status":"pass"}
JSON
cat > "$out/report_bundle/report/run_summary.json" <<'JSON'
{"status":"partial","poc":{"status":"verified"},"rca":{"status":"blocked","blocker_code":"source_gap"}}
JSON
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writePoCFailedLumoskit(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-poc-failed-lumoskit")
	script := `#!/bin/sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-root) out="$2"; shift 2 ;;
    --tx) shift 2 ;;
    --chain) shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$out"
cat > "$out/summary.json" <<'JSON'
{"status":"fail","poc":{"status":"missing","failure_kind":"poc_missing"},"failure":{"kind":"poc_missing"}}
JSON
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
