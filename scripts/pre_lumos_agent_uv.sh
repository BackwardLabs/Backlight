#!/bin/sh
set -eu

repo_dir="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$repo_dir"

exec uv run --with-requirements requirements-pre-lumos.txt python "$@"
