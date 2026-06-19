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

func TestPublishRefreshesTokenAndCreatesPost(t *testing.T) {
	root := writeArtifacts(t)
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
		Enabled:      true,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RefreshToken: "refresh-token",
		APIBase:      server.URL,
		Username:     "BackwardLabs",
		DryRun:       false,
	})
	pub.Client = server.Client()
	res, err := pub.Publish(context.Background(), Case{Chain: "ethereum", TxHash: "0x" + strings.Repeat("1", 64), OutputRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if !sawRefresh || !sawPost {
		t.Fatalf("saw refresh=%v post=%v", sawRefresh, sawPost)
	}
	if !res.Published || res.PostID != "12345" || res.PostURL != "https://x.com/BackwardLabs/status/12345" || !res.RefreshReturned {
		t.Fatalf("publish result = %+v", res)
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
