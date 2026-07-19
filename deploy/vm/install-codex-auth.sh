#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Install a Codex auth file for the Backlight service user.

Usage:
  sudo deploy/vm/install-codex-auth.sh /path/to/auth.json

Defaults:
  BACKLIGHT_BASE_DIR=/srv/backlight
  BACKLIGHT_SERVICE_USER=backlight

The Codex SDK uses Path.home()/.codex by default. Backlight runs as the
BACKLIGHT_SERVICE_USER with HOME=$BACKLIGHT_BASE_DIR, so service runs read:

  $BACKLIGHT_BASE_DIR/.codex/auth.json

This script copies the explicitly supplied auth file there with service-only
permissions. Do not make the service depend on /home/ubuntu/.codex/auth.json.
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

source_auth="${1:-${SOURCE_AUTH_JSON:-}}"
base_dir="${BACKLIGHT_BASE_DIR:-/srv/backlight}"
service_user="${BACKLIGHT_SERVICE_USER:-backlight}"
dest_dir="${base_dir}/.codex"
dest_auth="${dest_dir}/auth.json"

if [[ -z "${source_auth}" ]]; then
  usage >&2
  exit 64
fi

if [[ ! -f "${source_auth}" ]]; then
  echo "missing source auth file: ${source_auth}" >&2
  exit 1
fi

if ! id "${service_user}" >/dev/null 2>&1; then
  echo "missing service user: ${service_user}" >&2
  exit 1
fi

if command -v jq >/dev/null 2>&1; then
  jq empty "${source_auth}" >/dev/null
fi

install -d -o "${service_user}" -g "${service_user}" -m 0700 "${dest_dir}"
if [[ "$(realpath -e "${source_auth}")" == "$(realpath -m "${dest_auth}")" ]]; then
  chown "${service_user}:${service_user}" "${dest_auth}"
  chmod 0600 "${dest_auth}"
else
  install -o "${service_user}" -g "${service_user}" -m 0600 "${source_auth}" "${dest_auth}"
fi

echo "Installed Codex auth for ${service_user}: ${dest_auth}"
