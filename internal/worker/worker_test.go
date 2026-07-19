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
	"github.com/UPside-Lumos-V2/helios/internal/notify"
	"github.com/UPside-Lumos-V2/helios/internal/outcome"
	"github.com/UPside-Lumos-V2/helios/internal/store"
	"github.com/UPside-Lumos-V2/helios/internal/xfeed"
	"github.com/UPside-Lumos-V2/helios/internal/xpublish"
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
		XPublisher:      xpublish.New(xpublish.Config{Enabled: true, DryRun: true, Username: "BackwardLabs"}),
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
	var sawPublish, sawXPublish, sawDuration bool
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
		if event.EventType == "x_publish" {
			sawXPublish = true
			if payload["dry_run"] != true || payload["published"] != false || payload["platform"] != "x" {
				t.Fatalf("x publish payload = %#v", payload)
			}
			if !strings.Contains(payload["text"].(string), "[Backlight Verified Incident]") {
				t.Fatalf("x publish text = %#v", payload["text"])
			}
			if !strings.Contains(payload["text"].(string), "Report: https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/yETH/README.md") {
				t.Fatalf("x publish text missing github report url: %#v", payload["text"])
			}
		}
	}
	if !sawDuration {
		t.Fatalf("running done state_transition did not include duration_ms: %#v", events)
	}
	if !sawPublish {
		t.Fatalf("github_publish event not found: %#v", events)
	}
	if !sawXPublish {
		t.Fatalf("x_publish event not found: %#v", events)
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
		XPublisher:      xpublish.New(xpublish.Config{Enabled: true, DryRun: true, Username: "BackwardLabs"}),
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
	var sawPublish, sawXPublish bool
	for _, event := range events {
		if event.EventType != "github_publish" && event.EventType != "x_publish" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("unmarshal publish payload: %v", err)
		}
		if event.EventType == "github_publish" && payload["published"] == true && payload["outcome"] == "partial" && payload["publish_tier"] == outcome.PublishTierEconomicIncompleteRCA {
			sawPublish = true
		}
		if event.EventType == "x_publish" && payload["dry_run"] == true && payload["rca_state"] == outcome.RCAStateScopeLimited {
			sawXPublish = true
			if !strings.Contains(payload["text"].(string), "[Backlight Verified Incident]") {
				t.Fatalf("x publish title changed: %#v", payload["text"])
			}
		}
	}
	if !sawPublish {
		t.Fatalf("partial github_publish event not found: %#v", events)
	}
	if !sawXPublish {
		t.Fatalf("scope-limited partial x_publish event not found: %#v", events)
	}
}

func TestWorkerXFeedPublishesXThreadThenTelegram(t *testing.T) {
	ctx := context.Background()
	outputParent := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c, _, err := st.SubmitCase(ctx, "ethereum", "0x"+strings.Repeat("8", 64), nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err = st.ClaimNextQueued(ctx, outputParent)
	if err != nil {
		t.Fatal(err)
	}

	github := newFakeGitHub(t)
	defer github.server.Close()
	githubPublisher := githubpublish.New(githubpublish.Config{Token: "test-token", APIBase: github.server.URL})
	githubPublisher.Client = github.server.Client()

	var xPostCount int
	xServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2/oauth2/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-token",
				"expires_in":   7200,
				"token_type":   "bearer",
			})
		case "/2/users/me":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{"id": "account-id", "username": "BackwardLabs"},
			})
		case "/2/tweets":
			xPostCount++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			switch xPostCount {
			case 1:
				text := body["text"].(string)
				for _, want := range []string{
					"🚨 yETH — Under review",
					"Key info",
					"TL;DR",
					"Why it matters",
					"Builder takeaway",
				} {
					if !strings.Contains(text, want) {
						t.Fatalf("main X body missing %q: %q", want, text)
					}
				}
				if strings.Contains(text, "[Backlight") || strings.Contains(text, "Tx: ") {
					t.Fatalf("main X body = %q", text)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": "main-id", "text": text}})
			case 2:
				text := body["text"].(string)
				reply, ok := body["reply"].(map[string]any)
				if !ok || reply["in_reply_to_tweet_id"] != "main-id" {
					t.Fatalf("reply X body = %#v", body)
				}
				for _, want := range []string{
					"Artifacts + analysis 🧾",
					"- Report: https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/yETH/README.md",
					"- PoC: https://github.com/BackwardLabs/Q1-2026/blob/main/test/2026-01/yETH/yETH.t.sol",
					"Analysis:\nAt a high level,",
					"Explorer:\nhttps://etherscan.io/tx/0x" + strings.Repeat("8", 64),
				} {
					if !strings.Contains(text, want) {
						t.Fatalf("reply X body missing %q: %#v", want, body)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": "reply-id", "text": text}})
			default:
				t.Fatalf("unexpected X post count %d", xPostCount)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer xServer.Close()

	var telegramMu sync.Mutex
	var telegramTexts []string
	telegramServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		telegramMu.Lock()
		telegramTexts = append(telegramTexts, body.Text)
		telegramMu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer telegramServer.Close()

	xPublisher := xpublish.New(xpublish.Config{
		Enabled:      true,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RefreshToken: "refresh-token",
		APIBase:      xServer.URL,
		Username:     "BackwardLabs",
		DryRun:       false,
	})
	xPublisher.Client = xServer.Client()
	notifier := &notify.Notifier{
		Store:       st,
		Channels:    []notify.Channel{&notify.TelegramChannel{BotToken: "token", ChatID: "chat", APIBase: telegramServer.URL, Client: telegramServer.Client()}},
		MaxAttempts: 1,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	w := &Worker{
		Store:                  st,
		Runner:                 &lumoskit.Runner{Binary: writePartialPublishLumoskit(t, t.TempDir())},
		GitHubPublisher:        githubPublisher,
		XFeedRunner:            &xfeed.Runner{Enabled: true, SkillDir: writeWorkerSkillDir(t)},
		XPublisher:             xPublisher,
		Notifier:               notifier,
		TelegramPublishEnabled: true,
		Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	w.process(ctx, c)
	w.Wait()

	if xPostCount != 2 {
		t.Fatalf("X post count = %d, want 2", xPostCount)
	}
	events, err := st.CaseEvents(ctx, c.CaseID)
	if err != nil {
		t.Fatal(err)
	}
	var draftText, xText, tgText, githubURL, xURL string
	var sawDraft, sawX, sawTelegram bool
	for _, event := range events {
		var payload map[string]any
		if len(event.Payload) > 0 {
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("unmarshal event %s: %v", event.EventType, err)
			}
		}
		switch event.EventType {
		case "x_feed_draft":
			sawDraft = true
			if payload["ready_to_publish"] != true {
				t.Fatalf("x_feed_draft payload = %#v", payload)
			}
			draftText = payload["main_post"].(string)
			githubURL = payload["github_url"].(string)
		case "x_publish":
			sawX = true
			if payload["published"] != true || payload["reply_post_id"] != "reply-id" {
				t.Fatalf("x_publish payload = %#v", payload)
			}
			xText = payload["text"].(string)
			xURL = payload["post_url"].(string)
		case notify.EventTelegramPublish:
			sawTelegram = true
			if payload["published"] != true {
				t.Fatalf("telegram_publish payload = %#v", payload)
			}
			if payload["x_url"] != "https://x.com/BackwardLabs/status/main-id" {
				t.Fatalf("telegram_publish x_url = %#v", payload["x_url"])
			}
			tgText = payload["text"].(string)
		}
	}
	if !sawDraft || !sawX || !sawTelegram {
		t.Fatalf("missing publish events draft=%v x=%v telegram=%v events=%#v", sawDraft, sawX, sawTelegram, events)
	}
	if draftText == "" || draftText != xText {
		t.Fatalf("draft/main X mismatch draft=%q x=%q", draftText, xText)
	}
	telegramMu.Lock()
	deliveredTelegramTexts := append([]string(nil), telegramTexts...)
	telegramMu.Unlock()
	foundDeliveredPublish := false
	for _, text := range deliveredTelegramTexts {
		if text == tgText && strings.Contains(text, draftText+"\n\nGitHub:\n"+githubURL+"\n\nX:\n"+xURL) {
			foundDeliveredPublish = true
		}
	}
	if !foundDeliveredPublish {
		t.Fatalf("telegram publish text not delivered texts=%q event=%q github=%q", deliveredTelegramTexts, tgText, githubURL)
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

func TestWorkerGuidedRepairsReachablePoCBeforePublishOrHandoff(t *testing.T) {
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
	if payload["analysis_stage"] != outcome.AnalysisStageReachablePoC {
		t.Fatalf("analysis_stage = %v, want %s", payload["analysis_stage"], outcome.AnalysisStageReachablePoC)
	}
	if payload["rerun_decision"] != outcome.RerunDecisionGuidedRepair {
		t.Fatalf("rerun_decision = %v, want %s", payload["rerun_decision"], outcome.RerunDecisionGuidedRepair)
	}
	if payload["rerun_eligible"] != true || payload["rerun_resume_stage"] != "agent_poc_repair" {
		t.Fatalf("rerun eligibility payload = %#v", payload)
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
	if auto["resume_stage"] != "agent_poc_repair" || auto["decision"] != outcome.RerunDecisionGuidedRepair || auto["repair_strategy"] != "economic_proof_guided_repair" {
		t.Fatalf("child guided repair metadata=%#v", auto)
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
		case r.Method == http.MethodGet && r.URL.Path == "/repos/BackwardLabs/Q1-2026/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"base-commit"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/BackwardLabs/Q1-2026/git/commits/base-commit":
			_, _ = w.Write([]byte(`{"sha":"base-commit","tree":{"sha":"base-tree"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/BackwardLabs/Q1-2026/git/blobs":
			blobCount++
			_, _ = w.Write([]byte(`{"sha":"blob-` + string(rune('0'+blobCount)) + `"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/BackwardLabs/Q1-2026/git/trees":
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
		case r.Method == http.MethodPost && r.URL.Path == "/repos/BackwardLabs/Q1-2026/git/commits":
			_, _ = w.Write([]byte(`{"sha":"next-commit","tree":{"sha":"next-tree"}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/BackwardLabs/Q1-2026/git/refs/heads/main":
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

func writeWorkerSkillDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "skills", "draft-x-exploit-thread", "references", "incident-post-format.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Backlight Incident Post Format\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
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
{"status":"pass","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"complete"}}
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
{"status":"partial","poc":{"status":"verified","execution_state":"economic_poc","proof_kind":"economic_proof"},"rca":{"status":"partial","blocker_code":"scope_limited"}}
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
{"status":"pass","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"complete"},"tx_timestamp":1779811222}
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
{"status":"partial","poc":{"status":"unverified","execution_state":"reachable_poc","proof_kind":"reachability_only","forge_build_status":"pass","forge_test_status":"pass","failure_kind":"missing_profit_or_economic_oracle"},"rca":{"status":"complete","analysis_status":"complete"}}
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
{"status":"pass","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"complete"}}
JSON
  exit 0
fi
mkdir -p "$out/artifacts/agent_poc"
cat > "$out/artifacts/agent_poc/result.json" <<'JSON'
{"status":"pass","poc_status":"pass"}
JSON
cat > "$out/report_bundle/report/run_summary.json" <<'JSON'
{"status":"partial","poc":{"status":"verified","execution_state":"economic_poc"},"rca":{"status":"blocked","blocker_code":"rca_agent_timeout"}}
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
{"status":"fail","poc":{"status":"unverified","execution_state":"no_working_poc","failure_kind":"forge_test_failed"},"failure":{"kind":"forge_test_failed"}}
JSON
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
