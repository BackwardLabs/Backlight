# Backlight results and handoff

This page explains how to interpret a completed Backlight case and how those
results can be used by operators, downstream agents, and MCP clients.

## How to read results

Backlight preserves the LumosKit engine result and maps it into an operator-facing
case outcome plus a rerun decision. The top-level Backlight `outcome` is
`verified`, `partial`, `unverified`, or `engine_error`; the stage and rerun
fields come from the LumosKit `summary.json` payload copied into the terminal
`state_transition` event.

| Analysis stage | Source fields | Backlight outcome | Rerun decision | What reruns |
| --- | --- | --- | --- | --- |
| `success` | `summary_status=pass` and `poc.status=verified` | `verified` | `no_rerun` | Nothing. The result is final. |
| `poc_failed` | `summary_status=fail` with `poc.status=unverified` or `poc.status=missing` | `unverified` | `manual_review` | Nothing automatically. Operators inspect and can submit `force_rerun=true`. |
| `poc_blocked` | `summary_status=partial` with `poc.status=unverified`, `poc.status=missing`, or `poc.failure_kind` | `partial` | `auto_rerun` | Child attempt copies prior deterministic artifacts, reruns `agent_poc`, then runs `rca`. |
| `rca_blocked` | `summary_status=partial`, PoC verified, and `rca.status=blocked` or an RCA blocker code/reason exists | `partial` | `auto_rerun` | Child attempt copies prior PoC artifacts and reruns `rca` only. |
| `partial` | `summary_status=partial` without a more specific PoC/RCA blocker | `partial` | `auto_rerun` | Child attempt reruns the full pipeline. |
| `engine_error` | nonzero exit, missing/unreadable summary, `failure.kind=engine_error`, or unexpected summary shape | `engine_error` | `manual_review` | Nothing automatically. Operators inspect `failure_kind` and artifacts. |

Backlight records `analysis_stage`, `rerun_decision`, and `rerun_reason` on the
terminal `state_transition` event. For `rerun_decision=auto_rerun`, it also
records `auto_rerun_eligible`, `auto_rerun_resume_stage`,
`auto_rerun_max_attempts`, and, when blocked by configuration,
`auto_rerun_blocked_reason`. The queued child stores the same resume intent in
metadata under `helios_auto_rerun`. When the child runs, terminal payloads include
`resume_stage`, `resume_source_case_id`, and `lumoskit_stage`. Automatic reruns
are bounded by `HELIOS_PARTIAL_AUTO_RERUN_MAX_ATTEMPTS`; manual review can still
create a new linked attempt with `force_rerun=true`.

## Result payload example

`state_transition` rows are the audit log for state changes. The terminal
`running -> done` or `running -> failed` transition carries a compact PoC/RCA
payload copied from `summary.json`, so operators can tell where the analysis
stopped without opening artifacts first.

Example `GET /cases/{case_id}` event payload for a partial result:

```json
{
  "outcome": "partial",
  "rule": "O2",
  "summary_status": "partial",
  "analysis_stage": "poc_blocked",
  "rerun_decision": "auto_rerun",
  "rerun_reason": "missing_profit_or_economic_oracle",
  "auto_rerun_eligible": true,
  "auto_rerun_resume_stage": "agent_poc",
  "auto_rerun_max_attempts": 3,
  "failure": {
    "kind": "missing_profit_or_economic_oracle",
    "message": "economic proof could not be verified"
  },
  "poc": {
    "status": "unverified",
    "proof_kind": "reachability_only",
    "forge_build_status": "pass",
    "forge_test_status": "pass",
    "failure_kind": "missing_profit_or_economic_oracle"
  },
  "rca": {
    "status": "blocked",
    "blocker_code": "economic_proof_gap",
    "blocker_reason": "proof_kind is reachability_only, expected economic_proof"
  }
}
```

In the browser UI this is summarized as **Analysis result** and the full JSON is
available in the Events table.

## Using the result

- **UI/API:** open `GET /cases/{case_id}` and check `state`, `outcome`,
  `analysis_stage`, `rerun_decision`, `rerun_reason`, `auto_rerun_eligible`,
  `poc`, `rca`, `failure`, `handoff_status`, and the terminal
  `state_transition` event.
- **Downstream agents:** set `HELIOS_DOWNSTREAM_WEBHOOK_URLS`. Backlight sends
  completed non-engine-error cases (`verified`, `partial`, `unverified`) and
  records per-target retry attempts. `engine_error` cases skip downstream
  fan-out.
- **Product publishing:** configure GitHub env vars. Backlight can publish
  `PoC.t.sol` and `Report.md` as a product README for `outcome=verified` and
  final `outcome=partial` cases when publishable artifacts exist.
- **Importer-ready incident JSON:** enable `HELIOS_PRE_LUMOS_ENABLED=true` and
  set `HELIOS_PRE_LUMOS_SEED_ROOT`. Verified cases run the Pre-Lumos Agent SDK
  sidecar and write `<output_root>/pre-lumos.json` plus merged
  `seed/import_{YEAR}.json` rows.
- **MCP-assisted review:** run `helios-mcp` in direct API mode or bridge-index
  mode. Assistant clients can call `helios.list_cases`, `helios.get_case`,
  `helios.list_artifacts`, and `helios.read_artifact` to inspect case metadata
  and allowlisted artifacts.

## Read-only MCP gateway

MCP support is intentionally narrow: MCP clients can inspect Backlight cases and
read only allowlisted product-facing report bundle files from a case output
root. The MCP server does not run `lumoskit`, write files, trigger downstream
webhooks, or expose arbitrary shell/filesystem access.

Two source modes are supported:

- **Direct mode:** `helios-mcp` reads case metadata from the Backlight API using
  `HELIOS_BASE_URL` + `HELIOS_API_TOKEN`; Backlight serves allowlisted artifacts
  from its configured output root.
- **Bridge-index mode:** Backlight posts completed handoff payloads to
  `helios-mcp-bridge`; `helios-mcp` reads the bridge SQLite index using
  `HELIOS_MCP_BRIDGE_DB_PATH` and derives the output directory from the bridge
  DB location unless explicitly overridden.

Exposed tools:

- `helios.list_cases` -> `GET /cases`
- `helios.get_case` -> `GET /cases/{case_id}`
- `helios.list_artifacts` -> existence/size for allowlisted case files
- `helios.read_artifact` -> read one allowlisted case file

Common artifact paths:

- `REPORT.md`, `RCA.md`, and `PoC.t.sol`
- `report_bundle/README.md`
- `report_bundle/manifest.json`
- `report_bundle/report/REPORT.md`
- `report_bundle/report/RCA.md`
- `report_bundle/report/report.json`
- `report_bundle/report/run_summary.json`
- `report_bundle/poc/PoC.t.sol`
- `report_bundle/poc/LumosPoCBase.sol`
- `report_bundle/evidence/asset_deltas.json`
- `report_bundle/evidence/fund_flows.json`
- `report_bundle/visuals/asset_deltas.dot`
- `report_bundle/visuals/fund_flows.dot`

Build the MCP binaries with:

```bash
go build -o dist/helios-mcp ./cmd/helios-mcp
go build -o dist/helios-mcp-bridge ./cmd/helios-mcp-bridge
```

Then configure a local stdio MCP client from
`deploy/mcp/client-config.example.json` or
`deploy/mcp/client-config.bridge.example.json`. For remote clients, run
`helios-mcp` in HTTP mode behind TLS by setting `HELIOS_MCP_LISTEN_ADDR`; the
endpoint defaults to `/mcp`, should be exposed from `api.backwardlabs.io`, and
uses bearer auth from
`HELIOS_MCP_HTTP_TOKEN`, or `HELIOS_API_TOKEN` when the MCP-specific token is
unset.
