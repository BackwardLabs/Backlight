package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/UPside-Lumos-V2/helios/internal/api"
	"github.com/UPside-Lumos-V2/helios/internal/artifacts"
	"github.com/UPside-Lumos-V2/helios/internal/config"
	"github.com/UPside-Lumos-V2/helios/internal/handoff"
	"github.com/UPside-Lumos-V2/helios/internal/lumoskit"
	"github.com/UPside-Lumos-V2/helios/internal/mcpbridge"
	"github.com/UPside-Lumos-V2/helios/internal/mcpserver"
	"github.com/UPside-Lumos-V2/helios/internal/store"
	"github.com/UPside-Lumos-V2/helios/internal/worker"
)

func TestHeliosToBridgeToMCPReadArtifact(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tmp := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	bridgeDB := filepath.Join(tmp, "bridge.db")
	bridgeStore, err := mcpbridge.Open(ctx, bridgeDB)
	if err != nil {
		t.Fatal(err)
	}
	defer bridgeStore.Close()
	bridgeHTTP := httptest.NewServer((&mcpbridge.Server{
		Store:  bridgeStore,
		Token:  "bridge-token",
		Logger: logger,
	}).Handler())
	defer bridgeHTTP.Close()

	heliosStore, err := store.Open(ctx, filepath.Join(tmp, "helios.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer heliosStore.Close()

	outputBase := filepath.Join(tmp, "outputs")
	fakeLumoskit := writeFakeLumoskit(t, tmp)
	dispatcher := &handoff.Dispatcher{
		Store:       heliosStore,
		Client:      bridgeHTTP.Client(),
		URLs:        []string{bridgeHTTP.URL + "/handoff"},
		BearerToken: "bridge-token",
		MaxAttempts: 1,
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Millisecond,
		Logger:      logger,
	}
	w := &worker.Worker{
		Store:            heliosStore,
		Runner:           &lumoskit.Runner{Binary: fakeLumoskit},
		Dispatcher:       dispatcher,
		OutputRootParent: outputBase,
		MaxConcurrent:    1,
		PollInterval:     10 * time.Millisecond,
		Logger:           logger,
	}
	w.Start(ctx)
	defer func() {
		cancel()
		w.Wait()
		dispatcher.Wait()
	}()

	heliosHTTP := httptest.NewServer(api.NewServer(&config.Config{APIToken: "api-token"}, heliosStore, dispatcher).Handler())
	defer heliosHTTP.Close()

	caseID := submitCase(t, heliosHTTP.URL, "api-token")
	waitForHeliosCase(t, heliosHTTP.URL, "api-token", caseID, func(c api.CaseDetailResponse) bool {
		return c.State == "handed-off" && c.HandoffStatus == "succeeded"
	})

	indexed, err := bridgeStore.GetCase(ctx, caseID)
	if err != nil {
		t.Fatal(err)
	}
	if indexed.OutputRoot == nil || !strings.HasPrefix(*indexed.OutputRoot, outputBase) {
		t.Fatalf("bridge did not preserve output root under output base: %+v", indexed.OutputRoot)
	}

	reader, err := artifacts.NewReader(outputBase, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	mcp := &mcpserver.Server{Client: bridgeStore, Artifacts: reader, Logger: logger}
	input := fmt.Sprintf(
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"helios.read_artifact\",\"arguments\":{\"case_id\":%q,\"path\":\"REPORT.md\"}}}\n",
		caseID,
	)
	var out bytes.Buffer
	if err := mcp.Serve(ctx, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "phase2 bridge e2e report") {
		t.Fatalf("MCP read_artifact output did not contain artifact text:\n%s", out.String())
	}
}

func writeFakeLumoskit(t *testing.T, dir string) string {
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
mkdir -p "$out/report_bundle/report" "$out/report_bundle/poc"
cat > "$out/summary.json" <<'JSON'
{"status":"pass","poc":{"status":"verified"},"failure":{}}
JSON
printf '%s\n' 'phase2 bridge e2e summary' > "$out/summary.md"
printf '%s\n' '# RCA' 'phase2 bridge e2e rca' > "$out/RCA.md"
printf '%s\n' '// SPDX-License-Identifier: UNLICENSED' 'contract PoC {}' > "$out/report_bundle/poc/PoC.t.sol"
printf '%s\n' '# Report' 'phase2 bridge e2e report' > "$out/report_bundle/report/REPORT.md"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func submitCase(t *testing.T, baseURL, token string) string {
	t.Helper()
	body := strings.NewReader(`{"chain":"ethereum","tx_hash":"0x0000000000000000000000000000000000000000000000000000000000000001","metadata":{"protocol":"bridge-e2e"}}`)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/cases", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /cases status = %d: %s", resp.StatusCode, data)
	}
	var out api.SubmissionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.CaseID == "" {
		t.Fatal("empty case_id")
	}
	return out.CaseID
}

func waitForHeliosCase(t *testing.T, baseURL, token, caseID string, done func(api.CaseDetailResponse) bool) api.CaseDetailResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last api.CaseDetailResponse
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, baseURL+"/cases/"+caseID, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&last); err != nil {
				_ = resp.Body.Close()
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if done(last) {
				return last
			}
		} else {
			data, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			t.Fatalf("GET /cases/%s status = %d: %s", caseID, resp.StatusCode, data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("case %s did not reach desired state, last=%+v", caseID, last)
	return last
}
