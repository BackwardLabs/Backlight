# VM runtime deployment

This VM uses local git checkouts under `/home/ubuntu/lumos` and keeps only
service state, env files, logs, data, and the Backlight service binary under
`/srv/backlight`.

Backlight runs under the `backlight` service account with runtime state under
`/srv/backlight`. The application still reads `HELIOS_*` env vars for backward
compatibility.

```text
VM
  /home/ubuntu/lumos/helios              # Backlight git checkout
  /home/ubuntu/lumos/lumoskit            # LumosKit git checkout and runtime repo root
  /home/ubuntu/lumos/lumoskit/bin/lumoskit
                                         # LumosKit binary used by Backlight
  /srv/backlight/bin/backlight                 # Backlight binary used by systemd
  /srv/backlight/data/backlight.db             # SQLite
  /srv/backlight/data/outputs/              # per-case LumosKit outputs
  /srv/backlight/data/.svm                  # Foundry solc cache writable by backlight
  /srv/backlight/data/.local/share          # Foundry/XDG data writable by backlight
  /srv/backlight/env/backlight.env             # Backlight env file
  /srv/backlight/logs/                      # service logs / operator logs
```

Backlight runs `/srv/backlight/bin/backlight` with:

```ini
WorkingDirectory=/srv/backlight
EnvironmentFile=/srv/backlight/env/backlight.env
```

`HELIOS_LUMOSKIT_BIN` should point to the LumosKit checkout binary:

```dotenv
HELIOS_LUMOSKIT_BIN=/home/ubuntu/lumos/lumoskit/bin/lumoskit
```

Backlight starts LumosKit with the LumosKit repo root as the child process working
directory, so `scripts/run_agent_poc.py`, `scripts/run_rca.py`, Python virtual
envs, and `.env` resolve next to `bin/lumoskit`.

## 1. Sync, build, and restart

Use `deploy/vm/sync-git-runtime.sh` from the Backlight checkout. It updates the
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
HELIOS_BASE_DIR=/srv/backlight
HELIOS_SERVICE_USER=backlight
HELIOS_SERVICE_NAME=backlight.service
HELIOS_RESTART_SERVICE=false
```

The script expects both checkouts to already exist. It does not clone into
`/srv/backlight/src`.

Do not run production updates with a raw `git pull` only. LumosKit loads Python
scripts, prompt templates, and RCA skill files from the checkout at runtime, and
those files must be readable by the `backlight` service user. The sync script
normalizes runtime asset permissions after every checkout:

- `scripts/`, `prompts/`, and `external/` are assigned to group `backlight`
- files receive group read permission
- directories receive group read/traverse permission and setgid inheritance
- git/build steps run with `umask 0027`

If someone manually pulls LumosKit and the service starts failing with
`Permission denied`, rerun the sync script instead of only restarting systemd.

## 2. Manual build/install

The script performs these operations, but they are useful for debugging.

Build Backlight:

```bash
cd /home/ubuntu/lumos/helios
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/backlight ./cmd/helios
sudo install -o root -g root -m 0755 dist/backlight /srv/backlight/bin/backlight
```

Build LumosKit:

```bash
cd /home/ubuntu/lumos/lumoskit
cargo build --release --bin lumoskit
sudo install -o ubuntu -g backlight -m 0755 \
  target/release/lumoskit \
  /home/ubuntu/lumos/lumoskit/bin/lumoskit
```

Then refresh the runtime checkout permissions:

```bash
sudo chgrp -R backlight \
  /home/ubuntu/lumos/lumoskit/scripts \
  /home/ubuntu/lumos/lumoskit/prompts \
  /home/ubuntu/lumos/lumoskit/external
sudo chmod -R g+rX \
  /home/ubuntu/lumos/lumoskit/scripts \
  /home/ubuntu/lumos/lumoskit/prompts \
  /home/ubuntu/lumos/lumoskit/external
sudo find /home/ubuntu/lumos/lumoskit/scripts \
  /home/ubuntu/lumos/lumoskit/prompts \
  /home/ubuntu/lumos/lumoskit/external \
  -type d -exec chmod g+s {} +
```

Then restart:

```bash
sudo systemctl daemon-reload
sudo systemctl restart backlight
```

## 3. Env files

Required Backlight values in `/srv/backlight/env/backlight.env`:

- `HELIOS_API_TOKEN`
- `HELIOS_DB_PATH=/srv/backlight/data/backlight.db`
- `HELIOS_OUTPUT_ROOT=/srv/backlight/data/outputs`
- `HELIOS_LUMOSKIT_BIN=/home/ubuntu/lumos/lumoskit/bin/lumoskit`

Set `GH_TOKEN` in `backlight.env` when verified-case GitHub publishing should be
enabled.

Keep LumosKit runtime secrets next to the LumosKit checkout, or install that
file from `/srv/backlight/env/lumoskit.env`:

```bash
sudo install -o root -g backlight -m 0640 /srv/backlight/env/lumoskit.env \
  /home/ubuntu/lumos/lumoskit/.env
```

Required LumosKit value for real runs:

- `ALCHEMY_API_KEY`

`ETHERSCAN_API_KEY` and `OPENAI_API_KEY` are optional depending on which
LumosKit stages and repair lanes are enabled.

Codex SDK auth is user-home scoped. Backlight runs as `backlight` with
`HOME=/srv/backlight`, so service runs read `/srv/backlight/.codex/auth.json`, not
`/home/ubuntu/.codex/auth.json`.

Treat `/srv/backlight/.codex/auth.json` as the canonical runtime auth file. Do
not make the service depend on an operator home directory. When an operator
refreshes a login elsewhere, promote that file explicitly:

```bash
cd /home/ubuntu/lumos/helios
sudo deploy/vm/install-codex-auth.sh /path/to/auth.json
```

For example, if the refreshed login is temporarily in the ubuntu account:

```bash
sudo deploy/vm/install-codex-auth.sh /home/ubuntu/.codex/auth.json
```

The script installs the file to `/srv/backlight/.codex/auth.json` with owner
`backlight:backlight` and mode `0600`. New LumosKit child processes pick it up
without a Backlight restart.

## 4. systemd

The deployed unit should match the active VM layout:

```ini
[Service]
User=backlight
Group=backlight
WorkingDirectory=/srv/backlight
EnvironmentFile=/srv/backlight/env/backlight.env
Environment="HOME=/srv/backlight"
Environment="PATH=/srv/backlight/.foundry/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
Environment="SVM_HOME=/srv/backlight/data/.svm"
Environment="XDG_DATA_HOME=/srv/backlight/data/.local/share"
ExecStart=/srv/backlight/bin/backlight
Restart=always
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=30
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ReadWritePaths=/srv/backlight/data /srv/backlight/logs
UMask=0027
```

Foundry must write compiler caches under `/srv/backlight/data`, because the service
uses `ProtectSystem=full`:

```ini
[Service]
Environment="PATH=/srv/backlight/.foundry/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
Environment="SVM_HOME=/srv/backlight/data/.svm"
Environment="XDG_DATA_HOME=/srv/backlight/data/.local/share"
```

## 5. Smoke test

```bash
curl http://127.0.0.1:8080/healthz

sudo bash -c 'set -a; . /srv/backlight/env/backlight.env; set +a; \
  curl -H "Authorization: Bearer ${HELIOS_API_TOKEN}" \
  http://127.0.0.1:8080/cases'

sudo -u helios /home/ubuntu/lumos/lumoskit/bin/lumoskit --help
```
