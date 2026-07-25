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
| `poc_missing` | `summary_status=fail` with `poc.status=missing`, or missing reconstruction/execution evidence without a recoverable execution failure | `unverified` | `manual_review` | Nothing automatically. Operators inspect the missing evidence boundary. Telegram reports **PoC incomplete**. |
| `poc_failed` | `summary_status=fail` with `poc.status=unverified` and a non-recoverable failure reason | `unverified` | `manual_review` | Nothing automatically. Operators inspect and can submit `force_rerun=true`. |
| `reachable_poc` | `summary_status=partial` with a non-failing Foundry replay (`forge_test_status=pass`) but incomplete economic proof (`execution_state=reachable_poc` or `proof_kind!=economic_proof`) | `partial` | `guided_repair` | Child attempt copies deterministic artifacts and reruns `agent_poc_repair` to strengthen economic proof; RCA may already be complete. |
| `poc_blocked` | `summary_status=partial` or `fail` with recoverable `static_validation_failed`, `forge_fmt_failed`, `forge_build_failed`, `forge_test_failed`, `abi_selector_compatibility_failed`, or `protocol_revert_with_oracle_gap` (including a missing product PoC after safe source restoration) | `partial` or `unverified` | `auto_rerun` | Child attempt copies prior deterministic artifacts, reruns `agent_poc`, then runs `rca` if the replay becomes non-failing. |
| `rca_blocked` | `summary_status=partial`, PoC verified, and `rca.status=blocked` or an RCA blocker code/reason exists | `partial` | `auto_rerun` | Child attempt copies prior PoC artifacts and reruns `rca` only. |
| `partial` | `summary_status=partial` without a more specific PoC/RCA blocker | `partial` | `auto_rerun` | Child attempt reruns the full pipeline. |
| `engine_error` | nonzero exit, missing/unreadable summary, `failure.kind=engine_error`, or unexpected summary shape | `engine_error` | `manual_review` | Nothing automatically. Operators inspect `failure_kind` and artifacts. |

Backlight records `analysis_stage`, `rerun_decision`, and `rerun_reason` on the
terminal `state_transition` event. For queued rerun decisions (`auto_rerun` or
`guided_repair`), it also records `auto_rerun_eligible`, `auto_rerun_resume_stage`,
`auto_rerun_max_attempts`, and, when blocked by configuration,
`auto_rerun_blocked_reason`. The queued child stores the same resume intent in
metadata under `helios_auto_rerun`. When the child runs, terminal payloads include
`resume_stage`, `resume_source_case_id`, and `lumoskit_stage`. Automatic reruns
are bounded by `BACKLIGHT_PARTIAL_AUTO_RERUN_MAX_ATTEMPTS`; manual review can still
create a new linked attempt with `force_rerun=true`.

Operator notifications preserve the same distinction: `poc_missing` is
rendered as **PoC incomplete**, while **PoC failed** is reserved for an actual
non-recoverable replay or validation failure. Intermediate automatic rerun
attempts do not notify. For manually linked attempts, Backlight also suppresses
an exact semantic duplicate of the latest successfully notified attempt. The
suppressed case records `notification_status=succeeded` plus a durable
`notification_suppressed` event with `reason=identical_terminal_result`.
Changed outcomes or diagnoses, prior notification failures, and all
`engine_error` cases are always delivered.

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
  "analysis_stage": "reachable_poc",
  "rerun_decision": "guided_repair",
  "rerun_reason": "missing_profit_or_economic_oracle",
  "auto_rerun_eligible": true,
  "auto_rerun_resume_stage": "agent_poc_repair",
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
    "status": "complete",
    "analysis_status": "complete"
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
- **Downstream agents:** set `BACKLIGHT_DOWNSTREAM_WEBHOOK_URLS`. Backlight sends
  completed non-engine-error cases (`verified`, `partial`, `unverified`) and
  records per-target retry attempts. `engine_error` cases skip downstream
  fan-out.
- **Product publishing:** configure GitHub env vars. Backlight can publish
  `PoC.t.sol` and `Report.md` as a product README for `outcome=verified` and
  final `outcome=partial` cases when publishable artifacts exist.
- **Importer-ready incident JSON:** enable `BACKLIGHT_PRE_LUMOS_ENABLED=true` and
  set `BACKLIGHT_PRE_LUMOS_SEED_ROOT`. Verified cases run the Pre-Lumos Codex SDK
  sidecar and write `<output_root>/pre-lumos.json` plus merged
  `seed/import_{YEAR}.json` rows.
- **MCP-assisted review:** run `backlight-mcp` in direct API mode or bridge-index
  mode. Assistant clients can call `helios.list_cases`, `helios.get_case`,
  `helios.list_artifacts`, and `helios.read_artifact` to inspect case metadata
  and allowlisted artifacts.

## Read-only MCP gateway

MCP support is intentionally narrow: MCP clients can inspect Backlight cases and
read only allowlisted product-facing report bundle files from a case output
root. The MCP server does not run `lumoskit`, write files, trigger downstream
webhooks, or expose arbitrary shell/filesystem access.

Two source modes are supported:

- **Direct mode:** `backlight-mcp` reads case metadata from the Backlight API using
  `BACKLIGHT_BASE_URL` + `BACKLIGHT_API_TOKEN`; Backlight serves allowlisted artifacts
  from its configured output root.
- **Bridge-index mode:** Backlight posts completed handoff payloads to
  `backlight-mcp-bridge`; `backlight-mcp` reads the bridge SQLite index using
  `BACKLIGHT_MCP_BRIDGE_DB_PATH` and derives the output directory from the bridge
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
- `report_bundle/poc/Base.sol` (current PoC support contract)
- `report_bundle/poc/LumosPoCBase.sol` (legacy compatibility)
- `report_bundle/evidence/asset_deltas.json`
- `report_bundle/evidence/fund_flows.json`
- `report_bundle/visuals/asset_deltas.dot`
- `report_bundle/visuals/fund_flows.dot`
- `artifacts/pre_lumos_result.json`
- `artifacts/pre_lumos_result.md`

## ECW export profile

The internal ECW extension uses a separate bearer token and endpoint instead of
expanding the MCP/product artifact profile:

```text
GET /ecw/cases/{case_id}/export
Authorization: Bearer $BACKLIGHT_ECW_EXPORT_TOKEN
```

The endpoint is disabled when `BACKLIGHT_ECW_EXPORT_TOKEN` is unset. It returns an
`ecw-internal-complete` JSON bundle with minimal case metadata, the sorted ECW
allowlist, exported text artifacts, and a `missing` list for allowlisted files
that were not produced by the selected run.

The ECW profile is intentionally broader than MCP because it supports internal
PoC replay/adaptation work. It includes report bundle files, `inputs/tx_metadata.json`,
agent PoC result/foundry files, PoC sketch context/spec files, RCA frontier and
validation files, asset/fund-flow evidence, selector labels, localized call
graph, and compact pseudocode. It still uses exact file allowlisting and output
root containment; it does not expose arbitrary output-root browsing, victim
source directories, prompts, Codex/event logs, full CEFG/lift internals, or raw
semantic internals.

Build the MCP binaries with:

```bash
go build -o dist/backlight-mcp ./cmd/helios-mcp
go build -o dist/backlight-mcp-bridge ./cmd/helios-mcp-bridge
```

Then configure a local stdio MCP client from
`deploy/mcp/client-config.example.json` or
`deploy/mcp/client-config.bridge.example.json`. For remote clients, run
`backlight-mcp` in HTTP mode behind TLS by setting `BACKLIGHT_MCP_LISTEN_ADDR`; the
endpoint defaults to `/mcp`, should be exposed from `api.backwardlabs.io`, and
uses bearer auth from
`BACKLIGHT_MCP_HTTP_TOKEN`, or `BACKLIGHT_API_TOKEN` when the MCP-specific token is
unset.
