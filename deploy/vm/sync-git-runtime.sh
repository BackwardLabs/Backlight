#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Sync Helios and LumosKit git checkouts on a VM.

Usage:
  sudo HELIOS_REF=<sha-or-tag> LUMOSKIT_REF=<sha-or-tag> deploy/vm/sync-git-runtime.sh

Environment:
  HELIOS_REPO=https://github.com/UPside-Lumos-V2/helios.git
  LUMOSKIT_REPO=https://github.com/UPside-Lumos-V2/lumoskit.git
  HELIOS_REF=main
  LUMOSKIT_REF=main
  HELIOS_BASE_DIR=/srv/helios
  HELIOS_SERVICE_USER=helios
  HELIOS_RESTART_SERVICE=false

This script does not build Go or Rust. It uses git only to place the runtime
source trees and the LumosKit committed binary/scripts on disk. Install the
Helios binaries separately into /srv/helios/bin, for example from CI artifacts.
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

base_dir="${HELIOS_BASE_DIR:-/srv/helios}"
service_user="${HELIOS_SERVICE_USER:-helios}"
helios_repo="${HELIOS_REPO:-https://github.com/UPside-Lumos-V2/helios.git}"
lumoskit_repo="${LUMOSKIT_REPO:-https://github.com/UPside-Lumos-V2/lumoskit.git}"
helios_ref="${HELIOS_REF:-main}"
lumoskit_ref="${LUMOSKIT_REF:-main}"
restart_service="${HELIOS_RESTART_SERVICE:-false}"

if ! command -v git >/dev/null 2>&1; then
  echo "git is required" >&2
  exit 1
fi

if ! id "${service_user}" >/dev/null 2>&1; then
  useradd --system --home "${base_dir}" --shell /usr/sbin/nologin "${service_user}"
fi

mkdir -p \
  "${base_dir}/bin" \
  "${base_dir}/src" \
  "${base_dir}/data/outputs" \
  "${base_dir}/data/pre-lumos-seed" \
  "${base_dir}/data/uv-cache" \
  "${base_dir}/env" \
  "${base_dir}/logs"
chmod 750 "${base_dir}" "${base_dir}/data" "${base_dir}/data/outputs" "${base_dir}/logs"
chmod 700 "${base_dir}/env"

sync_repo() {
  local repo="$1"
  local ref="$2"
  local dir="$3"

  if [[ ! -d "${dir}/.git" ]]; then
    rm -rf "${dir}"
    git clone "${repo}" "${dir}"
  fi

  git -C "${dir}" fetch --tags origin '+refs/heads/*:refs/remotes/origin/*'
  if git -C "${dir}" rev-parse --verify --quiet "origin/${ref}" >/dev/null; then
    git -C "${dir}" checkout --detach "origin/${ref}"
  else
    git -C "${dir}" checkout --detach "${ref}"
  fi
  git -C "${dir}" submodule update --init --recursive
}

echo "==> Syncing Helios ${helios_ref}"
sync_repo "${helios_repo}" "${helios_ref}" "${base_dir}/src/helios"

echo "==> Syncing LumosKit ${lumoskit_ref}"
sync_repo "${lumoskit_repo}" "${lumoskit_ref}" "${base_dir}/src/lumoskit"

if [[ ! -x "${base_dir}/src/lumoskit/bin/lumoskit" ]]; then
  echo "missing executable ${base_dir}/src/lumoskit/bin/lumoskit" >&2
  exit 1
fi

lumoskit_arch_mismatch=false
if command -v file >/dev/null 2>&1; then
  host_arch="$(uname -m)"
  lumoskit_file="$(file -b "${base_dir}/src/lumoskit/bin/lumoskit")"
  case "${host_arch}" in
    aarch64|arm64)
      if echo "${lumoskit_file}" | grep -qi 'x86-64'; then
        cat >&2 <<EOF
LumosKit binary architecture mismatch:
  host: ${host_arch}
  binary: ${lumoskit_file}

Build or install a Linux ARM64 LumosKit binary, then place it at:
  ${base_dir}/src/lumoskit/bin/lumoskit
EOF
        lumoskit_arch_mismatch=true
      fi
      ;;
    x86_64|amd64)
      if echo "${lumoskit_file}" | grep -Eqi 'aarch64|ARM64|ARM aarch64'; then
        cat >&2 <<EOF
LumosKit binary architecture mismatch:
  host: ${host_arch}
  binary: ${lumoskit_file}
EOF
        lumoskit_arch_mismatch=true
      fi
      ;;
  esac
fi

ln -sfn "${base_dir}/src/lumoskit/bin/lumoskit" "${base_dir}/bin/lumoskit"

if [[ -f "${base_dir}/env/lumoskit.env" ]]; then
  ln -sfn "${base_dir}/env/lumoskit.env" "${base_dir}/src/lumoskit/.env"
else
  echo "warning: ${base_dir}/env/lumoskit.env does not exist; LumosKit needs ALCHEMY_API_KEY in .env or inherited env for real runs" >&2
fi

# Pre-Lumos defaults in deploy/env/helios.env.example resolve from the Helios
# working directory, but these links make manual inspection under /srv/helios
# convenient and preserve compatibility with older env files.
ln -sfn "${base_dir}/src/helios/scripts" "${base_dir}/scripts"
ln -sfn "${base_dir}/src/helios/skills" "${base_dir}/skills"

chown -R "${service_user}:${service_user}" "${base_dir}/src" "${base_dir}/data" "${base_dir}/env" "${base_dir}/logs"

cat <<EOF
==> Synced runtime checkouts
Helios:   ${base_dir}/src/helios @ $(git -C "${base_dir}/src/helios" rev-parse --short HEAD)
LumosKit: ${base_dir}/src/lumoskit @ $(git -C "${base_dir}/src/lumoskit" rev-parse --short HEAD)

Set in /srv/helios/env/helios.env:
HELIOS_LUMOSKIT_BIN=${base_dir}/src/lumoskit/bin/lumoskit

Set LumosKit runtime secrets in:
${base_dir}/env/lumoskit.env
EOF

if [[ "${lumoskit_arch_mismatch}" == "true" ]]; then
  cat >&2 <<EOF

warning: LumosKit binary does not match this VM architecture.
Replace ${base_dir}/src/lumoskit/bin/lumoskit with a native build before starting Helios.
EOF
fi

if [[ "${restart_service}" == "true" ]]; then
  if [[ "${lumoskit_arch_mismatch}" == "true" ]]; then
    echo "refusing to restart helios.service while LumosKit binary architecture mismatches host" >&2
    exit 1
  fi
  systemctl daemon-reload || true
  systemctl restart helios.service
fi
