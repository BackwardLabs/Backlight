# VM git-clone runtime deployment

For the first single-VM deployment, keep this simple:

```text
VM
  /srv/helios/bin/helios                 # Helios binary from CI/release
  /srv/helios/src/helios                 # pinned git clone for deploy assets
  /srv/helios/src/lumoskit               # pinned git clone; includes bin/lumoskit
  /srv/helios/data/helios.db             # SQLite
  /srv/helios/data/outputs/              # per-case LumosKit outputs
  /srv/helios/env/helios.env             # short production env file
```

This avoids copying a large custom bundle. LumosKit already keeps its Linux
convenience binary in git, and its runtime Python/RCA files live next to that
binary in the same repo. Helios points at that executable:

```dotenv
HELIOS_LUMOSKIT_BIN=/srv/helios/src/lumoskit/bin/lumoskit
```

Helios now runs a `bin/lumoskit` executable with the LumosKit repo root as the
child process working directory, so the binary can resolve its own
`scripts/run_agent_poc.py`, `scripts/run_rca.py`, and RCA instruction files.

## 1. Sync git runtime trees

Use immutable commits or tags for production, not a floating branch:

```bash
sudo HELIOS_REF=<helios-sha-or-tag> \
  LUMOSKIT_REF=<lumoskit-sha-or-tag> \
  deploy/vm/sync-git-runtime.sh
```

The script does not build Go or Rust. It only places runtime files and the
committed LumosKit binary on disk.

### Oracle Cloud Ampere A1 / ARM64 note

Oracle's 4 OCPU / 24 GB shape is usually ARM64 (`aarch64`). The currently
committed LumosKit convenience binary may be Linux x86-64, so verify before
starting Helios:

```bash
uname -m
file /srv/helios/src/lumoskit/bin/lumoskit
```

If the host is `aarch64` but `bin/lumoskit` is `x86-64`, replace it with a
Linux ARM64 build:

```bash
cd /srv/helios/src/lumoskit
cargo build --release -p lumoskit-cli
sudo install -o helios -g helios -m 0755 target/release/lumoskit bin/lumoskit
```

For normal deploys, prefer producing that ARM64 LumosKit binary in CI and
installing it over `bin/lumoskit` after the git sync.

## 2. Install Helios binaries

Install the Helios binaries produced by CI into `/srv/helios/bin`.
On Oracle Ampere/ARM64, build Helios with `GOARCH=arm64`:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/helios ./cmd/helios
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/helios-mcp ./cmd/helios-mcp
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/helios-mcp-bridge ./cmd/helios-mcp-bridge
```

Then install:

```bash
sudo install -o helios -g helios -m 0755 dist/helios /srv/helios/bin/helios
sudo install -o helios -g helios -m 0755 dist/helios-mcp /srv/helios/bin/helios-mcp
sudo install -o helios -g helios -m 0755 dist/helios-mcp-bridge /srv/helios/bin/helios-mcp-bridge
```

If you do not have CI artifacts yet, building these three Helios binaries on the
VM is acceptable for the very first bring-up, but it should not be the normal
deploy path.

## 3. Env files

Start from the short Helios example:

```bash
sudo install -o helios -g helios -m 0600 \
  /srv/helios/src/helios/deploy/env/helios.env.example \
  /srv/helios/env/helios.env
```

Required Helios values:

- `HELIOS_API_TOKEN`
- `HELIOS_DB_PATH=/srv/helios/data/helios.db`
- `HELIOS_OUTPUT_ROOT=/srv/helios/data/outputs`
- `HELIOS_LUMOSKIT_BIN=/srv/helios/src/lumoskit/bin/lumoskit`

Set `GH_TOKEN` in `helios.env` when verified-case GitHub publishing should be
enabled.

Keep LumosKit runtime secrets in a separate file:

```bash
sudo install -o helios -g helios -m 0600 \
  /srv/helios/src/helios/deploy/env/lumoskit.env.example \
  /srv/helios/env/lumoskit.env
sudo ln -sfn /srv/helios/env/lumoskit.env /srv/helios/src/lumoskit/.env
```

Required LumosKit value for real runs:

- `ALCHEMY_API_KEY`

`ETHERSCAN_API_KEY` and `OPENAI_API_KEY` are optional depending on which
LumosKit stages/repair lanes you enable.

## 4. systemd

The systemd templates use:

```ini
WorkingDirectory=/srv/helios/src/helios
ExecStart=/srv/helios/bin/helios
```

That keeps Helios deploy assets available from its repo clone while the
LumosKit child process runs from the LumosKit repo root.

## 5. Smoke test

```bash
curl http://127.0.0.1:8080/healthz

set -a
. /srv/helios/env/helios.env
set +a

curl -H "Authorization: Bearer $HELIOS_API_TOKEN" \
  http://127.0.0.1:8080/cases

sudo -u helios /srv/helios/src/lumoskit/bin/lumoskit \
  --chain ethereum \
  --tx 0x0000000000000000000000000000000000000000000000000000000000000001 \
  --output-root /srv/helios/data/outputs/deploy-smoke
```
