#!/usr/bin/env bash
set -euo pipefail

# Prepare Gathon for one optimized benchmark worktree without touching global
# agent configuration. The Claude benchmark passes an explicit MCP config that
# launches the local venv against this per-run worktree.

ANALYZER_REPO="${ANALYZER_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
GATHON_TOOL_ROOT="${GATHON_TOOL_ROOT:-$ANALYZER_REPO/.data/benchmarks/external-tools}"
GATHON_SOURCE="${GATHON_SOURCE:-$GATHON_TOOL_ROOT/gathon}"
GATHON_COMMIT="${GATHON_COMMIT:-0578b83f66ba35ca3d8cc800ff8faf21eaf2ad3c}"
GATHON_VENV="${GATHON_VENV:-$GATHON_TOOL_ROOT/gathon-venv}"
GATHON_TREE_SITTER_LANGUAGE_PACK="${GATHON_TREE_SITTER_LANGUAGE_PACK:-tree-sitter-language-pack==0.9.1}"

select_python() {
  if [[ -n "${GATHON_BENCHMARK_PYTHON:-}" ]]; then
    printf '%s\n' "$GATHON_BENCHMARK_PYTHON"
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
  echo "No supported Python found for Gathon benchmark. Need Python 3.11+." >&2
  exit 2
}

if [[ ! -d "$GATHON_SOURCE/.git" ]]; then
  echo "Missing Gathon checkout at $GATHON_SOURCE. Clone https://github.com/pauldx/gathon there or set GATHON_SOURCE." >&2
  exit 2
fi

actual_commit="$(git -C "$GATHON_SOURCE" rev-parse HEAD)"
if [[ "$actual_commit" != "$GATHON_COMMIT" ]]; then
  echo "Gathon checkout commit mismatch: expected $GATHON_COMMIT, got $actual_commit" >&2
  exit 2
fi

python_bin="$(select_python)"
"$python_bin" - <<'PY'
import sys
if sys.version_info < (3, 11):
    raise SystemExit(
        f"Gathon benchmark requires Python 3.11+; got {sys.version.split()[0]}"
    )
PY

venv_ok=0
if [[ -x "$GATHON_VENV/bin/python" ]]; then
  if "$GATHON_VENV/bin/python" - "$GATHON_SOURCE" <<'PY'
import importlib.metadata
import pathlib
import sys

source = pathlib.Path(sys.argv[1]).resolve()
if sys.version_info < (3, 11):
    raise SystemExit(1)
if importlib.metadata.version("gathon") != "0.1.0":
    raise SystemExit(1)
if importlib.metadata.version("tree-sitter-language-pack") != "0.9.1":
    raise SystemExit(1)
import fastmcp  # noqa: F401
import networkx  # noqa: F401
import tree_sitter_language_pack  # noqa: F401
import gathon  # noqa: F401
package_path = pathlib.Path(gathon.__file__).resolve()
if source not in package_path.parents:
    raise SystemExit(1)
PY
  then
    venv_ok=1
  fi
fi

if [[ "$venv_ok" != "1" ]]; then
  rm -rf "$GATHON_VENV"
  mkdir -p "$GATHON_TOOL_ROOT"
  "$python_bin" -m venv "$GATHON_VENV"
  "$GATHON_VENV/bin/python" -m pip install --quiet --upgrade pip
  "$GATHON_VENV/bin/python" -m pip install --quiet -e "$GATHON_SOURCE" fastmcp "$GATHON_TREE_SITTER_LANGUAGE_PACK"
fi

rm -rf .gathon
"$GATHON_VENV/bin/gathon" build "$PWD" --full --compress full
"$GATHON_VENV/bin/gathon" status "$PWD" >/dev/null
"$GATHON_VENV/bin/gathon" serve "$PWD" --compress ultra --help >/dev/null
