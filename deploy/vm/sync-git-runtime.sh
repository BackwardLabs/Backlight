#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Sync, build, install, and optionally restart the single-VM Backlight runtime.

Usage:
  sudo BACKLIGHT_REF=<sha-or-tag-or-branch> \
    LUMOSKIT_REF=<sha-or-tag-or-branch> \
    BACKLIGHT_RESTART_SERVICE=true \
    deploy/vm/sync-git-runtime.sh

Defaults:
  WORKSPACE_DIR=/home/ubuntu/lumos
  BACKLIGHT_WORKTREE=$WORKSPACE_DIR/backlight
  LUMOSKIT_WORKTREE=$WORKSPACE_DIR/lumoskit
  BACKLIGHT_BASE_DIR=/srv/backlight
  BACKLIGHT_SERVICE_USER=backlight
  BACKLIGHT_SERVICE_NAME=backlight.service
  BACKLIGHT_REF=main
  LUMOSKIT_REF=main
  BACKLIGHT_RESTART_SERVICE=false

This deployment uses the existing local git checkouts under /home/ubuntu/lumos.
It does not clone into /srv/backlight/src.
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
helios_dir="${BACKLIGHT_WORKTREE:-${workspace_dir}/backlight}"
lumoskit_dir="${LUMOSKIT_WORKTREE:-${workspace_dir}/lumoskit}"
base_dir="${BACKLIGHT_BASE_DIR:-/srv/backlight}"
service_user="${BACKLIGHT_SERVICE_USER:-backlight}"
service_name="${BACKLIGHT_SERVICE_NAME:-backlight.service}"
helios_ref="${BACKLIGHT_REF:-main}"
lumoskit_ref="${LUMOSKIT_REF:-main}"
restart_service="${BACKLIGHT_RESTART_SERVICE:-false}"

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
  echo "missing Backlight git checkout: ${helios_dir}" >&2
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
    (umask 0027 && git -C "${dir}" "$@")
  else
    sudo -u "${owner}" -H bash -c 'umask 0027; dir="$1"; shift; exec git -C "$dir" "$@"' bash "${dir}" "$@"
  fi
}

run_in_worktree() {
  local dir="$1"
  shift
  local owner
  owner="$(git_user_for "${dir}")"
  if [[ "${owner}" == "root" ]]; then
    (umask 0027 && cd "${dir}" && "$@")
  else
    sudo -u "${owner}" -H bash -c 'umask 0027; cd "$1" && shift && exec "$@"' bash "${dir}" "$@"
  fi
}

grant_lumoskit_runtime_read_access() {
  local path
  local runtime_paths=(
    "${lumoskit_dir}/bin"
    "${lumoskit_dir}/.venv-agents"
    "${lumoskit_dir}/scripts"
    "${lumoskit_dir}/prompts"
    "${lumoskit_dir}/external"
  )

  for path in "${runtime_paths[@]}"; do
    if [[ -e "${path}" ]]; then
      chgrp -R "${service_user}" "${path}"
      chmod -R g+rX "${path}"
      find "${path}" -type d -exec chmod g+s {} +
    fi
  done

  if [[ -f "${lumoskit_dir}/requirements-agent-poc.txt" ]]; then
    chgrp "${service_user}" "${lumoskit_dir}/requirements-agent-poc.txt"
    chmod g+r "${lumoskit_dir}/requirements-agent-poc.txt"
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
  "${base_dir}/logs" \
  "${base_dir}/skills"

chmod 750 "${base_dir}/data" "${base_dir}/data/outputs" "${base_dir}/logs"
chmod 750 "${base_dir}/data/.svm" "${base_dir}/data/.local" "${base_dir}/data/.local/share"
chown -R "${service_user}:${service_user}" "${base_dir}/data" "${base_dir}/logs"

sync_checkout "Backlight" "${helios_dir}" "${helios_ref}"
sync_checkout "LumosKit" "${lumoskit_dir}" "${lumoskit_ref}"

echo "==> Building Backlight"
run_in_worktree "${helios_dir}" env CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/backlight ./cmd/helios

echo "==> Building LumosKit"
run_in_worktree "${lumoskit_dir}" cargo build --release --bin lumoskit

echo "==> Installing binaries"
install -o root -g root -m 0755 "${helios_dir}/dist/backlight" "${base_dir}/bin/backlight"
rm -rf "${base_dir}/skills"
if [[ -d "${helios_dir}/skills" ]]; then
  cp -a "${helios_dir}/skills" "${base_dir}/skills"
  chown -R root:"${service_user}" "${base_dir}/skills"
  chmod -R g+rX "${base_dir}/skills"
fi
install -o "$(git_user_for "${lumoskit_dir}")" -g "${service_user}" -m 0755 \
  "${lumoskit_dir}/target/release/lumoskit" \
  "${lumoskit_dir}/bin/lumoskit"

if [[ -f "${base_dir}/env/lumoskit.env" ]]; then
  install -o root -g "${service_user}" -m 0640 "${base_dir}/env/lumoskit.env" "${lumoskit_dir}/.env"
fi

# LumosKit is checked out by the operator user, but Backlight executes Python
# runtime helpers and reads prompt/RCA skill files from the checkout. Git does
# not preserve owner/group policy, so refresh these permissions after every sync.
grant_lumoskit_runtime_read_access

echo "==> Writing systemd unit"
cat >"/etc/systemd/system/${service_name}" <<EOF
[Unit]
Description=Backlight orchestrator
After=network-online.target
Wants=network-online.target

[Service]
User=${service_user}
Group=${service_user}
WorkingDirectory=${base_dir}
EnvironmentFile=${base_dir}/env/backlight.env
Environment="HOME=${base_dir}"
Environment="PATH=${base_dir}/.foundry/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
Environment="SVM_HOME=${base_dir}/data/.svm"
Environment="XDG_DATA_HOME=${base_dir}/data/.local/share"
ExecStart=${base_dir}/bin/backlight
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

systemctl daemon-reload

cat <<EOF
==> Runtime installed
Backlight checkout:   ${helios_dir} @ $(run_git "${helios_dir}" rev-parse --short HEAD)
LumosKit checkout: ${lumoskit_dir} @ $(run_git "${lumoskit_dir}" rev-parse --short HEAD)
Backlight binary:     ${base_dir}/bin/backlight
LumosKit binary:   ${lumoskit_dir}/bin/lumoskit
Service:           ${service_name}

Expected in ${base_dir}/env/backlight.env:
BACKLIGHT_LUMOSKIT_BIN=${lumoskit_dir}/bin/lumoskit
EOF

if [[ "${restart_service}" == "true" ]]; then
  echo "==> Restarting ${service_name}"
  systemctl restart "${service_name}"
fi
