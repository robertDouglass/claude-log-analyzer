#!/usr/bin/env bash
set -euo pipefail

# Prepare Headroom for one optimized benchmark worktree without touching global
# agent configuration. The Claude benchmark passes an explicit MCP config that
# launches the pinned local venv.

ANALYZER_REPO="${ANALYZER_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
HEADROOM_PACKAGE="${HEADROOM_PACKAGE:-headroom-ai[proxy,code]==0.23.0}"
HEADROOM_EXPECTED_VERSION="${HEADROOM_EXPECTED_VERSION:-0.23.0}"
HEADROOM_TOOL_ROOT="${HEADROOM_TOOL_ROOT:-$ANALYZER_REPO/.data/benchmarks/external-tools}"
HEADROOM_VENV="${HEADROOM_VENV:-$HEADROOM_TOOL_ROOT/headroom-venv}"

select_python() {
  if [[ -n "${HEADROOM_BENCHMARK_PYTHON:-}" ]]; then
    printf '%s\n' "$HEADROOM_BENCHMARK_PYTHON"
    return
  fi
  for candidate in \
    /opt/homebrew/bin/python3.13 \
    /opt/homebrew/bin/python3.12 \
    /opt/homebrew/bin/python3.11 \
    python3.13 \
    python3.12 \
    python3.11
  do
    if command -v "$candidate" >/dev/null 2>&1; then
      printf '%s\n' "$candidate"
      return
    fi
  done
  echo "No supported Python found for Headroom benchmark. Need Python 3.10-3.13; Python 3.14 breaks a transitive PyO3 build." >&2
  exit 2
}

python_bin="$(select_python)"
"$python_bin" - <<'PY'
import sys
if sys.version_info < (3, 10) or sys.version_info >= (3, 14):
    raise SystemExit(
        f"Headroom benchmark requires Python 3.10-3.13; got {sys.version.split()[0]}"
    )
PY

venv_ok=0
if [[ -x "$HEADROOM_VENV/bin/python" ]]; then
  if "$HEADROOM_VENV/bin/python" - "$HEADROOM_EXPECTED_VERSION" <<'PY'
import importlib.metadata
import sys

expected = sys.argv[1]
if sys.version_info < (3, 10) or sys.version_info >= (3, 14):
    raise SystemExit(1)
if importlib.metadata.version("headroom-ai") != expected:
    raise SystemExit(1)
import fastapi  # noqa: F401
import mcp  # noqa: F401
import tree_sitter_language_pack  # noqa: F401
PY
  then
    venv_ok=1
  fi
fi

if [[ "$venv_ok" != "1" ]]; then
  rm -rf "$HEADROOM_VENV"
  mkdir -p "$HEADROOM_TOOL_ROOT"
  "$python_bin" -m venv "$HEADROOM_VENV"
  "$HEADROOM_VENV/bin/python" -m pip install --upgrade pip
  HEADROOM_TELEMETRY=off HEADROOM_TELEMETRY_WARN=off \
    "$HEADROOM_VENV/bin/python" -m pip install "$HEADROOM_PACKAGE"
fi

smoke_root="$PWD/.benchmark-headroom-smoke"
rm -rf "$smoke_root"
mkdir -p "$smoke_root/workspace" "$smoke_root/config"

export HEADROOM_TELEMETRY=off
export HEADROOM_TELEMETRY_WARN=off
export HEADROOM_WORKSPACE_DIR="$smoke_root/workspace"
export HEADROOM_CONFIG_DIR="$smoke_root/config"

"$HEADROOM_VENV/bin/headroom" --version
"$HEADROOM_VENV/bin/headroom" mcp serve --help >/dev/null
