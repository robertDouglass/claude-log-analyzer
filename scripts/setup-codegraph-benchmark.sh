#!/usr/bin/env bash
set -euo pipefail

# Prepare CodeGraph for one optimized benchmark worktree without touching global
# agent configuration. The Claude benchmark passes an explicit MCP config that
# launches CodeGraph via pinned npx.

CODEGRAPH_PACKAGE="${CODEGRAPH_PACKAGE:-@colbymchenry/codegraph@0.9.9}"
CODEGRAPH_CACHE_DIR="${CODEGRAPH_CACHE_DIR:-$PWD/.benchmark-codegraph-cache}"

export CODEGRAPH_INSTALL_DIR="$CODEGRAPH_CACHE_DIR"
export CODEGRAPH_NO_DOWNLOAD="${CODEGRAPH_NO_DOWNLOAD:-1}"

npx --yes "$CODEGRAPH_PACKAGE" init -i
npx --yes "$CODEGRAPH_PACKAGE" status

