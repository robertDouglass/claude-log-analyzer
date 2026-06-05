#!/usr/bin/env bash
set -euo pipefail

# Start a per-run Headroom proxy for the optimized benchmark side only.
# This avoids `headroom wrap` and global agent configuration changes.

ANALYZER_REPO="${ANALYZER_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
OUT_DIR="${OUT_DIR:?OUT_DIR must be set by benchmark harness}"
HEADROOM_TOOL_ROOT="${HEADROOM_TOOL_ROOT:-$ANALYZER_REPO/.data/benchmarks/external-tools}"
HEADROOM_VENV="${HEADROOM_VENV:-$HEADROOM_TOOL_ROOT/headroom-venv}"
PROXY_ROOT="${HEADROOM_PROXY_ROOT:-$OUT_DIR/headroom-proxy}"
ENV_FILE="${OPTIMIZED_ENV_FILE:-$OUT_DIR/optimized-env.sh}"

"$ANALYZER_REPO/scripts/setup-headroom-benchmark.sh" >/dev/null

rm -rf "$PROXY_ROOT"
mkdir -p "$PROXY_ROOT/workspace" "$PROXY_ROOT/config" "$PROXY_ROOT/logs"

port="$("$HEADROOM_VENV/bin/python" - <<'PY'
import socket

with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)"

export HEADROOM_TELEMETRY=off
export HEADROOM_TELEMETRY_WARN=off
export HEADROOM_WORKSPACE_DIR="$PROXY_ROOT/workspace"
export HEADROOM_CONFIG_DIR="$PROXY_ROOT/config"
export HEADROOM_LOG_FILE="$PROXY_ROOT/logs/proxy.jsonl"
export HEADROOM_NO_SUBSCRIPTION_TRACKING=true
export HEADROOM_AGENT_TYPE=claude
export HEADROOM_STACK=benchmark_claude_proxy

"$HEADROOM_VENV/bin/headroom" proxy \
  --host 127.0.0.1 \
  --port "$port" \
  --mode token \
  --no-telemetry \
  --no-subscription-tracking \
  --log-file "$HEADROOM_LOG_FILE" \
  >"$PROXY_ROOT/proxy.stdout.txt" \
  2>"$PROXY_ROOT/proxy.stderr.txt" &

pid="$!"
printf '%s\n' "$pid" >"$PROXY_ROOT/proxy.pid"
printf '%s\n' "$port" >"$PROXY_ROOT/proxy.port"

cleanup_on_start_failure() {
  if kill -0 "$pid" >/dev/null 2>&1; then
    kill "$pid" >/dev/null 2>&1 || true
  fi
}

ready=0
for _ in $(seq 1 90); do
  if curl -fsS "http://127.0.0.1:$port/livez" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! kill -0 "$pid" >/dev/null 2>&1; then
    echo "Headroom proxy exited before becoming ready; see $PROXY_ROOT/proxy.stderr.txt" >&2
    exit 1
  fi
  sleep 1
done

if [[ "$ready" != "1" ]]; then
  cleanup_on_start_failure
  echo "Headroom proxy did not become ready on 127.0.0.1:$port" >&2
  exit 1
fi

cat >"$ENV_FILE" <<EOF
export ANTHROPIC_BASE_URL=http://127.0.0.1:$port
EOF

echo "Headroom proxy ready on 127.0.0.1:$port"
