#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Run a controlled Claude Code -p benchmark for the generated Agent Analyzer plugin.

Required:
  TASK_PROMPT_FILE=/path/to/prompt.txt

Optional:
  SOURCE_REPO=$PWD
  ANALYZER_REPO=<directory containing agent-analyzer source>
  BASE_REF=HEAD
  OUT_DIR=.data/benchmarks/claude-p-plugin-token-savings
  CLAUDE_BIN=claude
  CLAUDE_ARGS="--setting-sources project,local --strict-mcp-config --mcp-config {\\\"mcpServers\\\":{}} --settings {\\\"enabledPlugins\\\":{\\\"sentrux@sentrux-marketplace\\\":false},\\\"permissions\\\":{\\\"defaultMode\\\":\\\"bypassPermissions\\\"}} --permission-mode bypassPermissions --disallowedTools Agent --output-format json --model sonnet --max-budget-usd 15"
  BASELINE_CLAUDE_ARGS="$CLAUDE_ARGS"
  OPTIMIZED_CLAUDE_ARGS="$CLAUDE_ARGS"
  QUALITY_COMMAND="go test ./..."
  BENCHMARK_SETUP_COMMAND="cp -R /path/fixtures .benchmark-fixtures"
  OPTIMIZED_GUIDANCE_FILE=/path/to/guidance.txt
  AGENT_PLUGIN_ENABLED=1
  OPTIMIZED_EXTRA_PLUGIN_DIRS=/path/plugin-a:/path/plugin-b
  OPTIMIZED_SETUP_COMMAND="cp /tmp/CLAUDE.md CLAUDE.md"
  OPTIMIZED_MCP_CONFIG_FILE=/path/to/mcp.json
  OPTIMIZED_PRE_TASK_PROMPT_FILE=/path/to/warmup-prompt.txt
  ANALYZE_ALL_CLAUDE_LOGS=1
  TOOLING_REVIEW_ENABLED=1

The script creates two isolated git worktrees from the same commit, runs the
same prompt in baseline and plugin-assisted sessions, analyzes both local logs,
and writes sanitized reports plus a comparison JSON.
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

SOURCE_REPO="${SOURCE_REPO:-$PWD}"
ANALYZER_REPO="${ANALYZER_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
BASE_REF="${BASE_REF:-HEAD}"
OUT_DIR="${OUT_DIR:-.data/benchmarks/claude-p-plugin-token-savings}"
CLAUDE_BIN="${CLAUDE_BIN:-claude}"
if [[ -z "${CLAUDE_ARGS+x}" ]]; then
  CLAUDE_ARGS='--setting-sources project,local --strict-mcp-config --mcp-config {"mcpServers":{}} --settings {"enabledPlugins":{"sentrux@sentrux-marketplace":false},"permissions":{"defaultMode":"bypassPermissions"}} --permission-mode bypassPermissions --disallowedTools Agent --output-format json --model sonnet --max-budget-usd 15'
fi
BASELINE_CLAUDE_ARGS="${BASELINE_CLAUDE_ARGS:-$CLAUDE_ARGS}"
OPTIMIZED_CLAUDE_ARGS="${OPTIMIZED_CLAUDE_ARGS:-$CLAUDE_ARGS}"
QUALITY_COMMAND="${QUALITY_COMMAND:-}"
BENCHMARK_SETUP_COMMAND="${BENCHMARK_SETUP_COMMAND:-}"
OPTIMIZED_GUIDANCE_FILE="${OPTIMIZED_GUIDANCE_FILE:-}"
AGENT_PLUGIN_ENABLED="${AGENT_PLUGIN_ENABLED:-1}"
OPTIMIZED_EXTRA_PLUGIN_DIRS="${OPTIMIZED_EXTRA_PLUGIN_DIRS:-}"
OPTIMIZED_SETUP_COMMAND="${OPTIMIZED_SETUP_COMMAND:-}"
OPTIMIZED_MCP_CONFIG_FILE="${OPTIMIZED_MCP_CONFIG_FILE:-}"
OPTIMIZED_PRE_TASK_PROMPT_FILE="${OPTIMIZED_PRE_TASK_PROMPT_FILE:-}"
TOOLING_REVIEW_ENABLED="${TOOLING_REVIEW_ENABLED:-1}"
ANALYZE_ALL_CLAUDE_LOGS="${ANALYZE_ALL_CLAUDE_LOGS:-0}"

if [[ ! -f "$TASK_PROMPT_FILE" ]]; then
  echo "TASK_PROMPT_FILE does not exist: $TASK_PROMPT_FILE" >&2
  exit 2
fi

if [[ -n "$OPTIMIZED_GUIDANCE_FILE" && ! -f "$OPTIMIZED_GUIDANCE_FILE" ]]; then
  echo "OPTIMIZED_GUIDANCE_FILE does not exist: $OPTIMIZED_GUIDANCE_FILE" >&2
  exit 2
fi
if [[ "$AGENT_PLUGIN_ENABLED" != "0" && "$AGENT_PLUGIN_ENABLED" != "1" ]]; then
  echo "AGENT_PLUGIN_ENABLED must be 0 or 1" >&2
  exit 2
fi
if [[ -n "$OPTIMIZED_MCP_CONFIG_FILE" && ! -f "$OPTIMIZED_MCP_CONFIG_FILE" ]]; then
  echo "OPTIMIZED_MCP_CONFIG_FILE does not exist: $OPTIMIZED_MCP_CONFIG_FILE" >&2
  exit 2
fi
if [[ -n "$OPTIMIZED_PRE_TASK_PROMPT_FILE" && ! -f "$OPTIMIZED_PRE_TASK_PROMPT_FILE" ]]; then
  echo "OPTIMIZED_PRE_TASK_PROMPT_FILE does not exist: $OPTIMIZED_PRE_TASK_PROMPT_FILE" >&2
  exit 2
fi
if [[ "$TOOLING_REVIEW_ENABLED" != "0" && "$TOOLING_REVIEW_ENABLED" != "1" ]]; then
  echo "TOOLING_REVIEW_ENABLED must be 0 or 1" >&2
  exit 2
fi

if ! command -v "$CLAUDE_BIN" >/dev/null 2>&1; then
  echo "Claude Code binary not found: $CLAUDE_BIN" >&2
  exit 2
fi

mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"
SOURCE_REPO="$(cd "$SOURCE_REPO" && pwd)"
ANALYZER_REPO="$(cd "$ANALYZER_REPO" && pwd)"
PROMPT_COPY="$OUT_DIR/task-prompt.txt"
cp "$TASK_PROMPT_FILE" "$PROMPT_COPY"
if [[ -n "$OPTIMIZED_GUIDANCE_FILE" ]]; then
  cp "$OPTIMIZED_GUIDANCE_FILE" "$OUT_DIR/optimized-guidance.txt"
fi
if [[ -n "$OPTIMIZED_PRE_TASK_PROMPT_FILE" ]]; then
  cp "$OPTIMIZED_PRE_TASK_PROMPT_FILE" "$OUT_DIR/optimized-pre-task-prompt.txt"
fi
printf '%s\n' "$BASELINE_CLAUDE_ARGS" >"$OUT_DIR/baseline-claude-args.txt"
printf '%s\n' "$OPTIMIZED_CLAUDE_ARGS" >"$OUT_DIR/optimized-claude-args.txt"

FIXED_COMMIT="$(git -C "$SOURCE_REPO" rev-parse "$BASE_REF")"
BASELINE_WT="$OUT_DIR/worktree-baseline"
OPTIMIZED_WT="$OUT_DIR/worktree-optimized"
PLUGIN_ZIP="$OUT_DIR/agent-analyzer-plugin.zip"

rm -rf "$BASELINE_WT" "$OPTIMIZED_WT"
git -C "$SOURCE_REPO" worktree prune
git -C "$SOURCE_REPO" worktree add --detach "$BASELINE_WT" "$FIXED_COMMIT"
git -C "$SOURCE_REPO" worktree add --detach "$OPTIMIZED_WT" "$FIXED_COMMIT"
if [[ -n "$BENCHMARK_SETUP_COMMAND" ]]; then
  (cd "$BASELINE_WT" && bash -lc "$BENCHMARK_SETUP_COMMAND") >"$OUT_DIR/baseline-setup.stdout.txt" 2>"$OUT_DIR/baseline-setup.stderr.txt"
  (cd "$OPTIMIZED_WT" && bash -lc "$BENCHMARK_SETUP_COMMAND") >"$OUT_DIR/optimized-common-setup.stdout.txt" 2>"$OUT_DIR/optimized-common-setup.stderr.txt"
fi
if [[ -n "$OPTIMIZED_SETUP_COMMAND" ]]; then
  (cd "$OPTIMIZED_WT" && bash -lc "$OPTIMIZED_SETUP_COMMAND") >"$OUT_DIR/optimized-setup.stdout.txt" 2>"$OUT_DIR/optimized-setup.stderr.txt"
fi

latest_log_after() {
  local marker="$1"
  python3 - "$marker" <<'PY'
import sys
from pathlib import Path

marker = Path(sys.argv[1]).stat().st_mtime
logs = [p for p in Path.home().glob(".claude/projects/**/*.jsonl") if p.stat().st_mtime >= marker]
if not logs:
    raise SystemExit("no Claude Code JSONL log found after benchmark marker")
print(max(logs, key=lambda p: p.stat().st_mtime))
PY
}

log_for_session_id() {
  local session_id="$1"
  python3 - "$session_id" <<'PY'
import sys
from pathlib import Path

session_id = sys.argv[1]
matches = list(Path.home().glob(f".claude/projects/**/{session_id}.jsonl"))
if not matches:
    raise SystemExit(f"no Claude Code JSONL log found for session {session_id}")
print(max(matches, key=lambda p: p.stat().st_mtime))
PY
}

logs_after_marker_for_worktree() {
  local marker="$1"
  local worktree="$2"
  python3 - "$marker" "$worktree" <<'PY'
import sys
from pathlib import Path

marker = Path(sys.argv[1]).stat().st_mtime
worktree = Path(sys.argv[2]).resolve()
encoded = "".join(ch if ch.isalnum() else "-" for ch in str(worktree))
logs = [
    p for p in Path.home().glob(".claude/projects/**/*.jsonl")
    if p.stat().st_mtime >= marker and encoded in str(p)
]
for path in sorted(logs, key=lambda p: p.stat().st_mtime):
    print(path)
PY
}

run_claude_task() {
  local label="$1"
  local worktree="$2"
  local plugin_arg="${3:-}"
  local guidance_path="${4:-}"
  local marker="$OUT_DIR/$label.marker"
  local stdout_path="$OUT_DIR/$label.stdout.json"
  local stderr_path="$OUT_DIR/$label.stderr.txt"
  local status_path="$OUT_DIR/$label.exit-status"
  local claude_args="$BASELINE_CLAUDE_ARGS"
  local prompt
  local plugin_flags=()
  local mcp_flags=()

  prompt="$(cat "$PROMPT_COPY")"
  if [[ -n "$guidance_path" ]]; then
    prompt="$(cat "$guidance_path")

Task prompt, unchanged from baseline:

$prompt"
  fi
  if [[ "$label" == "optimized" ]]; then
    claude_args="$OPTIMIZED_CLAUDE_ARGS"
  fi

  touch "$marker"
  if [[ -n "$plugin_arg" ]]; then
    plugin_flags+=(--plugin-dir "$plugin_arg")
  fi
  if [[ -n "$OPTIMIZED_EXTRA_PLUGIN_DIRS" && "$label" == "optimized" ]]; then
    IFS=':' read -r -a extra_plugin_dirs <<<"$OPTIMIZED_EXTRA_PLUGIN_DIRS"
    for extra_plugin_dir in "${extra_plugin_dirs[@]}"; do
      if [[ -n "$extra_plugin_dir" ]]; then
        plugin_flags+=(--plugin-dir "$extra_plugin_dir")
      fi
    done
  fi
  if [[ -n "$OPTIMIZED_MCP_CONFIG_FILE" && "$label" == "optimized" ]]; then
    mcp_flags+=(--mcp-config "$OPTIMIZED_MCP_CONFIG_FILE")
  fi
  {
    printf '%s' "$claude_args"
    if [[ "${#plugin_flags[@]}" -gt 0 ]]; then
      printf ' %q' "${plugin_flags[@]}"
    fi
    if [[ "${#mcp_flags[@]}" -gt 0 ]]; then
      printf ' %q' "${mcp_flags[@]}"
    fi
    printf '\n'
  } >"$OUT_DIR/$label-claude-actual-args.txt"
  set +e
  (cd "$worktree" && "$CLAUDE_BIN" ${plugin_flags[@]+"${plugin_flags[@]}"} $claude_args ${mcp_flags[@]+"${mcp_flags[@]}"} -p "$prompt") >"$stdout_path" 2>"$stderr_path"
  local status=$?
  set -e
  echo "$status" >"$status_path"
  python3 - "$stdout_path" "$label" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
label = sys.argv[2]
try:
    result = json.loads(path.read_text())
except Exception as exc:
    raise SystemExit(f"{label}: Claude output was not JSON: {exc}")
if result.get("is_error"):
    raise SystemExit(f"{label}: Claude returned is_error=true: {result.get('result')}")
PY
  local session_id
  session_id="$(python3 - "$stdout_path" <<'PY'
import json
import sys
from pathlib import Path

print(json.loads(Path(sys.argv[1]).read_text()).get("session_id", ""))
PY
)"
  if [[ -n "$session_id" ]]; then
    log_for_session_id "$session_id" >"$OUT_DIR/$label.log-path"
  else
    latest_log_after "$marker" >"$OUT_DIR/$label.log-path"
  fi
  if [[ "$ANALYZE_ALL_CLAUDE_LOGS" == "1" ]]; then
    logs_after_marker_for_worktree "$marker" "$worktree" >"$OUT_DIR/$label.log-paths"
    if ! grep -Fxq "$(cat "$OUT_DIR/$label.log-path")" "$OUT_DIR/$label.log-paths"; then
      cat "$OUT_DIR/$label.log-path" >>"$OUT_DIR/$label.log-paths"
    fi
  fi
}

analyze_log() {
  local label="$1"
  local log_path
  if [[ "$ANALYZE_ALL_CLAUDE_LOGS" == "1" && -s "$OUT_DIR/$label.log-paths" ]]; then
    local report_dir="$OUT_DIR/$label-reports"
    rm -rf "$report_dir"
    mkdir -p "$report_dir"
    local index=0
    while IFS= read -r log_path; do
      [[ -z "$log_path" ]] && continue
      index=$((index + 1))
      go run "$ANALYZER_REPO/cmd/agent-analyzer" analyze \
        --log "$log_path" \
        --out "$report_dir/report-$(printf '%02d' "$index").json" >"$report_dir/report-$(printf '%02d' "$index").txt"
    done <"$OUT_DIR/$label.log-paths"
    python3 - "$report_dir" "$OUT_DIR/$label-report.json" <<'PY'
import json
import sys
from pathlib import Path

report_dir = Path(sys.argv[1])
out_path = Path(sys.argv[2])
reports = [json.loads(path.read_text()) for path in sorted(report_dir.glob("report-*.json"))]
if not reports:
    raise SystemExit("no reports to aggregate")
aggregate = reports[0]
metrics = aggregate["metrics"]
for key in ("turns", "estimated_tokens", "tool_output_tokens", "rereads", "context_growth_events", "failed_commands"):
    metrics[key] = sum(report["metrics"].get(key, 0) for report in reports)
metrics["retry_depth_max"] = max(report["metrics"].get("retry_depth_max", 0) for report in reports)
metrics["session_count"] = len(reports)
total_tokens = max(metrics["estimated_tokens"], 1)
aggregate["estimated_waste_pct"] = {
    "low": round(sum(report["metrics"].get("estimated_tokens", 0) * report.get("estimated_waste_pct", {}).get("low", 0) for report in reports) / total_tokens),
    "high": round(sum(report["metrics"].get("estimated_tokens", 0) * report.get("estimated_waste_pct", {}).get("high", 0) for report in reports) / total_tokens),
}
aggregate["score"] = round(sum(report.get("score", 0) for report in reports) / len(reports))
aggregate["findings"] = [finding for report in reports for finding in report.get("findings", [])]
aggregate["timeline"] = []
aggregate["immediate_fixes"] = sorted({fix for report in reports for fix in report.get("immediate_fixes", [])})
aggregate["aggregate_event"]["event"] = "claude_analyzer_multi_session_report"
aggregate["aggregate_event"]["findings"] = {
    finding.get("id", "unknown"): finding.get("cost_impact", "unknown")
    for finding in aggregate["findings"]
}
out_path.write_text(json.dumps(aggregate, indent=2) + "\n")
PY
    return
  fi
  log_path="$(cat "$OUT_DIR/$label.log-path")"
  go run "$ANALYZER_REPO/cmd/agent-analyzer" analyze \
    --log "$log_path" \
    --out "$OUT_DIR/$label-report.json" >"$OUT_DIR/$label-analyze.txt"
}

run_quality_gate() {
  local label="$1"
  local worktree="$2"
  if [[ -z "$QUALITY_COMMAND" ]]; then
    echo "skipped" >"$OUT_DIR/$label-quality-status"
    return
  fi
  set +e
  (cd "$worktree" && bash -lc "$QUALITY_COMMAND") >"$OUT_DIR/$label-quality.stdout.txt" 2>"$OUT_DIR/$label-quality.stderr.txt"
  local status=$?
  set -e
  echo "$status" >"$OUT_DIR/$label-quality-status"
}

run_claude_task baseline "$BASELINE_WT"
run_quality_gate baseline "$BASELINE_WT"
analyze_log baseline

if [[ "$AGENT_PLUGIN_ENABLED" == "1" ]]; then
  go run "$ANALYZER_REPO/cmd/agent-analyzer" plugin \
    --report "$OUT_DIR/baseline-report.json" \
    --out "$PLUGIN_ZIP" >"$OUT_DIR/plugin-generate.txt"
else
  : >"$OUT_DIR/plugin-generate.txt"
fi

tooling_plugin_flags=()
if [[ "$AGENT_PLUGIN_ENABLED" == "1" ]]; then
  tooling_plugin_flags+=(--plugin-dir "$PLUGIN_ZIP")
fi
if [[ -n "$OPTIMIZED_EXTRA_PLUGIN_DIRS" ]]; then
  IFS=':' read -r -a extra_plugin_dirs <<<"$OPTIMIZED_EXTRA_PLUGIN_DIRS"
  for extra_plugin_dir in "${extra_plugin_dirs[@]}"; do
    if [[ -n "$extra_plugin_dir" ]]; then
      tooling_plugin_flags+=(--plugin-dir "$extra_plugin_dir")
    fi
  done
fi
tooling_mcp_flags=()
if [[ -n "$OPTIMIZED_MCP_CONFIG_FILE" ]]; then
  tooling_mcp_flags+=(--mcp-config "$OPTIMIZED_MCP_CONFIG_FILE")
fi
if [[ "$TOOLING_REVIEW_ENABLED" == "1" ]]; then
  touch "$OUT_DIR/tooling-review.marker"
  set +e
  (cd "$OPTIMIZED_WT" && "$CLAUDE_BIN" ${tooling_plugin_flags[@]+"${tooling_plugin_flags[@]}"} $OPTIMIZED_CLAUDE_ARGS ${tooling_mcp_flags[@]+"${tooling_mcp_flags[@]}"} -p "Review available benchmark optimization guidance for this run. Do not install optional tools and do not edit files. Return only the approved setup notes for the next run.") >"$OUT_DIR/tooling-review.stdout.json" 2>"$OUT_DIR/tooling-review.stderr.txt"
  echo "$?" >"$OUT_DIR/tooling-review.exit-status"
  set -e
else
  : >"$OUT_DIR/tooling-review.stdout.json"
  : >"$OUT_DIR/tooling-review.stderr.txt"
  echo "skipped" >"$OUT_DIR/tooling-review.exit-status"
fi

if [[ -n "$OPTIMIZED_PRE_TASK_PROMPT_FILE" ]]; then
  touch "$OUT_DIR/optimized-pre-task.marker"
  set +e
  (cd "$OPTIMIZED_WT" && "$CLAUDE_BIN" ${tooling_plugin_flags[@]+"${tooling_plugin_flags[@]}"} $OPTIMIZED_CLAUDE_ARGS ${tooling_mcp_flags[@]+"${tooling_mcp_flags[@]}"} -p "$(cat "$OUT_DIR/optimized-pre-task-prompt.txt")") >"$OUT_DIR/optimized-pre-task.stdout.json" 2>"$OUT_DIR/optimized-pre-task.stderr.txt"
  echo "$?" >"$OUT_DIR/optimized-pre-task.exit-status"
  set -e
  python3 - "$OUT_DIR/optimized-pre-task.stdout.json" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
try:
    result = json.loads(path.read_text())
except Exception as exc:
    raise SystemExit(f"optimized pre-task: Claude output was not JSON: {exc}")
if result.get("is_error"):
    raise SystemExit(f"optimized pre-task: Claude returned is_error=true: {result.get('result')}")
PY
  latest_log_after "$OUT_DIR/optimized-pre-task.marker" >"$OUT_DIR/optimized-pre-task.log-path"
fi

optimized_guidance_arg=""
if [[ -n "$OPTIMIZED_GUIDANCE_FILE" ]]; then
  optimized_guidance_arg="$OUT_DIR/optimized-guidance.txt"
fi
agent_plugin_arg=""
if [[ "$AGENT_PLUGIN_ENABLED" == "1" ]]; then
  agent_plugin_arg="$PLUGIN_ZIP"
fi
run_claude_task optimized "$OPTIMIZED_WT" "$agent_plugin_arg" "$optimized_guidance_arg"
run_quality_gate optimized "$OPTIMIZED_WT"
analyze_log optimized

python3 - "$OUT_DIR" "$FIXED_COMMIT" "$("$CLAUDE_BIN" --version | head -n 1)" <<'PY'
import json
import sys
from pathlib import Path

out_dir = Path(sys.argv[1])
fixed_commit = sys.argv[2]
claude_version = sys.argv[3]

baseline = json.loads((out_dir / "baseline-report.json").read_text())
optimized = json.loads((out_dir / "optimized-report.json").read_text())
baseline_stdout = json.loads((out_dir / "baseline.stdout.json").read_text())
optimized_stdout = json.loads((out_dir / "optimized.stdout.json").read_text())

def read_optional(path):
    return path.read_text().strip() if path.exists() else ""

def tool_use_counts(label):
    log_path_file = out_dir / f"{label}.log-path"
    if not log_path_file.exists():
        return {}
    log_path = Path(log_path_file.read_text().strip())
    if not log_path.exists():
        return {}
    counts = {}
    for line in log_path.read_text(errors="ignore").splitlines():
        try:
            obj = json.loads(line)
        except Exception:
            continue
        message = obj.get("message")
        if not isinstance(message, dict):
            continue
        content = message.get("content")
        if not isinstance(content, list):
            continue
        for item in content:
            if not isinstance(item, dict) or item.get("type") != "tool_use":
                continue
            name = str(item.get("name", "unknown"))
            counts[name] = counts.get(name, 0) + 1
    return dict(sorted(counts.items()))

def summarize(report, label):
    metrics = report["metrics"]
    waste = report["estimated_waste_pct"]
    return {
        "label": label,
        "score": report["score"],
        "estimated_tokens": metrics["estimated_tokens"],
        "tool_output_tokens": metrics["tool_output_tokens"],
        "avoidable_waste_pct": waste,
        "rereads": metrics["rereads"],
        "retry_depth_max": metrics["retry_depth_max"],
        "context_growth_events": metrics["context_growth_events"],
        "failed_commands": metrics["failed_commands"],
        "session_count": metrics.get("session_count", 1),
    }

def usage_summary(stdout):
    usage = stdout.get("usage") or {}
    return {
        "total_cost_usd": stdout.get("total_cost_usd"),
        "num_turns": stdout.get("num_turns"),
        "input_tokens": usage.get("input_tokens", 0),
        "cache_creation_input_tokens": usage.get("cache_creation_input_tokens", 0),
        "cache_read_input_tokens": usage.get("cache_read_input_tokens", 0),
        "output_tokens": usage.get("output_tokens", 0),
        "models_used": sorted((stdout.get("modelUsage") or {}).keys()),
    }

comparison = {
    "schema_version": "2026-05-23",
    "fixed_commit": fixed_commit,
    "claude_version": claude_version,
    "baseline_claude_args": (out_dir / "baseline-claude-args.txt").read_text().strip() if (out_dir / "baseline-claude-args.txt").exists() else "",
    "optimized_claude_args": (out_dir / "optimized-claude-args.txt").read_text().strip() if (out_dir / "optimized-claude-args.txt").exists() else "",
    "baseline_claude_actual_args": read_optional(out_dir / "baseline-claude-actual-args.txt"),
    "optimized_claude_actual_args": read_optional(out_dir / "optimized-claude-actual-args.txt"),
    "baseline_exit_status": int((out_dir / "baseline.exit-status").read_text()),
    "optimized_exit_status": int((out_dir / "optimized.exit-status").read_text()),
    "baseline_quality_status": (out_dir / "baseline-quality-status").read_text().strip(),
    "optimized_quality_status": (out_dir / "optimized-quality-status").read_text().strip(),
    "baseline": summarize(baseline, "baseline Claude Code -p"),
    "optimized": summarize(optimized, "plugin-assisted Claude Code -p"),
    "baseline_tool_use_counts": tool_use_counts("baseline"),
    "optimized_tool_use_counts": tool_use_counts("optimized"),
    "baseline_claude_usage": usage_summary(baseline_stdout),
    "optimized_claude_usage": usage_summary(optimized_stdout),
    "delta": {
        "estimated_tokens": optimized["metrics"]["estimated_tokens"] - baseline["metrics"]["estimated_tokens"],
        "tool_output_tokens": optimized["metrics"]["tool_output_tokens"] - baseline["metrics"]["tool_output_tokens"],
        "avoidable_waste_low_points": optimized["estimated_waste_pct"]["low"] - baseline["estimated_waste_pct"]["low"],
        "avoidable_waste_high_points": optimized["estimated_waste_pct"]["high"] - baseline["estimated_waste_pct"]["high"],
        "rereads": optimized["metrics"]["rereads"] - baseline["metrics"]["rereads"],
        "context_growth_events": optimized["metrics"]["context_growth_events"] - baseline["metrics"]["context_growth_events"],
        "failed_commands": optimized["metrics"]["failed_commands"] - baseline["metrics"]["failed_commands"],
        "claude_total_cost_usd": (optimized_stdout.get("total_cost_usd") or 0) - (baseline_stdout.get("total_cost_usd") or 0),
        "claude_cache_creation_input_tokens": (optimized_stdout.get("usage") or {}).get("cache_creation_input_tokens", 0) - (baseline_stdout.get("usage") or {}).get("cache_creation_input_tokens", 0),
        "claude_cache_read_input_tokens": (optimized_stdout.get("usage") or {}).get("cache_read_input_tokens", 0) - (baseline_stdout.get("usage") or {}).get("cache_read_input_tokens", 0),
        "claude_output_tokens": (optimized_stdout.get("usage") or {}).get("output_tokens", 0) - (baseline_stdout.get("usage") or {}).get("output_tokens", 0),
    },
    "privacy_boundary": "Publish sanitized reports and comparison JSON only. Do not publish raw Claude Code logs, raw prompts, secrets, private paths, or unknown private tool names.",
}
(out_dir / "comparison.json").write_text(json.dumps(comparison, indent=2) + "\n")
PY

echo "Benchmark artifacts written to $OUT_DIR"
echo "Review comparison: $OUT_DIR/comparison.json"
