#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Run a benchmark harness repeatedly in fresh baseline/optimized contexts and aggregate the results.

Required:
  TASK_PROMPT_FILE=/path/to/task-prompt.txt

Optional:
  HARNESS=claude|codex              default: claude
  REPEATS=3                         default: 3
  OUT_DIR=.data/benchmarks/suites/<name>
  RUN_NAME=<label>                  default: repeat-<timestamp>
  MANIFEST_ONLY=1                   write manifest.json and exit without launching agents

All other benchmark variables are passed through to the selected harness:
  SOURCE_REPO, BASE_REF, QUALITY_COMMAND, BENCHMARK_SETUP_COMMAND, OPTIMIZED_GUIDANCE_FILE,
  CLAUDE_ARGS, BASELINE_CLAUDE_ARGS, OPTIMIZED_CLAUDE_ARGS,
  CODEX_ARGS, BASELINE_CODEX_ARGS, OPTIMIZED_CODEX_ARGS, etc.

Each repeat invokes the underlying harness once, creating fresh git worktrees
and fresh Claude/Codex task sessions for that baseline/optimized pair. The
suite writes:
  manifest.json
  run-01/comparison.json
  run-02/comparison.json
  run-03/comparison.json
  aggregate.json
USAGE
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ -z "${TASK_PROMPT_FILE:-}" ]]; then
  usage >&2
  exit 2
fi

HARNESS="${HARNESS:-claude}"
REPEATS="${REPEATS:-3}"
RUN_NAME="${RUN_NAME:-repeat-$(date -u +%Y%m%dT%H%M%SZ)}"
ANALYZER_REPO="${ANALYZER_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
OUT_DIR="${OUT_DIR:-$ANALYZER_REPO/.data/benchmarks/suites/$RUN_NAME}"

if ! [[ "$REPEATS" =~ ^[0-9]+$ ]] || [[ "$REPEATS" -lt 1 ]]; then
  echo "REPEATS must be a positive integer" >&2
  exit 2
fi

case "$HARNESS" in
  claude) HARNESS_SCRIPT="$ANALYZER_REPO/scripts/benchmark-claude-p.sh" ;;
  codex) HARNESS_SCRIPT="$ANALYZER_REPO/scripts/benchmark-codex-exec.sh" ;;
  *)
    echo "HARNESS must be claude or codex" >&2
    exit 2
    ;;
esac

mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"
ANALYZER_REPO="$(cd "$ANALYZER_REPO" && pwd)"
TASK_PROMPT_FILE="$(cd "$(dirname "$TASK_PROMPT_FILE")" && pwd)/$(basename "$TASK_PROMPT_FILE")"
export TASK_PROMPT_FILE
export ANALYZER_REPO

write_manifest() {
  python3 - "$OUT_DIR" "$RUN_NAME" "$HARNESS" "$REPEATS" "$TASK_PROMPT_FILE" "$ANALYZER_REPO" <<'PY'
import datetime as dt
import hashlib
import json
import os
import platform
import shutil
import subprocess
import sys
from pathlib import Path

out_dir = Path(sys.argv[1])
run_name = sys.argv[2]
harness = sys.argv[3]
repeats = int(sys.argv[4])
task_prompt_file = sys.argv[5]
analyzer_repo = sys.argv[6]

keys = [
    "BENCHMARK_SUITE_FILE",
    "BENCHMARK_SUITE_SHA256",
    "BENCHMARK_TARGET_COMMIT",
    "BENCHMARK_TARGET_PREPARE_COMMAND",
    "BENCHMARK_TASK_PROMPT_SHA256",
    "BENCHMARK_GUIDANCE_SHA256",
    "BENCHMARK_PRE_TASK_PROMPT_SHA256",
    "BENCHMARK_MCP_CONFIG_SHA256",
    "SOURCE_REPO",
    "BASE_REF",
    "QUALITY_COMMAND",
    "BENCHMARK_SETUP_COMMAND",
    "OPTIMIZED_GUIDANCE_FILE",
    "OPTIMIZED_SETUP_COMMAND",
    "OPTIMIZED_MCP_CONFIG_FILE",
    "OPTIMIZED_PRE_TASK_PROMPT_FILE",
    "ANALYZE_ALL_CLAUDE_LOGS",
    "AGENT_PLUGIN_ENABLED",
    "TOOLING_REVIEW_ENABLED",
    "CLAUDE_BIN",
    "CLAUDE_ARGS",
    "BASELINE_CLAUDE_ARGS",
    "OPTIMIZED_CLAUDE_ARGS",
    "OPTIMIZED_EXTRA_PLUGIN_DIRS",
    "CODEX_BIN",
    "CODEX_ARGS",
    "BASELINE_CODEX_ARGS",
    "OPTIMIZED_CODEX_ARGS",
    "CODEX_EXEC_ARGS",
    "BASELINE_CODEX_EXEC_ARGS",
    "OPTIMIZED_CODEX_EXEC_ARGS",
]

def clean_value(key, value):
    marker = key.upper()
    if any(secret in marker for secret in ("KEY", "TOKEN", "SECRET", "PASSWORD")):
        return "<redacted>"
    value = value.replace(analyzer_repo, "<repo>")
    value = value.replace("/private/tmp/", "<tmp>/")
    value = value.replace("/tmp/", "<tmp>/")
    return value

def run(args, cwd=None):
    try:
        return subprocess.check_output(args, cwd=cwd, text=True, stderr=subprocess.DEVNULL).strip()
    except Exception:
        return ""

def file_sha256(path_value):
    if not path_value:
        return ""
    path = Path(path_value)
    if not path.exists() or not path.is_file():
        return ""
    return hashlib.sha256(path.read_bytes()).hexdigest()

def tool_version(command, *args):
    resolved = shutil.which(command)
    if not resolved:
        return None
    value = run([command, *args])
    return value.splitlines()[0] if value else "available"

def git_state(path_value):
    path = Path(path_value)
    if not path.exists():
        return None
    if run(["git", "-C", str(path), "rev-parse", "--is-inside-work-tree"]) != "true":
        return None
    status = run(["git", "-C", str(path), "status", "--porcelain=v1"])
    return {
        "commit": run(["git", "-C", str(path), "rev-parse", "HEAD"]),
        "branch": run(["git", "-C", str(path), "branch", "--show-current"]) or "detached",
        "dirty": bool(status),
        "status_entry_count": len([line for line in status.splitlines() if line.strip()]),
    }

env = {key: clean_value(key, os.environ[key]) for key in keys if key in os.environ}
path_hash_keys = [
    "TASK_PROMPT_FILE",
    "OPTIMIZED_GUIDANCE_FILE",
    "OPTIMIZED_MCP_CONFIG_FILE",
    "OPTIMIZED_PRE_TASK_PROMPT_FILE",
]
manifest = {
    "schema_version": "2026-06-04",
    "run_name": run_name,
    "harness": harness,
    "required_repeats": repeats,
    "started_at_utc": dt.datetime.now(dt.UTC).isoformat().replace("+00:00", "Z"),
    "fresh_context_contract": "Each repeat invokes one full baseline/optimized harness run. The harness creates fresh detached git worktrees and launches separate Claude/Codex task sessions for baseline and optimized runs.",
    "task_prompt_file": clean_value("TASK_PROMPT_FILE", task_prompt_file),
    "analyzer_repo": "<repo>",
    "host": {
        "platform": platform.platform(),
        "machine": platform.machine(),
        "python": platform.python_version(),
    },
    "git": {
        "analyzer": git_state(analyzer_repo),
        "source": git_state(os.environ.get("SOURCE_REPO", "")),
    },
    "tool_versions": {
        "git": tool_version("git", "--version"),
        "go": tool_version("go", "version"),
        "python3": tool_version("python3", "--version"),
        "node": tool_version("node", "--version"),
        "npm": tool_version("npm", "--version"),
        "npx": tool_version("npx", "--version"),
        "claude": tool_version(os.environ.get("CLAUDE_BIN", "claude"), "--version"),
        "codex": tool_version(os.environ.get("CODEX_BIN", "codex"), "--version"),
        "uvx": tool_version("uvx", "--version"),
        "rtk": tool_version("rtk", "--version"),
        "grepai": tool_version("grepai", "--version"),
    },
    "file_hashes": {
        key.lower(): file_sha256(os.environ.get(key, ""))
        for key in path_hash_keys
        if os.environ.get(key)
    },
    "environment": env,
}
(out_dir / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
PY
}

write_manifest

if [[ "${MANIFEST_ONLY:-0}" == "1" ]]; then
  echo "Benchmark manifest written to $OUT_DIR/manifest.json"
  exit 0
fi

completed=0
ORIGINAL_OPTIMIZED_MCP_CONFIG_FILE="${OPTIMIZED_MCP_CONFIG_FILE:-}"
for i in $(seq 1 "$REPEATS"); do
  run_id="$(printf 'run-%02d' "$i")"
  run_dir="$OUT_DIR/$run_id"
  rm -rf "$run_dir" "$run_dir 2" "$run_dir 3"
  mkdir -p "$run_dir"
  if [[ -n "$ORIGINAL_OPTIMIZED_MCP_CONFIG_FILE" ]]; then
    run_mcp_config="$run_dir/optimized-mcp-config.json"
    python3 - "$ORIGINAL_OPTIMIZED_MCP_CONFIG_FILE" "$run_mcp_config" "$RUN_NAME" "$run_id" "$$" "$ANALYZER_REPO" "$run_dir" <<'PY'
import json
import re
import sys
from pathlib import Path

source = Path(sys.argv[1])
target = Path(sys.argv[2])
run_name = sys.argv[3]
run_id = sys.argv[4]
pid = sys.argv[5]
analyzer_repo = sys.argv[6]
run_dir = sys.argv[7]

config = json.loads(source.read_text())
suffix = re.sub(r"[^A-Za-z0-9_]", "_", f"{run_name}_{run_id}_{pid}")[-48:]

def expand_placeholders(value):
    if isinstance(value, dict):
        return {key: expand_placeholders(item) for key, item in value.items()}
    if isinstance(value, list):
        return [expand_placeholders(item) for item in value]
    if isinstance(value, str):
        return (
            value
            .replace("${ANALYZER_REPO}", analyzer_repo)
            .replace("$ANALYZER_REPO", analyzer_repo)
            .replace("__BENCHMARK_RUN_DIR__", run_dir)
            .replace("__BENCHMARK_RUN_SUFFIX__", suffix)
        )
    return value

config = expand_placeholders(config)
for server in config.get("mcpServers", {}).values():
    env = server.setdefault("env", {})
    if "CODE_CHUNKS_COLLECTION_NAME_OVERRIDE" in env:
        base = re.sub(r"[^A-Za-z0-9_]", "_", env["CODE_CHUNKS_COLLECTION_NAME_OVERRIDE"])[:40]
        env["CODE_CHUNKS_COLLECTION_NAME_OVERRIDE"] = f"{base}_{suffix}"
target.write_text(json.dumps(config, indent=2) + "\n")
PY
    export OPTIMIZED_MCP_CONFIG_FILE="$run_mcp_config"
  fi
  echo "[$run_id] starting $HARNESS benchmark"
  set +e
  (
    export OUT_DIR="$run_dir"
    export ANALYZER_REPO="$ANALYZER_REPO"
    "$HARNESS_SCRIPT"
  ) >"$run_dir/suite-run.stdout.txt" 2>"$run_dir/suite-run.stderr.txt"
  status=$?
  set -e
  echo "$status" >"$run_dir/suite-run.exit-status"
  if [[ "$status" -eq 0 && -f "$run_dir/comparison.json" ]]; then
    completed=$((completed + 1))
    echo "[$run_id] completed"
  else
    echo "[$run_id] failed with status $status; see $run_dir/suite-run.stderr.txt" >&2
  fi
done

python3 - "$OUT_DIR" "$REPEATS" "$completed" <<'PY'
import datetime as dt
import json
import statistics
import sys
from pathlib import Path

out_dir = Path(sys.argv[1])
required = int(sys.argv[2])
completed = int(sys.argv[3])

comparisons = []
failed = []
for run_dir in sorted(out_dir.glob("run-*")):
    status_path = run_dir / "suite-run.exit-status"
    status = status_path.read_text().strip() if status_path.exists() else "missing"
    comparison_path = run_dir / "comparison.json"
    if status == "0" and comparison_path.exists():
        comparison = json.loads(comparison_path.read_text())
        comparisons.append({"run": run_dir.name, "comparison": comparison})
    else:
        failed.append({"run": run_dir.name, "status": status})

def numeric_delta_keys():
    keys = set()
    for item in comparisons:
        for key, value in item["comparison"].get("delta", {}).items():
            if isinstance(value, (int, float)) and not isinstance(value, bool):
                keys.add(key)
    return sorted(keys)

def stat(values):
    if not values:
        return None
    return {
        "n": len(values),
        "mean": statistics.fmean(values),
        "median": statistics.median(values),
        "min": min(values),
        "max": max(values),
        "stdev": statistics.stdev(values) if len(values) > 1 else 0,
    }

delta_stats = {}
for key in numeric_delta_keys():
    values = [item["comparison"]["delta"][key] for item in comparisons if key in item["comparison"].get("delta", {})]
    delta_stats[key] = stat(values)

quality = []
for item in comparisons:
    comparison = item["comparison"]
    quality.append({
        "run": item["run"],
        "baseline_quality_status": comparison.get("baseline_quality_status"),
        "optimized_quality_status": comparison.get("optimized_quality_status"),
        "baseline_exit_status": comparison.get("baseline_exit_status"),
        "optimized_exit_status": comparison.get("optimized_exit_status"),
    })

aggregate = {
    "schema_version": "2026-05-24",
    "required_repeats": required,
    "completed_repeats": completed,
    "failed_repeats": failed,
    "confidence_policy": "Recommendation verdicts should be promoted from first-pass to repeated only when at least three fresh baseline/optimized pairs complete and pass the same quality gate.",
    "quality": quality,
    "delta_stats": delta_stats,
    "run_dirs": [item["run"] for item in comparisons],
}
(out_dir / "aggregate.json").write_text(json.dumps(aggregate, indent=2) + "\n")

manifest_path = out_dir / "manifest.json"
if manifest_path.exists():
    manifest = json.loads(manifest_path.read_text())
    manifest["completed_at_utc"] = dt.datetime.now(dt.UTC).isoformat().replace("+00:00", "Z")
    manifest["completed_repeats"] = completed
    manifest["failed_repeats"] = failed
    (out_dir / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
PY

echo "Repeat benchmark suite written to $OUT_DIR"
echo "Aggregate: $OUT_DIR/aggregate.json"

if [[ "$completed" -lt "$REPEATS" ]]; then
  exit 1
fi
