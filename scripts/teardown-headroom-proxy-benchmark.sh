#!/usr/bin/env bash
set -euo pipefail

OUT_DIR="${OUT_DIR:?OUT_DIR must be set by benchmark harness}"
PROXY_ROOT="${HEADROOM_PROXY_ROOT:-$OUT_DIR/headroom-proxy}"

if [[ -f "$PROXY_ROOT/proxy.port" ]]; then
  port="$(cat "$PROXY_ROOT/proxy.port")"
  curl -fsS "http://127.0.0.1:$port/stats" >"$PROXY_ROOT/proxy-stats.json" 2>"$PROXY_ROOT/proxy-stats.stderr.txt" || true
fi

if [[ -f "$PROXY_ROOT/proxy.pid" ]]; then
  pid="$(cat "$PROXY_ROOT/proxy.pid")"
  if [[ -n "$pid" ]] && kill -0 "$pid" >/dev/null 2>&1; then
    kill "$pid" >/dev/null 2>&1 || true
    for _ in $(seq 1 20); do
      if ! kill -0 "$pid" >/dev/null 2>&1; then
        exit 0
      fi
      sleep 0.5
    done
    kill -9 "$pid" >/dev/null 2>&1 || true
  fi
fi
