# Backlight production deployment sketch

This directory contains deploy-time templates for the first production shape:

```text
operator browser -> Nginx TLS proxy -> Backlight on 127.0.0.1:8080
                                      -> SQLite DB + output roots
                                      -> lumoskit child process
                                      -> optional GitHub product publish
                                      -> optional Pre-Lumos incident JSON sidecar
                                      -> downstream webhooks / notifications
                                      -> optional local MCP bridge index
                                      -> Prometheus scrape of /metrics
```

Backlight v1 is a single-process orchestrator. Do not run multiple Backlight replicas
against the same SQLite database.

Current production templates still use the legacy `helios` Unix user, systemd
unit names, binary names, paths, and MCP tool namespace. Those identifiers are
intentionally kept stable until a separate host migration. Environment variables
use the canonical `BACKLIGHT_*` namespace; legacy `HELIOS_*` names are not read.

## What is already wired in Backlight

- `POST /signals` for hack-detector submissions.
- `POST /cases` for manual/operator submissions from the UI.
- `GET /cases`, `GET /cases/{case_id}`, and `POST /cases/{case_id}/retry-handoff` for operator workflows.
- Worker dispatch from SQLite queue to `lumoskit --tx --chain --output-root`.
- RPC env pass-through to lumoskit; Backlight does not choose RPC URLs.
- Outcome mapping from lumoskit exit code + `summary.json`.
- Downstream webhook fan-out with bounded retry.
- Optional downstream Bearer token via `BACKLIGHT_DOWNSTREAM_WEBHOOK_BEARER_TOKEN`.
- Optional verified-case GitHub publish for `PoC.t.sol` and `Report.md` as `README.md`.
- Optional verified-case Pre-Lumos Codex SDK sidecar for `pre-lumos.json` and `seed/import_{YEAR}.json`.
- Optional internal ECW export endpoint for complete PoC/RCA replay bundles.
- Operator webhook / Telegram notifications with bounded retry.
- `/metrics` for Prometheus and `/ui` for the browser console.

## What production still must provide

- A real `lumoskit` executable at `BACKLIGHT_LUMOSKIT_BIN`.
- Persistent storage for `BACKLIGHT_DB_PATH` and `BACKLIGHT_OUTPUT_ROOT`.
- Strong secret values in `/srv/backlight/env/backlight.env` and
  `/srv/backlight/env/lumoskit.env`.
- TLS termination and optional network/basic-auth gating in Nginx.
- A hack-detector or other producer calling `POST /signals` with the Bearer token.
- Downstream webhook receivers if `BACKLIGHT_DOWNSTREAM_WEBHOOK_URLS` is non-empty.
- A repo-write GitHub token if verified artifact publishing is enabled.
- Python Codex SDK dependencies and a Codex-authenticated service user if Pre-Lumos JSON generation is enabled.

## Server layout

```bash
sudo useradd --system --home /srv/backlight --shell /usr/sbin/nologin helios
sudo mkdir -p /srv/backlight/{bin,data/outputs,data/pre-lumos-seed,data/uv-cache,env,logs}
sudo install -d -o helios -g helios -m 0750 /srv/backlight/data /srv/backlight/data/outputs /srv/backlight/logs
sudo install -d -o helios -g helios -m 0750 /srv/backlight/data/.svm /srv/backlight/data/.local/share
sudo install -d -o root -g helios -m 0750 /srv/backlight/env
```

## Preferred VM local-checkout runtime flow

For this single VM, keep repo-owned runtime files in the local checkouts under
`/home/ubuntu/lumos` and keep persistent service state under `/srv/backlight`.
LumosKit needs the repo's Python/RCA files beside `bin/lumoskit`, so Backlight
points at the binary inside the LumosKit checkout.

```text
VM
  -> pull /home/ubuntu/lumos/helios to a pinned ref
  -> pull /home/ubuntu/lumos/lumoskit to a pinned ref
  -> build Backlight and install /srv/backlight/bin/backlight
  -> build LumosKit and install /home/ubuntu/lumos/lumoskit/bin/lumoskit
  -> set BACKLIGHT_LUMOSKIT_BIN=/home/ubuntu/lumos/lumoskit/bin/lumoskit
  -> reload/restart systemd
```

See [`deploy/vm/README.md`](vm/README.md) for the concrete commands.
`deploy/vm/sync-git-runtime.sh` performs the pull, build, install, systemd unit
write, daemon-reload, and optional restart.

The systemd templates in this directory use `WorkingDirectory=/srv/backlight`.
Backlight sets the LumosKit child process working directory from
`BACKLIGHT_LUMOSKIT_BIN` when the binary lives under a `bin/` directory, so
`/home/ubuntu/lumos/lumoskit/bin/lumoskit` can resolve its own runtime scripts
from `/home/ubuntu/lumos/lumoskit`.

## Manual Backlight binary copy flow

After the git runtime trees are synced, install the Backlight binaries. In CI this
is just copying build artifacts; for a first bring-up you can build them from the
Backlight repository root:

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/backlight ./cmd/helios
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/backlight-mcp ./cmd/helios-mcp
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/backlight-mcp-bridge ./cmd/helios-mcp-bridge
```

Copy artifacts:

```bash
sudo install -o root -g root -m 0755 dist/backlight /srv/backlight/bin/backlight
sudo install -o root -g root -m 0755 dist/backlight-mcp /srv/backlight/bin/backlight-mcp
sudo install -o root -g root -m 0755 dist/backlight-mcp-bridge /srv/backlight/bin/backlight-mcp-bridge
sudo install -o root -g backlight -m 0640 deploy/env/backlight.env.example /srv/backlight/env/backlight.env
sudo install -o root -g backlight -m 0640 deploy/mcp/backlight-mcp-bridge.env.example /srv/backlight/env/backlight-mcp-bridge.env
```

Edit `/srv/backlight/env/backlight.env` and replace every placeholder. Put
Backlight-owned values there, including `GH_TOKEN` when verified-case GitHub
publishing should run.

Keep LumosKit runtime keys in a separate file and install it into the LumosKit
checkout so `bin/lumoskit` can load its normal `.env` from its repo root:

```bash
sudo install -o root -g backlight -m 0640 deploy/env/lumoskit.env.example /srv/backlight/env/lumoskit.env
sudo install -o root -g backlight -m 0640 /srv/backlight/env/lumoskit.env /home/ubuntu/lumos/lumoskit/.env
```

`ALCHEMY_API_KEY` belongs in `lumoskit.env` for real LumosKit runs. Backlight will
still inherit any process env through systemd, but the local-checkout deployment
path defaults to LumosKit's own `.env` contract to avoid duplicating those keys in
`backlight.env`.

Optional preflight incident naming can run before LumosKit when Backlight has scan/RPC envs:

```bash
BACKLIGHT_INCIDENT_RESOLVER_ENABLED=true
ETHERSCAN_API_KEY=replace-with-etherscan-v2-key
# Optional, used for ERC20 symbol/name calls on candidate addresses:
BACKLIGHT_INCIDENT_RPC_URL=https://...
```

If the resolver cannot identify a protocol, submission still proceeds with the
normal `unknown` slug and later analysis may enrich `incident_slug`.

## systemd

```bash
sudo cp deploy/systemd/backlight.service.example /etc/systemd/system/backlight.service
sudo cp deploy/systemd/backlight-mcp-bridge.service.example /etc/systemd/system/backlight-mcp-bridge.service
sudo systemctl daemon-reload
sudo systemctl enable --now backlight-mcp-bridge
sudo systemctl enable --now backlight
sudo journalctl -u backlight -f
```

## Nginx

The production proxy splits browser and automation surfaces:

| Host | Exposed paths | Intended callers |
| --- | --- | --- |
| `dashboard.backwardlabs.io` | `/`, `/ui`, `/cases...`, `/healthz` | operators using the browser console |
| `api.backwardlabs.io` | `/signals`, `/cases...`, `/metrics`, `/mcp`, `/healthz` | hack-detector, Prometheus, MCP clients, operator scripts |

The dashboard host intentionally does not expose `/signals`, `/metrics`, or
`/mcp`. The API host intentionally does not serve the browser UI.

```bash
sudo cp deploy/nginx/backlight-bootstrap.conf.example /etc/nginx/sites-available/backlight-bootstrap.conf
sudo ln -s /etc/nginx/sites-available/backlight-bootstrap.conf /etc/nginx/sites-enabled/backlight-bootstrap.conf
sudo nginx -t
sudo systemctl reload nginx

sudo certbot certonly --webroot -w /var/www/html \
  --cert-name backlight-backwardlabs \
  -d dashboard.backwardlabs.io \
  -d api.backwardlabs.io

sudo cp deploy/nginx/backlight.conf.example /etc/nginx/sites-available/backlight.conf
sudo ln -s /etc/nginx/sites-available/backlight.conf /etc/nginx/sites-enabled/backlight.conf
sudo rm /etc/nginx/sites-enabled/backlight-bootstrap.conf
sudo nginx -t
sudo systemctl reload nginx
```

Before running certbot, point both DNS records at the VM. If certificates are
issued with a different cert name, update the certificate paths in the file.
For public internet exposure, prefer VPN/IP allow-listing or enable the commented
basic-auth block on `dashboard.backwardlabs.io` in addition to the Backlight
Bearer token.

## Smoke test

```bash
curl http://127.0.0.1:8080/healthz

curl -H "Authorization: Bearer $BACKLIGHT_API_TOKEN" \
  http://127.0.0.1:8080/cases

curl -X POST https://api.backwardlabs.io/cases \
  -H "Authorization: Bearer $BACKLIGHT_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "chain": "ethereum",
    "tx_hash": "0x0000000000000000000000000000000000000000000000000000000000000001",
    "metadata": {"source": "prod-smoke", "protocol": "curve"}
  }'
```

Then verify:

- `/srv/backlight/data/outputs/260526_eth_curve/summary.json` exists.
- `GET /cases/{case_id}` reaches `done` or `handed-off`, or reports a concrete `engine_error`.
- For verified cases, `github_publish` and `pre_lumos_sync` events appear when those optional features are enabled.
- Downstream webhook / Telegram notifications arrive if configured.
- Prometheus target for `https://api.backwardlabs.io/metrics` is UP.

## MCP Phase 1/2: read-only artifact gateway + downstream bridge

`backlight-mcp` supports two transports:

- stdio, the default for local MCP clients. The MCP client starts the binary and
  talks to it over stdin/stdout.
- stateless Streamable HTTP, enabled with `BACKLIGHT_MCP_LISTEN_ADDR`, for remote
  MCP clients that should connect by URL. Put it behind TLS and bearer auth;
  the HTTP endpoint defaults to `/mcp`.

`backlight-mcp-bridge` is the Phase 2 downstream receiver. It is a local HTTP
service that receives Backlight handoff payloads and stores a small SQLite index.
Run this one under systemd if you want automatic MCP indexing.

Phase 1 direct flow (stdio):

```text
MCP client -> /srv/backlight/bin/backlight-mcp
           -> Backlight HTTP API on BACKLIGHT_BASE_URL
           -> server-side allowlisted artifact reads
```

Phase 2 indexed flow (stdio):

```text
Backlight -> POST /handoff -> backlight-mcp-bridge -> bridge SQLite index
MCP client -> /srv/backlight/bin/backlight-mcp -> bridge SQLite index
                                          -> allowlisted files next to the bridge DB
```

Remote HTTP flow:

```text
MCP client --url https://api.backwardlabs.io/mcp
           -> reverse proxy TLS
           -> backlight-mcp HTTP mode on BACKLIGHT_MCP_LISTEN_ADDR
           -> Backlight API or bridge index
```

Exposed tools:

- `helios.list_cases` -> `GET /cases`
- `helios.get_case` -> `GET /cases/{case_id}`
- `helios.list_artifacts` -> existence/size for allowlisted case files
- `helios.read_artifact` -> read one allowlisted case file

The only artifact paths exposed are the text-oriented report bundle files:

- `REPORT.md`, `RCA.md`, and `PoC.t.sol` as convenience aliases
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

Required MCP env:

- `BACKLIGHT_BASE_URL`, for example `http://127.0.0.1:8080`
- `BACKLIGHT_API_TOKEN`

For bridge/indexed mode, replace `BACKLIGHT_BASE_URL` and `BACKLIGHT_API_TOKEN` with:

- `BACKLIGHT_MCP_BRIDGE_DB_PATH`, usually `/srv/backlight/data/backlight-mcp-bridge.db`

Optional MCP env:

- `BACKLIGHT_MCP_LISTEN_ADDR`, enables HTTP mode, for example `127.0.0.1:8090`.
- `BACKLIGHT_MCP_PATH`, HTTP endpoint path, default `/mcp`.
- `BACKLIGHT_MCP_HTTP_TOKEN`, bearer token for HTTP mode. Defaults to
  `BACKLIGHT_API_TOKEN` when unset.
- `BACKLIGHT_OUTPUT_ROOT` / `BACKLIGHT_OUTPUT_BASE`, bridge-mode or legacy
  direct-file containment override. Usually leave unset in direct mode because
  Backlight serves artifacts server-side. `/` is rejected.
- `BACKLIGHT_MCP_MAX_BYTES`, default `1048576`
- `BACKLIGHT_MCP_HTTP_TIMEOUT_SECONDS`, default `30`

Use `deploy/mcp/client-config.example.json` for local stdio direct mode or
`deploy/mcp/client-config.bridge.example.json` for local stdio indexed mode.
For remote URL access with Codex:

```bash
codex mcp add backlight \
  --url https://api.backwardlabs.io/mcp \
  --bearer-token-env-var BACKLIGHT_MCP_HTTP_TOKEN
```

Keep the MCP binary on the same host as Backlight/output storage unless you
intentionally mount the output directory read-only to the MCP runtime.

## ECW internal export

Backlight can expose a dedicated ECW bundle without changing the operator API,
MCP tool list, or worker queue. The ECW endpoint uses a broader allowlist than
the narrow product artifact profile, which includes the current and legacy PoC
support contracts required by report bundles. The endpoint is disabled unless
a separate token is configured:

```bash
BACKLIGHT_ECW_EXPORT_TOKEN=<separate-long-random-token>
# Optional; default is 16777216 bytes per exported file.
BACKLIGHT_ECW_EXPORT_MAX_BYTES=16777216
```

Fetch a bundle with the ECW token, not `BACKLIGHT_API_TOKEN`:

```bash
curl -fsS \
  -H "Authorization: Bearer $BACKLIGHT_ECW_EXPORT_TOKEN" \
  "https://api.backwardlabs.io/ecw/cases/<case_id>/export"
```

The response profile is `ecw-internal-complete`. It returns minimal case
metadata, a sorted `allowed` profile, `artifacts` containing the text of files
that exist under the case output root, and `missing` for profile files that were
not produced by that run. The profile includes report bundle files, replay PoC
files, RCA frontier/context/validation files, asset/fund-flow evidence, selector
labels, localized call graph, and compact pseudocode. It does not expose
arbitrary `output_root` browsing, victim source directories, prompts, Codex/event
logs, full CEFG/lift internals, or raw semantic internals.

To enable Phase 2 indexing, set:

```bash
# /srv/backlight/env/backlight.env
BACKLIGHT_DOWNSTREAM_WEBHOOK_URLS=http://127.0.0.1:9090/handoff
BACKLIGHT_DOWNSTREAM_WEBHOOK_BEARER_TOKEN=<same-value-as-bridge-token>

# /srv/backlight/env/backlight-mcp-bridge.env
BACKLIGHT_MCP_BRIDGE_LISTEN_ADDR=127.0.0.1:9090
BACKLIGHT_MCP_BRIDGE_DB_PATH=/srv/backlight/data/backlight-mcp-bridge.db
BACKLIGHT_MCP_BRIDGE_TOKEN=<same-value-as-downstream-bearer-token>
BACKLIGHT_MCP_BRIDGE_ALLOW_INSECURE=false
```

The downstream Bearer token is sent to every URL in
`BACKLIGHT_DOWNSTREAM_WEBHOOK_URLS`. If the bridge token is only meant for the
local bridge, keep the bridge as the only downstream URL or use only trusted
targets that are allowed to receive the same credential.

To enable verified product-artifact publishing to GitHub, add these to the
Backlight env file:

```bash
GITHUB_TOKEN=<repo-write-token>
BACKLIGHT_GITHUB_PUBLISH_OWNER=BackwardLabs
BACKLIGHT_GITHUB_PUBLISH_REPO=Q1-2026
BACKLIGHT_GITHUB_PUBLISH_BRANCH=main
```

Backlight publishes `PoC.t.sol` and `Report.md` only after LumosKit maps the case
to `outcome=verified`; `Report.md` is copied to `README.md` under
`test/{YYYY-MM}/{Protocol}/`. If the date/protocol is missing, or the only available protocol label is a
generic fallback such as `unknown`, `lumos_*`, or `LumosKit-Run`, Backlight records
a skipped publish event instead of creating a GitHub commit or failing the case.

To enable the Backlight x-feed publish flow after GitHub publish succeeds:

```bash
X_PUBLISH_ENABLED=true
X_DRY_RUN=false
X_CLIENT_ID=<x-oauth-client-id>
X_CLIENT_SECRET=<x-oauth-client-secret>
X_REFRESH_TOKEN_FILE=/srv/backlight/data/x_refresh_token.json
X_ACCOUNT_USERNAME=BackwardLabs
BACKLIGHT_X_FEED_ENABLED=true
BACKLIGHT_X_FEED_SKILL_DIR=skills/x-feed
BACKLIGHT_X_FEED_CARD_ENABLED=true
BACKLIGHT_X_FEED_CARD_PYTHON_BIN=python3
BACKLIGHT_X_FEED_CARD_TIMEOUT_SECONDS=20
BACKLIGHT_VICTIM_MENTION=true
BACKLIGHT_X_MCP_ENABLED=true
# Optional exact-lookup fallback when user OAuth is not configured:
# BACKLIGHT_X_MCP_BEARER_TOKEN=<x-app-only-bearer-token>
TELEGRAM_PUBLISH_ENABLED=true
```

The flow is GitHub publish -> x-feed draft -> X main post/reply -> Telegram
publish. Telegram gets the same main X body with `GitHub:` and `X:` links
appended. When the card switch is enabled, Backlight also generates a public-safe
exploit-flow card from the x-feed brief and attaches the PNG to the main X post.
Install `requirements-x-feed.txt` in the configured Python environment to produce
the PNG, for example a dedicated venv under `/srv/backlight/data/x-feed-venv`
with `BACKLIGHT_X_FEED_CARD_PYTHON_BIN=/srv/backlight/data/x-feed-venv/bin/python`.
If the PNG dependency is missing, Backlight records the SVG/status and publishes
the text thread without media.

When `X_CLIENT_ID`, `X_CLIENT_SECRET`, and `X_REFRESH_TOKEN` or
`X_REFRESH_TOKEN_FILE` are configured, Backlight uses one shared, cached OAuth
user-context token for hosted MCP lookup/search and X publishing. Refreshes are
serialized and rotated refresh tokens are persisted before either consumer
continues. An app-only `BACKLIGHT_X_MCP_BEARER_TOKEN` is only a fallback for
exact public-account lookup; X rejects `search_users` for app-only auth. If
neither user OAuth nor an app Bearer is configured, install `xurl` and run
`xurl auth oauth2 --headless` once as the service user to use the stdio bridge.

To enable Pre-Lumos importer JSON generation, install `uv` or provide a Python
environment that already has `requirements-pre-lumos.txt` installed. Confirm
that the service user's `HOME` / `CODEX_HOME` is authenticated with Codex, then add:

```bash
UV_CACHE_DIR=/srv/backlight/data/uv-cache
BACKLIGHT_PRE_LUMOS_ENABLED=true
BACKLIGHT_PRE_LUMOS_SEED_ROOT=/srv/backlight/data/pre-lumos-seed
BACKLIGHT_PRE_LUMOS_PYTHON_BIN=scripts/pre_lumos_agent_uv.sh
BACKLIGHT_PRE_LUMOS_AGENT_SCRIPT=scripts/pre_lumos_agent.py
BACKLIGHT_PRE_LUMOS_CODEX_BIN=/usr/local/bin/codex
BACKLIGHT_PRE_LUMOS_SKILL_DIR=skills/pre-lumos
BACKLIGHT_PRE_LUMOS_WEB_SEARCH=false
```

Backlight passes the verified case output root directly to the sidecar. The sidecar
writes `<output_root>/pre-lumos.json`, `<output_root>/pre-lumos-status.json`,
and merges rows by `slug` into
`$BACKLIGHT_PRE_LUMOS_SEED_ROOT/seed/import_{YEAR}.json`. The auto-report Backlight
workflow additionally writes `artifacts/pre_lumos_result.json` and
`artifacts/pre_lumos_result.md` for site/API display. Because the example
systemd unit only grants writes under `/srv/backlight/data` and `/srv/backlight/logs`,
keep `BACKLIGHT_PRE_LUMOS_SEED_ROOT` under `/srv/backlight/data` or extend
`ReadWritePaths=`.

### MCP filesystem permissions

In bridge-index mode, the MCP client process needs read-only access to:

- `/srv/backlight/data/backlight-mcp-bridge.db`
- `/srv/backlight/data/outputs/260526_eth_curve/{summary.json,summary.md,rca.md,PoC.t.sol,Report.md}`

The systemd templates use `UMask=0027`, so files are owner/group readable but
not world-readable. Run the MCP client as the `backlight` user or add the operator
account that launches the MCP client to the `backlight` group:

```bash
sudo usermod -aG backlight <operator-user>
```

Do not solve MCP permission errors with `chmod 777`. If more isolation is
needed, create a dedicated read-only group and make the Backlight service write
outputs and the bridge DB with that group.
