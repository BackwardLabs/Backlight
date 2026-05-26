#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Sync, build, install, and optionally restart the single-VM Helios runtime.

Usage:
  sudo HELIOS_REF=<sha-or-tag-or-branch> \
    LUMOSKIT_REF=<sha-or-tag-or-branch> \
    HELIOS_RESTART_SERVICE=true \
    deploy/vm/sync-git-runtime.sh

Defaults:
  WORKSPACE_DIR=/home/ubuntu/lumos
  HELIOS_WORKTREE=$WORKSPACE_DIR/helios
  LUMOSKIT_WORKTREE=$WORKSPACE_DIR/lumoskit
  HELIOS_BASE_DIR=/srv/helios
  HELIOS_SERVICE_USER=helios
  HELIOS_SERVICE_NAME=helios.service
  HELIOS_REF=main
  LUMOSKIT_REF=main
  HELIOS_RESTART_SERVICE=false

This deployment uses the existing local git checkouts under /home/ubuntu/lumos.
It does not clone into /srv/helios/src.
USAGE
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ "$(id -u)" -ne 0 ]]; then
  echo "run as root, for example with sudo" >&2
  exit 1
fi

workspace_dir="${WORKSPACE_DIR:-/home/ubuntu/lumos}"
helios_dir="${HELIOS_WORKTREE:-${workspace_dir}/helios}"
lumoskit_dir="${LUMOSKIT_WORKTREE:-${workspace_dir}/lumoskit}"
base_dir="${HELIOS_BASE_DIR:-/srv/helios}"
service_user="${HELIOS_SERVICE_USER:-helios}"
service_name="${HELIOS_SERVICE_NAME:-helios.service}"
helios_ref="${HELIOS_REF:-main}"
lumoskit_ref="${LUMOSKIT_REF:-main}"
restart_service="${HELIOS_RESTART_SERVICE:-false}"

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "$1 is required" >&2
    exit 1
  fi
}

need_cmd git
need_cmd go
need_cmd cargo
need_cmd install
need_cmd systemctl

if ! id "${service_user}" >/dev/null 2>&1; then
  useradd --system --home "${base_dir}" --shell /usr/sbin/nologin "${service_user}"
fi

if [[ ! -d "${helios_dir}/.git" ]]; then
  echo "missing Helios git checkout: ${helios_dir}" >&2
  exit 1
fi

if [[ ! -d "${lumoskit_dir}/.git" ]]; then
  echo "missing LumosKit git checkout: ${lumoskit_dir}" >&2
  exit 1
fi

git_user_for() {
  stat -c '%U' "$1"
}

run_git() {
  local dir="$1"
  shift
  local owner
  owner="$(git_user_for "${dir}")"
  if [[ "${owner}" == "root" ]]; then
    git -C "${dir}" "$@"
  else
    sudo -u "${owner}" git -C "${dir}" "$@"
  fi
}

run_in_worktree() {
  local dir="$1"
  shift
  local owner
  owner="$(git_user_for "${dir}")"
  if [[ "${owner}" == "root" ]]; then
    (cd "${dir}" && "$@")
  else
    sudo -u "${owner}" -H bash -c 'cd "$1" && shift && exec "$@"' bash "${dir}" "$@"
  fi
}

ensure_clean() {
  local name="$1"
  local dir="$2"
  if [[ -n "$(run_git "${dir}" status --porcelain)" ]]; then
    echo "${name} checkout has local changes; commit or stash them first: ${dir}" >&2
    run_git "${dir}" status --short >&2
    exit 1
  fi
}

reset_lumoskit_deployed_binary_if_only_dirty() {
  local dirty
  dirty="$(run_git "${lumoskit_dir}" status --porcelain)"
  if [[ "${dirty}" == " M bin/lumoskit" ]]; then
    echo "==> Resetting locally installed LumosKit binary before git sync"
    run_git "${lumoskit_dir}" checkout -- bin/lumoskit
  fi
}

sync_checkout() {
  local name="$1"
  local dir="$2"
  local ref="$3"

  if [[ "${dir}" == "${lumoskit_dir}" ]]; then
    reset_lumoskit_deployed_binary_if_only_dirty
  fi
  ensure_clean "${name}" "${dir}"
  echo "==> Fetching ${name}"
  run_git "${dir}" fetch --tags origin '+refs/heads/*:refs/remotes/origin/*'

  if run_git "${dir}" rev-parse --verify --quiet "origin/${ref}" >/dev/null; then
    echo "==> Updating ${name} to origin/${ref}"
    run_git "${dir}" checkout "${ref}"
    run_git "${dir}" pull --ff-only origin "${ref}"
  else
    echo "==> Checking out ${name} ${ref}"
    run_git "${dir}" checkout --detach "${ref}"
  fi

  run_git "${dir}" submodule update --init --recursive
}

mkdir -p \
  "${base_dir}/bin" \
  "${base_dir}/data/outputs" \
  "${base_dir}/data/pre-lumos-seed" \
  "${base_dir}/data/uv-cache" \
  "${base_dir}/data/.svm" \
  "${base_dir}/data/.local/share" \
  "${base_dir}/env" \
  "${base_dir}/logs"

chmod 750 "${base_dir}/data" "${base_dir}/data/outputs" "${base_dir}/logs"
chmod 750 "${base_dir}/data/.svm" "${base_dir}/data/.local" "${base_dir}/data/.local/share"
chown -R "${service_user}:${service_user}" "${base_dir}/data" "${base_dir}/logs"

sync_checkout "Helios" "${helios_dir}" "${helios_ref}"
sync_checkout "LumosKit" "${lumoskit_dir}" "${lumoskit_ref}"

echo "==> Building Helios"
run_in_worktree "${helios_dir}" env CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/helios ./cmd/helios

echo "==> Building LumosKit"
run_in_worktree "${lumoskit_dir}" cargo build --release --bin lumoskit

echo "==> Installing binaries"
install -o root -g root -m 0755 "${helios_dir}/dist/helios" "${base_dir}/bin/helios"
install -o "$(git_user_for "${lumoskit_dir}")" -g "${service_user}" -m 0755 \
  "${lumoskit_dir}/target/release/lumoskit" \
  "${lumoskit_dir}/bin/lumoskit"

if [[ -f "${base_dir}/env/lumoskit.env" ]]; then
  install -o root -g "${service_user}" -m 0640 "${base_dir}/env/lumoskit.env" "${lumoskit_dir}/.env"
fi

# LumosKit is checked out by the operator user, but helios executes its Python
# runtime helpers from the service. Some executable scripts may be 0700 after
# checkout or local edits, so grant the service group read/traverse access.
chgrp -R "${service_user}" "${lumoskit_dir}/scripts"
chmod -R g+rX "${lumoskit_dir}/scripts"
if [[ -f "${lumoskit_dir}/requirements-agent-poc.txt" ]]; then
  chgrp "${service_user}" "${lumoskit_dir}/requirements-agent-poc.txt"
  chmod g+r "${lumoskit_dir}/requirements-agent-poc.txt"
fi

echo "==> Writing systemd unit"
cat >"/etc/systemd/system/${service_name}" <<EOF
[Unit]
Description=Helios orchestrator
After=network-online.target
Wants=network-online.target

[Service]
User=${service_user}
Group=${service_user}
WorkingDirectory=${helios_dir}
EnvironmentFile=${base_dir}/env/helios.env
ExecStart=${base_dir}/bin/helios
Restart=always
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=30
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ReadWritePaths=${base_dir}/data ${base_dir}/logs
UMask=0027

[Install]
WantedBy=multi-user.target
EOF

mkdir -p "/etc/systemd/system/${service_name}.d"
cat >"/etc/systemd/system/${service_name}.d/10-foundry-path.conf" <<EOF
[Service]
Environment="PATH=${base_dir}/.foundry/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
EOF
cat >"/etc/systemd/system/${service_name}.d/20-foundry-cache.conf" <<EOF
[Service]
Environment="SVM_HOME=${base_dir}/data/.svm"
Environment="XDG_DATA_HOME=${base_dir}/data/.local/share"
EOF

systemctl daemon-reload

cat <<EOF
==> Runtime installed
Helios checkout:   ${helios_dir} @ $(run_git "${helios_dir}" rev-parse --short HEAD)
LumosKit checkout: ${lumoskit_dir} @ $(run_git "${lumoskit_dir}" rev-parse --short HEAD)
Helios binary:     ${base_dir}/bin/helios
LumosKit binary:   ${lumoskit_dir}/bin/lumoskit
Service:           ${service_name}

Expected in ${base_dir}/env/helios.env:
HELIOS_LUMOSKIT_BIN=${lumoskit_dir}/bin/lumoskit
EOF

if [[ "${restart_service}" == "true" ]]; then
  echo "==> Restarting ${service_name}"
  systemctl restart "${service_name}"
fi
