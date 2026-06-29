package xpublish

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UPside-Lumos-V2/helios/internal/mention"
	"github.com/UPside-Lumos-V2/helios/internal/store"
)

func TestBuildPostUsesArtifactsAndURLs(t *testing.T) {
	root := writeArtifacts(t)
	text, err := BuildPost(Case{
		CaseID:     "case_1",
		Chain:      "ethereum",
		TxHash:     "0x" + strings.Repeat("1", 64),
		OutputRoot: root,
		ReportURL:  "https://github.com/BackwardLabs/Q1-2026/blob/main/test/README.md",
		PoCURL:     "https://github.com/BackwardLabs/Q1-2026/blob/main/test/PoC.t.sol",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[Backlight Verified Incident]",
		"Protocol: ExampleFi",
		"Chain: ethereum",
		"Tx: 0x" + strings.Repeat("1", 64),
		"Root cause:\n- Unchecked accounting inflated redeemable shares",
		"Report: https://github.com/BackwardLabs/Q1-2026/blob/main/test/README.md",
		"PoC: https://github.com/BackwardLabs/Q1-2026/blob/main/test/PoC.t.sol",
		"Attack contract: 0x2222222222222222222222222222222222222222",
		"Attacker EOA: 0x3333333333333333333333333333333333333333",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("post text missing %q:\n%s", want, text)
		}
	}
}

func TestBuildPostUsesReportJSONAndAssetDeltas(t *testing.T) {
	root := t.TempDir()
	reportDir := filepath.Join(root, "report_bundle", "report")
	evidenceDir := filepath.Join(root, "report_bundle", "evidence")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	reportJSON := `{
  "tx_hash": "0x` + strings.Repeat("a", 64) + `",
  "chain": "base",
  "vulnerability": {
    "root_cause": "The supported loss-causing path is helper contracts repeatedly calling claimReward() after earned(address), causing duplicated rewards."
  },
  "attack_summary": {
    "public_entrypoint_called_per_iteration": "claimReward() after earned(address)"
  },
  "impact": {
    "attacker_profit_symbol": "PRXVT",
    "attacker_profit_formatted": "206765.939"
  }
}`
	if err := os.WriteFile(filepath.Join(reportDir, "report.json"), []byte(reportJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	assetDeltas := `{
  "asset_deltas": [
    {"holder_role":"attacker_entry","holder":"0x1111111111111111111111111111111111111111","asset_symbol":"PRXVT","balance_delta_raw":"206765939449912147200000","asset_decimals":18},
    {"holder_role":"storage_contract","holder":"0x2222222222222222222222222222222222222222","asset_symbol":"PRXVT","balance_delta_raw":"-229739932722124608000000","asset_decimals":18}
  ],
  "attacker_surface": [
    {"role":"attacker_entry","address":"0x1111111111111111111111111111111111111111"},
    {"role":"tx_from_eoa","address":"0x3333333333333333333333333333333333333333"}
  ]
}`
	if err := os.WriteFile(filepath.Join(evidenceDir, "asset_deltas.json"), []byte(assetDeltas), 0o644); err != nil {
		t.Fatal(err)
	}

	text, err := BuildPost(Case{Chain: "base", TxHash: "0x" + strings.Repeat("a", 64), OutputRoot: root, IncidentSlug: "001_PRXVT"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Protocol: PRXVT",
		"Victim/staking contract lost 229739.932722124608 PRXVT",
		"Attacker gained 206765.939449912147 PRXVT",
		"Tx-created helpers repeatedly called claimReward() after earned(address), causing duplicable PRXVT reward payouts",
		"Attack contract: 0x1111111111111111111111111111111111111111",
		"Attacker EOA: 0x3333333333333333333333333333333333333333",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("post text missing %q:\n%s", want, text)
		}
	}
}

func TestBuildPostPrefersIncidentSlugOverTokenSymbol(t *testing.T) {
	root := t.TempDir()
	reportDir := filepath.Join(root, "report_bundle", "report")
	evidenceDir := filepath.Join(root, "report_bundle", "evidence")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	reportJSON := `{
  "tx_hash": "0x` + strings.Repeat("b", 64) + `",
  "chain": "bsc",
  "token_symbol": "USDT",
  "vulnerability": {
    "root_cause": "OLPCToken._update burns transfer value during skim-triggered transfers."
  },
  "attack_summary": {
    "public_entrypoint_called_per_iteration": "OLPC transfer -> PancakePair.skim(address) -> PancakePair.sync()"
  },
  "impact": {
    "attacker_profit_symbol": "USDT",
    "attacker_profit_formatted": "108753.775"
  }
}`
	if err := os.WriteFile(filepath.Join(reportDir, "report.json"), []byte(reportJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	assetDeltas := `{
  "asset_deltas": [
    {"holder_role":"storage_contract","holder":"0x2222222222222222222222222222222222222222","asset_symbol":"USDT","balance_delta_raw":"-108753775731175923000000","asset_decimals":18},
    {"holder_role":"attacker_entry","holder":"0x1111111111111111111111111111111111111111","asset_symbol":"WBNB","balance_delta_raw":"195568340183655000000","asset_decimals":18}
  ]
}`
	if err := os.WriteFile(filepath.Join(evidenceDir, "asset_deltas.json"), []byte(assetDeltas), 0o644); err != nil {
		t.Fatal(err)
	}

	text, err := BuildPost(Case{Chain: "bsc", TxHash: "0x" + strings.Repeat("b", 64), OutputRoot: root, IncidentSlug: "260620_bsc_pancakeswap_v2"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Protocol: pancakeswap_v2",
		"Helpers repeatedly called OLPC transfer -> PancakePair.skim(address) -> PancakePair.sync().",
		"Helpers forwarded proceeds back to the attacker entry.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("post text missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{
		"Protocol: USDT",
		"PRXVT rewards",
	} {
		if strings.Contains(text, notWant) {
			t.Fatalf("post text contains %q:\n%s", notWant, text)
		}
	}
}

func TestBuildPostUsesTemplatePath(t *testing.T) {
	root := writeArtifacts(t)
	templatePath := filepath.Join(t.TempDir(), "x-template.md")
	if err := os.WriteFile(templatePath, []byte("{{ .Protocol }} on {{ .Chain }} via {{ index .Flow 0 }}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text, err := BuildPostWithTemplate(Case{
		Chain:      "ethereum",
		TxHash:     "0x" + strings.Repeat("1", 64),
		OutputRoot: root,
	}, templatePath)
	if err != nil {
		t.Fatal(err)
	}
	if text != "ExampleFi on ethereum via The attacker funded the attack contract" {
		t.Fatalf("custom template text = %q", text)
	}
}

func TestBuildPostTagsVictimMention(t *testing.T) {
	root := writeArtifacts(t)
	templatePath := filepath.Join(t.TempDir(), "x-template.md")
	if err := os.WriteFile(templatePath, []byte("{{ .Protocol }} on {{ .Chain }}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := mention.BuildIndex([]store.MentionEntity{
		{CanonicalID: "examplefi", EntityName: "ExampleFi", Aliases: []string{"ExampleFi"}, XHandle: "examplefi", MentionPolicy: "allow", Status: "active"},
	})
	text, err := buildPost(Case{
		Chain:      "ethereum",
		TxHash:     "0x" + strings.Repeat("1", 64),
		OutputRoot: root,
	}, templatePath, idx)
	if err != nil {
		t.Fatal(err)
	}
	if text != "ExampleFi (@examplefi) on ethereum" {
		t.Fatalf("tagged template text = %q", text)
	}
	// A suppressed protocol stays plain text.
	idxSuppress := mention.BuildIndex([]store.MentionEntity{
		{CanonicalID: "examplefi", EntityName: "ExampleFi", Aliases: []string{"ExampleFi"}, XHandle: "examplefi", MentionPolicy: "suppress", Status: "active"},
	})
	text, err = buildPost(Case{Chain: "ethereum", TxHash: "0x" + strings.Repeat("1", 64), OutputRoot: root}, templatePath, idxSuppress)
	if err != nil {
		t.Fatal(err)
	}
	if text != "ExampleFi on ethereum" {
		t.Fatalf("suppressed should stay plain, got %q", text)
	}
}

func TestPublishDryRunDoesNotCallXAPI(t *testing.T) {
	root := writeArtifacts(t)
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	pub := New(Config{Enabled: true, DryRun: true, APIBase: server.URL})
	pub.Client = server.Client()
	res, err := pub.Publish(context.Background(), Case{Chain: "ethereum", TxHash: "0x" + strings.Repeat("1", 64), OutputRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("X API was called during dry run")
	}
	if res.Published || !res.DryRun || res.Platform != "x" || res.Template != templateVersion || res.Text == "" {
		t.Fatalf("dry run result = %+v", res)
	}
}

func TestPublishThreadDryRunDoesNotCallXAPI(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	pub := New(Config{Enabled: true, DryRun: true, APIBase: server.URL})
	pub.Client = server.Client()
	res, err := pub.PublishThread(context.Background(), Thread{MainText: "main", ReplyText: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("X API was called during dry run")
	}
	if res.Published || !res.DryRun || res.Text != "main" || res.ReplyText != "reply" || res.Template != threadFormatVersion {
		t.Fatalf("dry run thread result = %+v", res)
	}
}

func TestPublishRefreshesTokenAndCreatesPost(t *testing.T) {
	root := writeArtifacts(t)
	tokenFile := filepath.Join(t.TempDir(), "x_refresh_token.json")
	if err := os.WriteFile(tokenFile, []byte(`{"refresh_token":"refresh-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var sawRefresh, sawPost bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2/oauth2/token":
			sawRefresh = true
			if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Basic ") {
				t.Fatalf("refresh auth header = %q", got)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh-token" {
				t.Fatalf("refresh form = %#v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-token",
				"refresh_token": "rotated-refresh-token",
				"expires_in":    7200,
				"token_type":    "bearer",
			})
		case "/2/tweets":
			sawPost = true
			if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
				t.Fatalf("post auth header = %q", got)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body["text"], "[Backlight Verified Incident]") {
				t.Fatalf("post text = %q", body["text"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{"id": "12345", "text": body["text"]},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	pub := New(Config{
		Enabled:          true,
		ClientID:         "client-id",
		ClientSecret:     "client-secret",
		RefreshToken:     "stale-env-refresh-token",
		RefreshTokenFile: tokenFile,
		APIBase:          server.URL,
		Username:         "BackwardLabs",
		DryRun:           false,
	})
	pub.Client = server.Client()
	res, err := pub.Publish(context.Background(), Case{Chain: "ethereum", TxHash: "0x" + strings.Repeat("1", 64), OutputRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if !sawRefresh || !sawPost {
		t.Fatalf("saw refresh=%v post=%v", sawRefresh, sawPost)
	}
	if !res.Published || res.PostID != "12345" || res.PostURL != "https://x.com/BackwardLabs/status/12345" || !res.RefreshReturned || !res.RefreshTokenUpdated || !res.PostTextVerified {
		t.Fatalf("publish result = %+v", res)
	}
	tokenData, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tokenData), "rotated-refresh-token") {
		t.Fatalf("rotated refresh token was not persisted: %s", tokenData)
	}
	if info, err := os.Stat(tokenFile); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file permissions = %v", info.Mode().Perm())
	}
}

func TestPublishThreadUploadsMediaAndCreatesReply(t *testing.T) {
	mediaPath := filepath.Join(t.TempDir(), "card.png")
	if err := os.WriteFile(mediaPath, []byte("png-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	var sawRefresh, sawMedia bool
	var postCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2/oauth2/token":
			sawRefresh = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-token",
				"expires_in":   7200,
				"token_type":   "bearer",
			})
		case "/2/media/upload":
			sawMedia = true
			if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
				t.Fatalf("media auth header = %q", got)
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("media_category") != defaultMediaCategory || r.FormValue("media_type") != "image/png" {
				t.Fatalf("media form category/type = %q/%q", r.FormValue("media_category"), r.FormValue("media_type"))
			}
			if _, _, err := r.FormFile("media"); err != nil {
				t.Fatalf("media form file missing: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{"id": "media-1"},
			})
		case "/2/tweets":
			postCount++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			switch postCount {
			case 1:
				if body["text"] != "main text" {
					t.Fatalf("main post body = %#v", body)
				}
				media, ok := body["media"].(map[string]any)
				if !ok {
					t.Fatalf("main post missing media: %#v", body)
				}
				ids, ok := media["media_ids"].([]any)
				if !ok || len(ids) != 1 || ids[0] != "media-1" {
					t.Fatalf("main post media ids = %#v", media["media_ids"])
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]string{"id": "main-id", "text": "main text"},
				})
			case 2:
				if body["text"] != "reply text" {
					t.Fatalf("reply post body = %#v", body)
				}
				reply, ok := body["reply"].(map[string]any)
				if !ok || reply["in_reply_to_tweet_id"] != "main-id" {
					t.Fatalf("reply linkage = %#v", body["reply"])
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]string{"id": "reply-id", "text": "reply text"},
				})
			default:
				t.Fatalf("unexpected tweet create #%d", postCount)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	pub := New(Config{
		Enabled:      true,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RefreshToken: "refresh-token",
		APIBase:      server.URL,
		Username:     "BackwardLabs",
		DryRun:       false,
	})
	pub.Client = server.Client()
	res, err := pub.PublishThread(context.Background(), Thread{MainText: "main text", ReplyText: "reply text", MediaPath: mediaPath})
	if err != nil {
		t.Fatal(err)
	}
	if !sawRefresh || !sawMedia || postCount != 2 {
		t.Fatalf("saw refresh=%v media=%v post_count=%d", sawRefresh, sawMedia, postCount)
	}
	if !res.Published || res.PostID != "main-id" || res.ReplyPostID != "reply-id" || res.MediaID != "media-1" {
		t.Fatalf("thread publish result = %+v", res)
	}
	if res.PostURL != "https://x.com/BackwardLabs/status/main-id" || res.ReplyPostURL != "https://x.com/BackwardLabs/status/reply-id" {
		t.Fatalf("thread publish urls = %+v", res)
	}
	if !res.PostTextVerified || !res.ReplyTextVerified {
		t.Fatalf("thread text verification = %+v", res)
	}
}

func TestPublishKeepsMismatchedPost(t *testing.T) {
	root := writeArtifacts(t)
	var sawDelete bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2/oauth2/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-token",
				"expires_in":   7200,
				"token_type":   "bearer",
			})
		case "/2/tweets":
			if r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{"id": "12345", "text": "[Backlight Verified Incident]"},
			})
		case "/2/tweets/12345":
			switch r.Method {
			case http.MethodGet:
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]string{"id": "12345", "text": "[Backlight Verified Incident]"},
				})
			case http.MethodDelete:
				sawDelete = true
				_ = json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]bool{"deleted": true},
				})
			default:
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	pub := New(Config{
		Enabled:      true,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RefreshToken: "refresh-token",
		APIBase:      server.URL,
		DryRun:       false,
	})
	pub.Client = server.Client()
	res, err := pub.Publish(context.Background(), Case{Chain: "ethereum", TxHash: "0x" + strings.Repeat("1", 64), OutputRoot: root})
	if err != nil {
		t.Fatalf("publish err = %v", err)
	}
	if !res.Published || res.PostID != "12345" || res.PostTextVerified {
		t.Fatalf("publish result = %+v", res)
	}
	if sawDelete {
		t.Fatal("mismatched X post was deleted")
	}
}

func TestPublishPersistsRotatedRefreshTokenBeforePostFailure(t *testing.T) {
	root := writeArtifacts(t)
	tokenFile := filepath.Join(t.TempDir(), "x_refresh_token.json")
	if err := os.WriteFile(tokenFile, []byte(`{"refresh_token":"refresh-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2/oauth2/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-token",
				"refresh_token": "rotated-refresh-token",
				"expires_in":    7200,
				"token_type":    "bearer",
			})
		case "/2/tweets":
			http.Error(w, "post rejected", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	pub := New(Config{
		Enabled:          true,
		ClientID:         "client-id",
		ClientSecret:     "client-secret",
		RefreshTokenFile: tokenFile,
		APIBase:          server.URL,
		DryRun:           false,
	})
	pub.Client = server.Client()
	_, err := pub.Publish(context.Background(), Case{Chain: "ethereum", TxHash: "0x" + strings.Repeat("1", 64), OutputRoot: root})
	if err == nil || !strings.Contains(err.Error(), "post rejected") {
		t.Fatalf("publish err = %v", err)
	}
	tokenData, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tokenData), "rotated-refresh-token") {
		t.Fatalf("rotated refresh token was not persisted after post failure: %s", tokenData)
	}
}

func writeArtifacts(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	reportDir := filepath.Join(root, "report_bundle", "report")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reportDir, "run_summary.json"), []byte(`{"status":"pass","chain":"ethereum","protocol":"ExampleFi","poc":{"status":"verified"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	report := `# ExampleFi Incident

## Root cause

Unchecked accounting inflated redeemable shares.

## Exploit flow

- The attacker funded the attack contract.
- The attack contract inflated redeemable shares.
- The attacker redeemed and moved profit.

Attack contract: 0x2222222222222222222222222222222222222222
Attacker EOA: 0x3333333333333333333333333333333333333333
`
	if err := os.WriteFile(filepath.Join(reportDir, "Report.md"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
