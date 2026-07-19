#!/usr/bin/env bash
# fake-lumoskit.sh — local stand-in for bin/lumoskit used only by smoke tests.
#
# Honours the ADR-0018 surface (--tx / --chain / --output-root) plus an extra
# BACKLIGHT_FAKE_SCENARIO env that picks which summary.json shape (or non-shape)
# to write. Real lumoskit ignores BACKLIGHT_FAKE_SCENARIO.
set -euo pipefail

OUTPUT_ROOT=""
TX=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tx) TX="$2"; shift; shift ;;
    --chain) shift; shift ;;
    --output-root) OUTPUT_ROOT="$2"; shift; shift ;;
    *) shift ;;
  esac
done

if [[ -z "${OUTPUT_ROOT}" ]]; then
  echo "fake-lumoskit: --output-root is required" >&2
  exit 64
fi

mkdir -p "${OUTPUT_ROOT}"
SUMMARY="${OUTPUT_ROOT}/summary.json"

# When BACKLIGHT_FAKE_HANG_FILE is set and the file exists, sleep for a long time
# (or until killed) before doing any other work. The smoke test for restart
# recovery uses this to leave a case in state=running while helios is
# SIGKILL'd, then deletes the file so the next run completes normally.
if [[ -n "${BACKLIGHT_FAKE_HANG_FILE:-}" && -f "${BACKLIGHT_FAKE_HANG_FILE}" ]]; then
  sleep 120
fi

# Scenario selection precedence: explicit env wins, otherwise the last two hex
# chars of --tx map to a scenario so a single helios process can exercise all
# rules. Default is `verified` so a real tx_hash works in dev.
SCENARIO="${BACKLIGHT_FAKE_SCENARIO:-}"
if [[ -z "${SCENARIO}" && -n "${TX}" ]]; then
  LAST2="${TX: -2}"
  case "${LAST2}" in
    01) SCENARIO="verified" ;;
    02) SCENARIO="partial" ;;
    03) SCENARIO="unverified" ;;
    04) SCENARIO="engine_reported" ;;
    05) SCENARIO="nonzero" ;;
    06) SCENARIO="missing" ;;
    07) SCENARIO="unreadable" ;;
    08) SCENARIO="unexpected" ;;
    *)  SCENARIO="verified" ;;
  esac
fi
SCENARIO="${SCENARIO:-verified}"

case "${SCENARIO}" in
  verified)
    cat >"${SUMMARY}" <<'JSON'
{"status":"pass","poc":{"status":"verified"},"failure":{}}
JSON
    exit 0
    ;;
  partial)
    cat >"${SUMMARY}" <<'JSON'
{"status":"partial","poc":{"status":"unverified"},"failure":{}}
JSON
    exit 0
    ;;
  unverified)
    cat >"${SUMMARY}" <<'JSON'
{"status":"fail","poc":{"status":"unverified"},"failure":{}}
JSON
    exit 0
    ;;
  engine_reported)
    cat >"${SUMMARY}" <<'JSON'
{"status":"fail","poc":{"status":"missing"},"failure":{"kind":"rpc_timeout","message":"upstream RPC took too long"}}
JSON
    exit 0
    ;;
  nonzero)
    echo "fake-lumoskit: scenario=nonzero (no summary written)" >&2
    exit 17
    ;;
  missing)
    # exit 0 but write nothing
    exit 0
    ;;
  unreadable)
    printf 'this-is-not-json{{{\n' >"${SUMMARY}"
    exit 0
    ;;
  unexpected)
    cat >"${SUMMARY}" <<'JSON'
{"status":"weird","poc":{"status":"???"},"failure":{}}
JSON
    exit 0
    ;;
  *)
    echo "fake-lumoskit: unknown BACKLIGHT_FAKE_SCENARIO=${SCENARIO}" >&2
    exit 65
    ;;
esac
