# Helios operator architecture and workflow

This page is for operators and teammates who need to understand what Helios
_does_ during an incident. It intentionally avoids package-level or code-level
details.

Helios is the middle service in the Lumos incident workflow:

1. a suspicious transaction is submitted,
2. Helios turns it into a tracked case,
3. Helios runs `lumoskit` for that case,
4. Helios records the result,
5. Helios hands the result to downstream agents and notifies operators.

## System view

```mermaid
flowchart LR
    detector["hack-detector<br/>finds suspicious transactions"]
    operator["Operator<br/>browser UI or API"]
    helios["Helios<br/>case orchestrator"]
    store["SQLite case store<br/>cases, events, attempts"]
    outputs["Output roots<br/>summary.json + analysis bundle"]
    lumoskit["lumoskit<br/>analysis engine subprocess"]
    downstream["Downstream agents<br/>webhook receivers"]
    notify["Operator notifications<br/>webhook / Telegram"]
    prometheus["Prometheus<br/>metrics scrape"]
    mcp["MCP client<br/>read-only artifact access"]
    helios_mcp["helios-mcp<br/>stdio gateway"]
    mcp_bridge["helios-mcp-bridge<br/>optional downstream index"]

    detector -->|"POST /signals"| helios
    operator -->|"POST /cases<br/>GET /cases<br/>retry handoff"| helios
    helios <--> store
    helios -->|"one run per case"| lumoskit
    lumoskit -->|"summary.json"| outputs
    outputs -->|"result read by Helios"| helios
    helios -->|"verified / partial / unverified"| downstream
    helios -->|"optional POST /handoff"| mcp_bridge
    helios -->|"outcome and failure events"| notify
    prometheus -->|"GET /metrics"| helios
    mcp -->|"stdio MCP tools"| helios_mcp
    helios_mcp -->|"GET /cases"| helios
    helios_mcp -->|"or bridge DB"| mcp_bridge
    helios_mcp -->|"allowlisted files only"| outputs
```

### Boundary summary

| Area | Owner | Operator takeaway |
| --- | --- | --- |
| Detecting suspicious transactions | `hack-detector` | Helios receives already-detected transactions; it does not decide what is suspicious. |
| Running forensic analysis and PoC generation | `lumoskit` | Helios launches `lumoskit` and reads its `summary.json`; it does not perform the analysis itself. |
| Case tracking, retries, handoff, notifications | Helios | This is the service operators watch and control during incident processing. |
| MCP assistant access | `helios-mcp` / `helios-mcp-bridge` | Read-only access to case metadata and `summary.json`, `summary.md`, `rca.md`, `PoC.t.sol`; no engine execution, writes, shell, or arbitrary filesystem access. |
| Downstream follow-up | Webhook receivers / agents | They receive completed non-engine-error cases from Helios. |

## Case workflow

```mermaid
flowchart TD
    submit["Submission arrives<br/>POST /signals or POST /cases"]
    dedup{"Already have this<br/>chain + tx hash?"}
    existing["Return existing case_id<br/>no duplicate engine run"]
    queued["queued<br/>case is waiting for a worker"]
    running["running<br/>worker claimed the case"]
    engine["lumoskit runs<br/>unique output root per case"]
    result{"Engine result"}
    verified["done<br/>outcome = verified"]
    partial["done<br/>outcome = partial"]
    unverified["done<br/>outcome = unverified"]
    engine_error["failed<br/>outcome = engine_error<br/>handoff skipped"]
    downstream_config{"Downstream URLs<br/>configured?"}
    skipped["handed-off<br/>handoff_status = skipped"]
    fanout["retrying<br/>send to each downstream URL"]
    delivered["handed-off<br/>handoff_status = succeeded"]
    handoff_failed["done<br/>handoff_status = failed"]
    retry["Operator action<br/>retry handoff"]
    notify_outcome["Operator notification<br/>verified / partial / unverified"]
    notify_engine["Operator notification<br/>engine_error"]
    notify_handoff["Operator notification<br/>handoff_failed"]

    submit --> dedup
    dedup -->|"active leaf exists"| existing
    dedup -->|"new case, failed leaf, or force_rerun"| queued
    queued --> running
    running --> engine
    engine --> result
    result -->|"pass + verified PoC"| verified
    result -->|"partial result"| partial
    result -->|"unverified or missing PoC"| unverified
    result -->|"engine failure, missing summary, unreadable summary"| engine_error

    verified --> downstream_config
    partial --> downstream_config
    unverified --> downstream_config
    downstream_config -->|"no"| skipped
    downstream_config -->|"yes"| fanout
    fanout -->|"all targets delivered"| delivered
    fanout -->|"any target exhausts retries"| handoff_failed
    handoff_failed --> retry
    retry --> fanout

    verified --> notify_outcome
    partial --> notify_outcome
    unverified --> notify_outcome
    engine_error --> notify_engine
    handoff_failed --> notify_handoff
```

## What operators should watch

| Field / surface | What it means | Typical action |
| --- | --- | --- |
| `case_id` | Stable ID for one Helios attempt. | Use it when opening the UI detail page, searching logs, or retrying handoff. |
| `state=queued` | The case is waiting for an available worker slot. | Check queue depth and `HELIOS_MAX_CONCURRENT_LUMOSKIT` if many cases wait. |
| `state=running` | `lumoskit` is currently running for this case. | Wait, or inspect host resources if it stays running unexpectedly long. |
| `state=done` | `lumoskit` finished with a non-engine-error outcome. | Check `outcome` and `handoff_status`. |
| `state=handed-off` | Helios is finished with the case. | Downstream systems should now have the result, or handoff was intentionally skipped. |
| `state=failed` + `outcome=engine_error` | The engine run failed or the summary could not be used. | Inspect `failure_kind`, `summary_json_path`, output files, and operator notifications. |
| `outcome=verified` | The case produced a verified PoC. | Treat as high-confidence downstream material. |
| `outcome=partial` | The engine produced useful but incomplete material. | Review the output bundle before relying on it fully. |
| `outcome=unverified` | The engine completed but did not verify the PoC. | Review manually or rerun with better inputs if needed. |
| `handoff_status=retrying` | Helios is still delivering to downstream URLs. | Wait unless attempts are repeatedly failing. |
| `handoff_status=failed` | At least one downstream URL exhausted its retry budget. | Fix the receiver or network, then call `POST /cases/{case_id}/retry-handoff`. |
| `notification_status=failed` | Operator notification delivery failed. | Fix webhook or Telegram configuration; case processing itself is not blocked. |

## Operator checklist for a case

1. Open the case in the UI or call `GET /cases/{case_id}`.
2. Check `state` first.
3. If `state=done` or `state=handed-off`, check `outcome`.
4. If `outcome=engine_error`, check `failure_kind` and `summary_json_path`.
5. If `handoff_status=failed`, inspect `handoff_attempts` and retry handoff after the receiver is healthy.
6. If notifications are missing, inspect `notification_attempts`; this does not change the case result.
7. For fleet-level health, watch `/metrics` and queue depth.

## MCP assistant workflow

MCP support is a local read-only gateway for assistant clients.

Direct mode:

```text
MCP client -> helios-mcp stdio process -> Helios HTTP API + allowlisted output files
```

Indexed mode:

```text
Helios -> POST /handoff -> helios-mcp-bridge SQLite index
MCP client -> helios-mcp stdio process -> bridge index + allowlisted output files
```

It intentionally does not replace downstream handoff. The bridge is just another
downstream receiver that keeps a local read model for assistant queries.

## Special operational paths

### Duplicate submissions

Helios deduplicates by `(chain, tx_hash)` against the latest lineage leaf.
Repeated submissions for an active or completed leaf return the existing case
instead of starting another engine run. A failed leaf, an automatic restart
recovery, or a manual `force_rerun` creates a linked child case.

### Restart recovery

If Helios restarts while a case is `running`, it cannot safely assume the old
child process completed. On startup, Helios marks the old running case as:

```text
state=failed, outcome=engine_error, failure_kind=host_restart
```

Then it queues a new linked child case with a fresh output root.

### Empty downstream URL list

If `HELIOS_DOWNSTREAM_WEBHOOK_URLS` is empty, successful engine outcomes still
finish normally. Helios immediately moves the case to:

```text
state=handed-off, handoff_status=skipped
```

This means no external handoff was configured; it is not an error.

### Engine errors

Engine-error cases are not sent to downstream receivers. They only generate an
operator notification when a notification channel is configured.
