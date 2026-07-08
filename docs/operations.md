# Backlight operations

This page keeps the operational details out of the project README.

## What's wired

| Surface | State |
| --- | --- |
| `GET /healthz` | done (unauthenticated for container probes) |
| `GET /` / `GET /ui` | done (browser console for submitting and tracking analysis cases) |
| `GET /metrics` | done (Prometheus default registry, auth required) |
| `POST /signals` | done (validate, dedup, persist, queue) |
| `POST /cases` | done (validate, dedup, persist, `force_rerun`) |
| `GET /cases` | done (filters: state/outcome/chain/tx_hash/created_from/created_to, offset/limit, default `created_at DESC`) |
| `GET /cases/{case_id}` | done (case-detail + events + handoff/notification attempts) |
| `POST /cases/{case_id}/retry-handoff` | done (precondition: state=done AND handoff_status=failed; pending URLs only) |
| `GET /ecw/cases/{case_id}/export` | done (internal ECW artifact bundle, separate ECW token, exact file allowlist) |
| Worker (FIFO dispatcher) | done (poll loop + semaphore for max_concurrent) |
| lumoskit child process invocation | done (env pass-through; stderr captured with 64 KiB cap) |
| Outcome mapping (O1..O8) | done (strict precedence O5 -> O6 -> O7 -> O4 -> O1 -> O2 -> O3 -> O8) |
| Downstream fan-out | done (per-URL bounded exp backoff, multi-URL aggregation) |
| GitHub product publish | done (`verified` and final `partial`, `test/{YYYY-MM}/{Protocol}/`, `Report.md` -> `README.md`) |
| Pre-Lumos incident JSON | done (verified only, Agent SDK sidecar, case `pre-lumos.json` + `seed/import_{YEAR}.json`) |
| Operator notifications | done (webhook + Telegram native; 5 events; per-channel retry) |
| Restart recovery | done (orphan running cases -> failed/host_restart + linked child queued) |
| Prometheus metrics | done (case_state/outcome counters, queue_depth gauge, lumoskit duration histogram, handoff/notification attempt counters) |

## Web UI

Open `http://127.0.0.1:8080/ui` or the matching host/port from
`HELIOS_LISTEN_ADDR`. The UI is a lightweight console for manual analysis work:

- save the local `HELIOS_API_TOKEN` in browser localStorage
- submit `POST /cases` with `chain`, `tx_hash`, optional incident metadata,
  and optional `force_rerun`
- poll `GET /cases` for queued/running/done/failed progress
- inspect `GET /cases/{case_id}` analysis result, events, output paths,
  handoff attempts, and notifications
- call `POST /cases/{case_id}/retry-handoff` when a completed case is retryable

The HTML shell is public so a browser can load it without a pre-existing
Authorization header. All data/actions still use the protected API and require
the token entered in the UI.

## Configuration

All configuration is via env. Defaults match `seeds/v1.yaml` ->
`runtime_config_surface`.

For local runs, Backlight loads `.env` and `.env.local` from the current working
directory before reading configuration. Existing process env values take
precedence; `.env.local` can override `.env`.

| Variable | Required | Default | Notes |
| --- | --- | --- | --- |
| `HELIOS_API_TOKEN` | yes | - | Bearer token for core API endpoints except `/healthz` |
| `HELIOS_ECW_EXPORT_TOKEN` | no | empty | separate Bearer token for `GET /ecw/cases/{case_id}/export`; unset disables ECW export and the value must differ from `HELIOS_API_TOKEN` |
| `HELIOS_ECW_EXPORT_MAX_BYTES` | no | `16777216` | per-artifact read cap for ECW export responses |
| `HELIOS_DB_PATH` | yes | - | SQLite file path |
| `HELIOS_OUTPUT_ROOT` | yes | - | fixed parent directory for flat human-readable LumosKit `--output-root` directories (`<YYMMDD>_<chain-alias>_<protocol>[-N]`) |
| `HELIOS_LISTEN_ADDR` | no | `:8080` | HTTP listen address |
| `HELIOS_LUMOSKIT_BIN` | no | `bin/lumoskit` | child-process executable invoked per case |
| `HELIOS_WORKER_POLL_MILLIS` | no | `1000` | worker poll cadence |
| `HELIOS_MAX_CONCURRENT_LUMOSKIT` | no | `2` | parallel lumoskit ceiling |
| `HELIOS_PARTIAL_AUTO_RERUN_MAX_ATTEMPTS` | no | `3` | linked rerun ceiling for `rerun_decision=auto_rerun`; set `0` to disable |
| `HELIOS_DOWNSTREAM_WEBHOOK_URLS` | no | empty | comma-separated webhook list (empty means `handoff_status=skipped`, case advances directly to `handed-off`) |
| `HELIOS_DOWNSTREAM_WEBHOOK_BEARER_TOKEN` | no | empty | optional Bearer token sent to downstream webhook targets, useful for `backlight-mcp-bridge` |
| `HELIOS_HANDOFF_RETRY_MAX_ATTEMPTS` | no | `5` | per-URL retry ceiling |
| `HELIOS_HANDOFF_RETRY_BACKOFF_BASE_SECONDS` | no | `2` | exponential backoff base |
| `HELIOS_HANDOFF_RETRY_BACKOFF_MAX_SECONDS` | no | `300` | exponential backoff ceiling |
| `OPERATOR_NOTIFY_WEBHOOK_URL` | no | empty | enables operator-webhook channel when set |
| `TELEGRAM_BOT_TOKEN` | no | empty | Backlight process env var required with chat id for Telegram alerts; can reuse the hackdetector bot token |
| `TELEGRAM_CHAT_ID` | no | empty | Backlight process env var required with bot token; target group/channel/user id for Backlight alerts |
| `HELIOS_TELEGRAM_API_BASE` | no | `https://api.telegram.org` | override for tests / self-hosted Telegram proxies |
| `HELIOS_NOTIFY_RETRY_MAX_ATTEMPTS` | no | `5` | notification retry ceiling |
| `HELIOS_NOTIFY_RETRY_BACKOFF_BASE_SECONDS` | no | `2` | |
| `HELIOS_NOTIFY_RETRY_BACKOFF_MAX_SECONDS` | no | `300` | |
| `GITHUB_TOKEN` / `GH_TOKEN` | no | empty | enables GitHub publish for verified LumosKit outputs; skipped when unset |
| `HELIOS_GITHUB_PUBLISH_OWNER` | no | `BackwardLabs` | GitHub owner for product artifact publish |
| `HELIOS_GITHUB_PUBLISH_REPO` | no | `Q1-2026` | GitHub repo for product artifact publish |
| `HELIOS_GITHUB_PUBLISH_BRANCH` | no | `main` | GitHub branch for product artifact publish |
| `X_PUBLISH_ENABLED` | no | `false` | enables X side-effect publishing for verified Backlight incident posts |
| `X_CLIENT_ID` | when X publish enabled and dry-run false | empty | OAuth 2.0 client id for the X app |
| `X_CLIENT_SECRET` | when X publish enabled and dry-run false | empty | OAuth 2.0 client secret for the X app |
| `X_REFRESH_TOKEN` | when X publish enabled and dry-run false | empty | OAuth refresh token with `tweet.write`, `tweet.read`, `users.read`, `media.write`, and `offline.access` scopes |
| `X_REFRESH_TOKEN_FILE` | no | empty | optional 0600 JSON/raw-token file used before `X_REFRESH_TOKEN` and updated when X rotates the refresh token |
| `X_POST_TEMPLATE_PATH` | no | embedded default | optional Markdown `text/template` file for the X verified-incident post body |
| `X_API_BASE` | no | `https://api.x.com` | override for tests or proxies |
| `X_ACCOUNT_USERNAME` | no | empty | optional username used to build the public `post_url`; omit to use `https://x.com/i/web/status/{id}` |
| `X_DRY_RUN` | no | `true` | when true, records the generated X post text as `x_publish` without calling X |
| `HELIOS_X_FEED_ENABLED` | no | `false` | enables the structured x-feed draft path after successful GitHub publish |
| `HELIOS_X_FEED_SKILL_DIR` | no | `skills/x-feed` | vendored x-feed skill directory containing `incident-post-format.md` |
| `HELIOS_X_FEED_INCLUDE_ATTACKER_CA` | no | `false` | opt-in switch for attacker CA details in the X reply |
| `HELIOS_X_FEED_CARD_ENABLED` | no | `true` | runs the vendored `exploit-flow-card` renderer during x-feed draft generation |
| `HELIOS_X_FEED_CARD_PYTHON_BIN` | no | `python3` | Python executable used for the exploit-flow-card renderer |
| `HELIOS_X_FEED_CARD_TIMEOUT_SECONDS` | no | `20` | timeout for the exploit-flow-card renderer; failures are recorded but do not block text publishing |
| `TELEGRAM_PUBLISH_ENABLED` | no | `false` | after live X thread publish succeeds, sends the same main X body plus GitHub and X links to Telegram |
| `HELIOS_PRE_LUMOS_ENABLED` | no | `false` | enables the Pre-Lumos Agent SDK sidecar for verified cases |
| `HELIOS_PRE_LUMOS_SEED_ROOT` | when enabled | empty | repo/root where `seed/import_{YEAR}.json` should be merged by slug |
| `HELIOS_PRE_LUMOS_OPENAI_BASE_URL` | no | `http://127.0.0.1:10631/v1` | OpenAI-compatible API proxy for the Agent SDK runner |
| `HELIOS_PRE_LUMOS_PYTHON_BIN` | no | `python3` | Python executable used to run `scripts/pre_lumos_agent.py` |
| `HELIOS_PRE_LUMOS_AGENT_SCRIPT` | no | `scripts/pre_lumos_agent.py` | Pre-Lumos Agent SDK runner script |
| `HELIOS_PRE_LUMOS_SKILL_DIR` | no | `skills/pre-lumos` | vendored skill bundle; copied as-is from `BackwardLabs/skills-pre-lumos` |
| `HELIOS_PRE_LUMOS_CASE_OUTPUT_ROOT` / `HELIOS_PRE_LUMOS_CASE_OUTPUT_ROOTS` | no | empty | standalone runner input roots; Backlight worker passes the case output root automatically |
| `HELIOS_PRE_LUMOS_YEAR` | no | inferred | optional forced target year for `seed/import_{YEAR}.json` |
| `HELIOS_PRE_LUMOS_MODEL` / `OPENAI_MODEL` | no | SDK default | optional model override for the Agent SDK runner |
| `HELIOS_PRE_LUMOS_WEB_SEARCH` | no | `false` | enables the hosted `WebSearchTool` when installed/supported |

Plus any RPC env vars (`CEFG_LIVE_RPC_URL`, `RPC_URL`, `ETH_RPC_URL`,
`ALCHEMY_API_KEY`); Backlight passes these through unchanged to the spawned
lumoskit child process per ADR-0018 in the `lumoskit` repo.

## Operational notes

- **State machine.** `queued -> running -> done -> handed-off`. Engine failure
  is `running -> failed` (outcome=`engine_error`, populated `failure_kind`).
  Handoff failure leaves `state=done` with `handoff_status=failed`; the case
  can be re-driven via `POST /cases/{case_id}/retry-handoff`.
- **GitHub publish.** When `GITHUB_TOKEN` or `GH_TOKEN` is set, Backlight publishes
  product artifacts for `verified` and final `partial` cases to
  `test/{YYYY-MM}/{Protocol}/` in the configured Q1 repo. The publish step
  requires `PoC.t.sol` and `Report.md`; `Report.md` becomes `README.md`.
- **Pre-Lumos incident JSON.** When `HELIOS_PRE_LUMOS_ENABLED=true` and
  `HELIOS_PRE_LUMOS_SEED_ROOT` is set, verified cases also run
  `scripts/pre_lumos_agent.py`, write `<case output root>/pre-lumos.json`, and
  merge rows into `seed/import_{YEAR}.json` by `slug`. The auto-report Backlight
  workflow also writes `artifacts/pre_lumos_result.json` and
  `artifacts/pre_lumos_result.md` for site/API display.
- **ECW export.** When `HELIOS_ECW_EXPORT_TOKEN` is set, `GET /ecw/cases/{case_id}/export`
  returns an `ecw-internal-complete` bundle for internal PoC replay/adaptation.
  It uses exact file allowlisting, reports absent profile files in `missing`, and
  does not expand the MCP/product artifact profile.
- **Decision-based auto-rerun.** Terminal event payloads include
  `analysis_stage`, `rerun_decision`, `rerun_reason`, `auto_rerun_resume_stage`,
  and eligibility fields. Attempts with `rerun_decision=auto_rerun` and
  `auto_rerun_eligible=true` are retried as linked child cases until
  `HELIOS_PARTIAL_AUTO_RERUN_MAX_ATTEMPTS` is reached. `reachable_poc` resumes
  at `agent_poc_repair` to strengthen economic proof while preserving the RCA
  result; `poc_blocked` resumes at `agent_poc` and then runs `rca` if the
  replay becomes non-failing; `rca_blocked` resumes at `rca` only;
  generic partial results rerun the full pipeline. Intermediate attempts skip
  downstream handoff and operator notification; the final attempt follows the
  normal terminal flow.
- **Lineage.** Each retry, automatic engine_error recovery, and `force_rerun`
  inserts a new case row linked via `parent_case_id` with `attempt_number+1`.
- **Restart recovery.** On startup every `state=running` row is rewritten to
  `state=failed`, `outcome=engine_error`, `failure_kind=host_restart`,
  `handoff_status=skipped`, and a fresh linked child case is queued with a new
  `output_root`.
- **Empty downstream URL list.** Successful engine outcomes immediately move to
  `handed-off` with `handoff_status=skipped`.
- **engine_error cases.** Skip downstream fan-out entirely. Only an operator
  notification (`event=engine_error`) is produced, and only if a channel is
  configured.
- **Notification status.** Reflects the latest event delivery: `pending`,
  `retrying`, `succeeded`, `failed`, or `disabled` (no channels at all).
  Telegram messages include case identity, concise result, optional reason,
  GitHub report link, and completion time when available.
- **X refresh tokens.** Live X publishing refreshes an access token before
  posting. Set `X_REFRESH_TOKEN_FILE=/srv/backlight/data/x_refresh_token.json` so
  Backlight can persist rotated refresh tokens with `0600` permissions. SQLite
  events record only `refresh_returned` / `refresh_token_updated`; token
  material is never stored in case events.
- **x-feed publish sequence.** When `HELIOS_X_FEED_ENABLED=true`, Backlight waits
  for GitHub artifact publish to succeed, drafts `x-feed-main-post.txt`,
  `x-feed-reply-post.txt`, `x-feed-telegram-post.txt`, and `x-feed-status.json`,
  then publishes the X main post and reply. `ready_to_publish=false` blocks X
  posting when required public URLs or the vendored format file are missing.
- **Exploit-flow card generation.** With `HELIOS_X_FEED_CARD_ENABLED=true`,
  Backlight writes a public-safe `x-feed-card-brief.md`, runs the vendored
  `skills/exploit-flow-card/scripts/render_card.py`, and records SVG/PNG paths in
  `x-feed-status.json`. The renderer always attempts SVG output; PNG output
  requires `requirements-x-feed.txt` installed in
  `HELIOS_X_FEED_CARD_PYTHON_BIN`'s environment.
- **X media upload.** If the x-feed draft supplies `image_path`, Backlight uploads
  it through X API v2 media upload before creating the main post, then attaches
  the returned media id to `POST /2/tweets`. The exploit-flow card PNG becomes
  that `image_path` when rendering succeeds. If card rendering fails or only SVG
  is produced, Backlight records `card_error` and still publishes the text thread
  without media.
- **X post verification.** After each create call, Backlight verifies the
  returned/fetched post text matches the requested text. If X truncates or
  mutates the body, Backlight keeps the post and records
  `post_text_verified=false` or `reply_text_verified=false` in `x_publish`.
- **Telegram publish.** With `TELEGRAM_PUBLISH_ENABLED=true`, Telegram receives
  the main X body exactly plus `GitHub:\n<target directory URL>` and
  `X:\n<main post URL>` after live X publishing succeeds. X dry runs do not send
  Telegram publish messages.
- **X post template.** The default template is embedded from
  `internal/xpublish/templates/x_verified_incident.md`. Set
  `X_POST_TEMPLATE_PATH` to point at a Markdown Go `text/template` file when
  operators need to edit the public post shape without rebuilding Backlight.

Telegram message shape:

```text
[Backlight] Success

Incident: Ambient Finance on Ethereum
Tx: 0x12345678...abcd
Result: verified · no rerun
Report: https://github.com/.../README.md
Completed: 2026-06-03 03:25 UTC
```
