# VM runtime deployment

This VM uses local git checkouts under `/home/ubuntu/lumos` and keeps only
service state, env files, logs, data, and the Helios service binary under
`/srv/helios`.

```text
VM
  /home/ubuntu/lumos/helios              # Helios git checkout and systemd WorkingDirectory
  /home/ubuntu/lumos/lumoskit            # LumosKit git checkout and runtime repo root
  /home/ubuntu/lumos/lumoskit/bin/lumoskit
                                         # LumosKit binary used by Helios
  /srv/helios/bin/helios                 # Helios binary used by systemd
  /srv/helios/data/helios.db             # SQLite
  /srv/helios/data/outputs/              # per-case LumosKit outputs
  /srv/helios/data/.svm                  # Foundry solc cache writable by helios
  /srv/helios/data/.local/share          # Foundry/XDG data writable by helios
  /srv/helios/env/helios.env             # Helios env file
  /srv/helios/logs/                      # service logs / operator logs
```

Helios runs `/srv/helios/bin/helios` with:

```ini
WorkingDirectory=/home/ubuntu/lumos/helios
EnvironmentFile=/srv/helios/env/helios.env
```

`HELIOS_LUMOSKIT_BIN` should point to the LumosKit checkout binary:

```dotenv
HELIOS_LUMOSKIT_BIN=/home/ubuntu/lumos/lumoskit/bin/lumoskit
```

Helios starts LumosKit with the LumosKit repo root as the child process working
directory, so `scripts/run_agent_poc.py`, `scripts/run_rca.py`, Python virtual
envs, and `.env` resolve next to `bin/lumoskit`.

## 1. Sync, build, and restart

Use `deploy/vm/sync-git-runtime.sh` from the Helios checkout. It updates the
existing local checkouts, builds both binaries on the VM, installs them to the
paths used by systemd, writes the systemd unit/drop-ins, reloads systemd, and
optionally restarts the service.

```bash
cd /home/ubuntu/lumos/helios
sudo HELIOS_REF=main \
  LUMOSKIT_REF=main \
  HELIOS_RESTART_SERVICE=true \
  deploy/vm/sync-git-runtime.sh
```

For production, prefer immutable commit SHAs or tags:

```bash
sudo HELIOS_REF=<helios-sha-or-tag> \
  LUMOSKIT_REF=<lumoskit-sha-or-tag> \
  HELIOS_RESTART_SERVICE=true \
  deploy/vm/sync-git-runtime.sh
```

Defaults:

```text
WORKSPACE_DIR=/home/ubuntu/lumos
HELIOS_WORKTREE=$WORKSPACE_DIR/helios
LUMOSKIT_WORKTREE=$WORKSPACE_DIR/lumoskit
HELIOS_BASE_DIR=/srv/helios
HELIOS_SERVICE_USER=helios
HELIOS_SERVICE_NAME=helios.service
HELIOS_RESTART_SERVICE=false
```

The script expects both checkouts to already exist. It does not clone into
`/srv/helios/src`.

## 2. Manual build/install

The script performs these operations, but they are useful for debugging.

Build Helios:

```bash
cd /home/ubuntu/lumos/helios
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/helios ./cmd/helios
sudo install -o root -g root -m 0755 dist/helios /srv/helios/bin/helios
```

Build LumosKit:

```bash
cd /home/ubuntu/lumos/lumoskit
cargo build --release --bin lumoskit
sudo install -o ubuntu -g helios -m 0755 \
  target/release/lumoskit \
  /home/ubuntu/lumos/lumoskit/bin/lumoskit
```

Then restart:

```bash
sudo systemctl daemon-reload
sudo systemctl restart helios
```

## 3. Env files

Required Helios values in `/srv/helios/env/helios.env`:

- `HELIOS_API_TOKEN`
- `HELIOS_DB_PATH=/srv/helios/data/helios.db`
- `HELIOS_OUTPUT_ROOT=/srv/helios/data/outputs`
- `HELIOS_LUMOSKIT_BIN=/home/ubuntu/lumos/lumoskit/bin/lumoskit`

Set `GH_TOKEN` in `helios.env` when verified-case GitHub publishing should be
enabled.

Keep LumosKit runtime secrets next to the LumosKit checkout, or install that
file from `/srv/helios/env/lumoskit.env`:

```bash
sudo install -o root -g helios -m 0640 /srv/helios/env/lumoskit.env \
  /home/ubuntu/lumos/lumoskit/.env
```

Required LumosKit value for real runs:

- `ALCHEMY_API_KEY`

`ETHERSCAN_API_KEY` and `OPENAI_API_KEY` are optional depending on which
LumosKit stages and repair lanes are enabled.

## 4. systemd

The deployed unit should match the active VM layout:

```ini
[Service]
User=helios
Group=helios
WorkingDirectory=/home/ubuntu/lumos/helios
EnvironmentFile=/srv/helios/env/helios.env
ExecStart=/srv/helios/bin/helios
Restart=always
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=30
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ReadWritePaths=/srv/helios/data /srv/helios/logs
UMask=0027
```

Foundry must write compiler caches under `/srv/helios/data`, because the service
uses `ProtectSystem=full`:

```ini
[Service]
Environment="PATH=/srv/helios/.foundry/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
Environment="SVM_HOME=/srv/helios/data/.svm"
Environment="XDG_DATA_HOME=/srv/helios/data/.local/share"
```

## 5. Smoke test

```bash
curl http://127.0.0.1:8080/healthz

sudo bash -c 'set -a; . /srv/helios/env/helios.env; set +a; \
  curl -H "Authorization: Bearer ${HELIOS_API_TOKEN}" \
  http://127.0.0.1:8080/cases'

sudo -u helios /home/ubuntu/lumos/lumoskit/bin/lumoskit --help
```
