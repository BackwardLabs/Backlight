# Helios production deployment sketch

This directory contains deploy-time templates for the first production shape:

```text
operator browser -> Nginx TLS proxy -> Helios on 127.0.0.1:8080
                                      -> SQLite DB + output roots
                                      -> lumoskit child process
                                      -> downstream webhooks / notifications
                                      -> optional local MCP bridge index
                                      -> Prometheus scrape of /metrics
```

Helios v1 is a single-process orchestrator. Do not run multiple Helios replicas
against the same SQLite database.

## What is already wired in Helios

- `POST /signals` for hack-detector submissions.
- `POST /cases` for manual/operator submissions from the UI.
- `GET /cases`, `GET /cases/{case_id}`, and `POST /cases/{case_id}/retry-handoff` for operator workflows.
- Worker dispatch from SQLite queue to `lumoskit --tx --chain --output-root`.
- RPC env pass-through to lumoskit; Helios does not choose RPC URLs.
- Outcome mapping from lumoskit exit code + `summary.json`.
- Downstream webhook fan-out with bounded retry.
- Optional downstream Bearer token via `HELIOS_DOWNSTREAM_WEBHOOK_BEARER_TOKEN`.
- Operator webhook / Telegram notifications with bounded retry.
- `/metrics` for Prometheus and `/ui` for the browser console.

## What production still must provide

- A real `lumoskit` executable at `HELIOS_LUMOSKIT_BIN`.
- Persistent storage for `HELIOS_DB_PATH` and `HELIOS_OUTPUT_ROOT`.
- Strong secret values in `/srv/helios/env/helios.env`.
- TLS termination and optional network/basic-auth gating in Nginx.
- A hack-detector or other producer calling `POST /signals` with the Bearer token.
- Downstream webhook receivers if `HELIOS_DOWNSTREAM_WEBHOOK_URLS` is non-empty.

## Server layout

```bash
sudo useradd --system --home /srv/helios --shell /usr/sbin/nologin helios
sudo mkdir -p /srv/helios/{bin,data/outputs,env,logs}
sudo chown -R helios:helios /srv/helios
sudo chmod 750 /srv/helios /srv/helios/data /srv/helios/data/outputs /srv/helios/logs
sudo chmod 700 /srv/helios/env
```

Build Helios binaries from the repository root:

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/helios ./cmd/helios
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/helios-mcp ./cmd/helios-mcp
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/helios-mcp-bridge ./cmd/helios-mcp-bridge
```

Copy artifacts:

```bash
sudo install -o helios -g helios -m 0755 dist/helios /srv/helios/bin/helios
sudo install -o helios -g helios -m 0755 dist/helios-mcp /srv/helios/bin/helios-mcp
sudo install -o helios -g helios -m 0755 dist/helios-mcp-bridge /srv/helios/bin/helios-mcp-bridge
sudo install -o helios -g helios -m 0755 lumoskit /srv/helios/bin/lumoskit
sudo install -o helios -g helios -m 0600 deploy/env/helios.env.example /srv/helios/env/helios.env
sudo install -o helios -g helios -m 0600 deploy/mcp/helios-mcp-bridge.env.example /srv/helios/env/helios-mcp-bridge.env
```

Edit `/srv/helios/env/helios.env` and replace every placeholder. Put the
lumoskit runtime env in this same file as well for systemd; Helios starts
lumoskit as a child process and passes its complete environment through
unchanged. For local runs, Helios also loads `.env` and `.env.local` from its
working directory, without overriding variables already present in the process
environment.

If you already have a lumoskit `.env`, merge only the needed key/value lines into
`/srv/helios/env/helios.env` instead of copying the file into git:

```bash
sudo sh -c 'grep -E "^(CEFG_LIVE_RPC_URL|RPC_URL|ETH_RPC_URL|ALCHEMY_API_KEY|ETHERSCAN_API_KEY|OPENAI_API_KEY)=" /path/to/lumoskit/.env >> /srv/helios/env/helios.env'
sudo chown helios:helios /srv/helios/env/helios.env
sudo chmod 600 /srv/helios/env/helios.env
```

Extend the `grep -E` list if your lumoskit build expects additional keys.

## systemd

```bash
sudo cp deploy/systemd/helios.service.example /etc/systemd/system/helios.service
sudo cp deploy/systemd/helios-mcp-bridge.service.example /etc/systemd/system/helios-mcp-bridge.service
sudo systemctl daemon-reload
sudo systemctl enable --now helios-mcp-bridge
sudo systemctl enable --now helios
sudo journalctl -u helios -f
```

## Nginx

```bash
sudo cp deploy/nginx/helios.conf.example /etc/nginx/sites-available/helios.conf
sudo ln -s /etc/nginx/sites-available/helios.conf /etc/nginx/sites-enabled/helios.conf
sudo nginx -t
sudo systemctl reload nginx
```

Before reloading, replace `helios.example.com` and certificate paths in the file.
For public internet exposure, prefer VPN/IP allow-listing or enable the commented
basic-auth block in addition to the Helios Bearer token.

## Smoke test

```bash
curl http://127.0.0.1:8080/healthz

curl -H "Authorization: Bearer $HELIOS_API_TOKEN" \
  http://127.0.0.1:8080/cases

curl -X POST https://helios.example.com/cases \
  -H "Authorization: Bearer $HELIOS_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "chain": "ethereum",
    "tx_hash": "0x0000000000000000000000000000000000000000000000000000000000000001",
    "metadata": {"source": "prod-smoke"}
  }'
```

Then verify:

- `/srv/helios/data/outputs/<case-id>/summary.json` exists.
- `GET /cases/{case_id}` reaches `done` or `handed-off`, or reports a concrete `engine_error`.
- Downstream webhook / Telegram notifications arrive if configured.
- Prometheus target for `/metrics` is UP.

## MCP Phase 1/2: read-only artifact gateway + downstream bridge

`helios-mcp` is a stdio MCP server. Do not run it as a public network service
or as a standalone systemd daemon. The MCP client starts the binary and talks to
it over stdin/stdout.

`helios-mcp-bridge` is the Phase 2 downstream receiver. It is a local HTTP
service that receives Helios handoff payloads and stores a small SQLite index.
Run this one under systemd if you want automatic MCP indexing.

Phase 1 direct flow:

```text
MCP client -> /srv/helios/bin/helios-mcp
           -> Helios HTTP API on HELIOS_BASE_URL
           -> server-side allowlisted artifact reads
```

Phase 2 indexed flow:

```text
Helios -> POST /handoff -> helios-mcp-bridge -> bridge SQLite index
MCP client -> /srv/helios/bin/helios-mcp -> bridge SQLite index
                                          -> allowlisted files next to the bridge DB
```

Exposed tools:

- `helios.list_cases` -> `GET /cases`
- `helios.get_case` -> `GET /cases/{case_id}`
- `helios.list_artifacts` -> existence/size for allowlisted case files
- `helios.read_artifact` -> read one allowlisted case file

The only artifact paths exposed are:

- `summary.json`
- `summary.md`
- `rca.md`
- `PoC.t.sol`
- `Report.md`

Required MCP env:

- `HELIOS_BASE_URL`, for example `http://127.0.0.1:8080`
- `HELIOS_API_TOKEN`

For bridge/indexed mode, replace `HELIOS_BASE_URL` and `HELIOS_API_TOKEN` with:

- `HELIOS_MCP_BRIDGE_DB_PATH`, usually `/srv/helios/data/helios-mcp-bridge.db`

Optional MCP env:

- `HELIOS_OUTPUT_ROOT` / `HELIOS_OUTPUT_BASE`, bridge-mode or legacy
  direct-file containment override. Usually leave unset in direct mode because
  Helios serves artifacts server-side. `/` is rejected.
- `HELIOS_MCP_MAX_BYTES`, default `1048576`
- `HELIOS_MCP_HTTP_TIMEOUT_SECONDS`, default `30`

Use `deploy/mcp/client-config.example.json` for direct mode or
`deploy/mcp/client-config.bridge.example.json` for indexed mode. Keep the MCP
binary on the same host as Helios/output storage unless you intentionally mount
the output directory read-only to the MCP runtime.

To enable Phase 2 indexing, set:

```bash
# /srv/helios/env/helios.env
HELIOS_DOWNSTREAM_WEBHOOK_URLS=http://127.0.0.1:9090/handoff
HELIOS_DOWNSTREAM_WEBHOOK_BEARER_TOKEN=<same-value-as-bridge-token>

# /srv/helios/env/helios-mcp-bridge.env
HELIOS_MCP_BRIDGE_LISTEN_ADDR=127.0.0.1:9090
HELIOS_MCP_BRIDGE_DB_PATH=/srv/helios/data/helios-mcp-bridge.db
HELIOS_MCP_BRIDGE_TOKEN=<same-value-as-downstream-bearer-token>
HELIOS_MCP_BRIDGE_ALLOW_INSECURE=false
```

The downstream Bearer token is sent to every URL in
`HELIOS_DOWNSTREAM_WEBHOOK_URLS`. If the bridge token is only meant for the
local bridge, keep the bridge as the only downstream URL or use only trusted
targets that are allowed to receive the same credential.

To enable verified product-artifact publishing to GitHub, add these to the
Helios env file:

```bash
GITHUB_TOKEN=<repo-write-token>
HELIOS_GITHUB_PUBLISH_OWNER=UPside-Lumos-V2
HELIOS_GITHUB_PUBLISH_REPO=Q1-2026
HELIOS_GITHUB_PUBLISH_BRANCH=main
```

Helios publishes `PoC.t.sol` and `Report.md` only after LumosKit maps the case
to `outcome=verified`; `Report.md` is copied to `README.md` under
`test/{YYYY-MM}/{Protocol}/`.

### MCP filesystem permissions

In bridge-index mode, the MCP client process needs read-only access to:

- `/srv/helios/data/helios-mcp-bridge.db`
- `/srv/helios/data/outputs/<case-id>/{summary.json,summary.md,rca.md,PoC.t.sol,Report.md}`

The systemd templates use `UMask=0027`, so files are owner/group readable but
not world-readable. Run the MCP client as the `helios` user or add the operator
account that launches the MCP client to the `helios` group:

```bash
sudo usermod -aG helios <operator-user>
```

Do not solve MCP permission errors with `chmod 777`. If more isolation is
needed, create a dedicated read-only group and make the Helios service write
outputs and the bridge DB with that group.
