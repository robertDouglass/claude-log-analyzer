#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Run a controlled Codex exec benchmark against the same task shape as the Claude Code benchmark.

Required:
  TASK_PROMPT_FILE=/path/to/prompt.txt

Optional:
  SOURCE_REPO=$PWD
  ANALYZER_REPO=<directory containing agent-analyzer source>
  BASE_REF=HEAD
  OUT_DIR=.data/benchmarks/codex-exec-token-savings
  CODEX_BIN=codex
  CODEX_ARGS="-s danger-full-access -a never"
  BASELINE_CODEX_ARGS="$CODEX_ARGS"
  OPTIMIZED_CODEX_ARGS="$CODEX_ARGS"
  CODEX_EXEC_ARGS="--ignore-rules --ignore-user-config"
  BASELINE_CODEX_EXEC_ARGS="$CODEX_EXEC_ARGS"
  OPTIMIZED_CODEX_EXEC_ARGS="$CODEX_EXEC_ARGS"
  QUALITY_COMMAND="go test ./..."
  BENCHMARK_SETUP_COMMAND="cp -R /path/fixtures .benchmark-fixtures"
  OPTIMIZED_GUIDANCE_FILE=/path/to/guidance.txt

The script creates two isolated git worktrees from the same commit, runs the
same task in baseline and guidance-assisted Codex exec sessions, analyzes the
Codex JSONL event stream locally, and writes sanitized reports plus a
comparison JSON.
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
OUT_DIR="${OUT_DIR:-.data/benchmarks/codex-exec-token-savings}"
CODEX_BIN="${CODEX_BIN:-codex}"
CODEX_ARGS="${CODEX_ARGS:--s danger-full-access -a never}"
BASELINE_CODEX_ARGS="${BASELINE_CODEX_ARGS:-$CODEX_ARGS}"
OPTIMIZED_CODEX_ARGS="${OPTIMIZED_CODEX_ARGS:-$CODEX_ARGS}"
CODEX_EXEC_ARGS="${CODEX_EXEC_ARGS:---ignore-rules --ignore-user-config}"
BASELINE_CODEX_EXEC_ARGS="${BASELINE_CODEX_EXEC_ARGS:-$CODEX_EXEC_ARGS}"
OPTIMIZED_CODEX_EXEC_ARGS="${OPTIMIZED_CODEX_EXEC_ARGS:-$CODEX_EXEC_ARGS}"
QUALITY_COMMAND="${QUALITY_COMMAND:-}"
BENCHMARK_SETUP_COMMAND="${BENCHMARK_SETUP_COMMAND:-}"
OPTIMIZED_GUIDANCE_FILE="${OPTIMIZED_GUIDANCE_FILE:-}"

if [[ ! -f "$TASK_PROMPT_FILE" ]]; then
  echo "TASK_PROMPT_FILE does not exist: $TASK_PROMPT_FILE" >&2
  exit 2
fi
if [[ -n "$OPTIMIZED_GUIDANCE_FILE" && ! -f "$OPTIMIZED_GUIDANCE_FILE" ]]; then
  echo "OPTIMIZED_GUIDANCE_FILE does not exist: $OPTIMIZED_GUIDANCE_FILE" >&2
  exit 2
fi
if ! command -v "$CODEX_BIN" >/dev/null 2>&1; then
  echo "Codex binary not found: $CODEX_BIN" >&2
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
printf '%s\n' "$BASELINE_CODEX_ARGS" >"$OUT_DIR/baseline-codex-args.txt"
printf '%s\n' "$OPTIMIZED_CODEX_ARGS" >"$OUT_DIR/optimized-codex-args.txt"
printf '%s\n' "$BASELINE_CODEX_EXEC_ARGS" >"$OUT_DIR/baseline-codex-exec-args.txt"
printf '%s\n' "$OPTIMIZED_CODEX_EXEC_ARGS" >"$OUT_DIR/optimized-codex-exec-args.txt"

FIXED_COMMIT="$(git -C "$SOURCE_REPO" rev-parse "$BASE_REF")"
BASELINE_WT="$OUT_DIR/worktree-baseline"
OPTIMIZED_WT="$OUT_DIR/worktree-optimized"

rm -rf "$BASELINE_WT" "$OPTIMIZED_WT"
git -C "$SOURCE_REPO" worktree prune
git -C "$SOURCE_REPO" worktree add --detach "$BASELINE_WT" "$FIXED_COMMIT"
git -C "$SOURCE_REPO" worktree add --detach "$OPTIMIZED_WT" "$FIXED_COMMIT"
if [[ -n "$BENCHMARK_SETUP_COMMAND" ]]; then
  (cd "$BASELINE_WT" && bash -lc "$BENCHMARK_SETUP_COMMAND") >"$OUT_DIR/baseline-setup.stdout.txt" 2>"$OUT_DIR/baseline-setup.stderr.txt"
  (cd "$OPTIMIZED_WT" && bash -lc "$BENCHMARK_SETUP_COMMAND") >"$OUT_DIR/optimized-common-setup.stdout.txt" 2>"$OUT_DIR/optimized-common-setup.stderr.txt"
fi

filter_codex_jsonl() {
  local input="$1"
  local output="$2"
  python3 - "$input" "$output" <<'PY'
import json
import sys
from pathlib import Path

source = Path(sys.argv[1])
dest = Path(sys.argv[2])
kept = []
for line in source.read_text(errors="replace").splitlines():
    line = line.strip()
    if not line.startswith("{"):
        continue
    try:
        json.loads(line)
    except json.JSONDecodeError:
        continue
    kept.append(line)
if not kept:
    raise SystemExit(f"no Codex JSON events found in {source}")
dest.write_text("\n".join(kept) + "\n")
PY
}

run_codex_task() {
  local label="$1"
  local worktree="$2"
  local guidance_path="${3:-}"
  local stdout_path="$OUT_DIR/$label.stdout.jsonl"
  local stderr_path="$OUT_DIR/$label.stderr.txt"
  local events_path="$OUT_DIR/$label-events.jsonl"
  local status_path="$OUT_DIR/$label.exit-status"
  local codex_args="$BASELINE_CODEX_ARGS"
  local codex_exec_args="$BASELINE_CODEX_EXEC_ARGS"
  local prompt

  prompt="$(cat "$PROMPT_COPY")"
  if [[ -n "$guidance_path" ]]; then
    prompt="$(cat "$guidance_path")

Task prompt, unchanged from baseline:

$prompt"
  fi
  if [[ "$label" == "optimized" ]]; then
    codex_args="$OPTIMIZED_CODEX_ARGS"
    codex_exec_args="$OPTIMIZED_CODEX_EXEC_ARGS"
  fi

  set +e
  (cd "$worktree" && printf '%s' "$prompt" | "$CODEX_BIN" $codex_args exec --json -C "$worktree" $codex_exec_args -) >"$stdout_path" 2>"$stderr_path"
  local status=$?
  set -e
  echo "$status" >"$status_path"
  filter_codex_jsonl "$stdout_path" "$events_path"
  python3 - "$events_path" "$label" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
label = sys.argv[2]
events = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
completed = [event for event in events if event.get("type") == "turn.completed"]
if not completed:
    raise SystemExit(f"{label}: Codex did not emit turn.completed")
PY
}

analyze_log() {
  local label="$1"
  go run "$ANALYZER_REPO/cmd/agent-analyzer" analyze \
    --log "$OUT_DIR/$label-events.jsonl" \
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

run_codex_task baseline "$BASELINE_WT"
run_quality_gate baseline "$BASELINE_WT"
analyze_log baseline

optimized_guidance_arg=""
if [[ -n "$OPTIMIZED_GUIDANCE_FILE" ]]; then
  optimized_guidance_arg="$OUT_DIR/optimized-guidance.txt"
fi
run_codex_task optimized "$OPTIMIZED_WT" "$optimized_guidance_arg"
run_quality_gate optimized "$OPTIMIZED_WT"
analyze_log optimized

python3 - "$OUT_DIR" "$FIXED_COMMIT" "$("$CODEX_BIN" --version | head -n 1)" <<'PY'
import json
import sys
from pathlib import Path

out_dir = Path(sys.argv[1])
fixed_commit = sys.argv[2]
codex_version = sys.argv[3]

baseline = json.loads((out_dir / "baseline-report.json").read_text())
optimized = json.loads((out_dir / "optimized-report.json").read_text())
baseline_events = [json.loads(line) for line in (out_dir / "baseline-events.jsonl").read_text().splitlines() if line.strip()]
optimized_events = [json.loads(line) for line in (out_dir / "optimized-events.jsonl").read_text().splitlines() if line.strip()]

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
    }

def usage_summary(events):
    usage = {}
    for event in events:
        if event.get("type") == "turn.completed":
            usage = event.get("usage") or {}
    return {
        "input_tokens": usage.get("input_tokens", 0),
        "cached_input_tokens": usage.get("cached_input_tokens", 0),
        "output_tokens": usage.get("output_tokens", 0),
        "reasoning_output_tokens": usage.get("reasoning_output_tokens", 0),
        "total_uncached_plus_output_tokens": (
            usage.get("input_tokens", 0)
            - usage.get("cached_input_tokens", 0)
            + usage.get("output_tokens", 0)
        ),
    }

def stderr_summary(label):
    text = (out_dir / f"{label}.stderr.txt").read_text(errors="replace")
    return {
        "line_count": len([line for line in text.splitlines() if line.strip()]),
        "skill_load_errors": text.count("failed to load skill"),
        "mcp_auth_errors": text.count("invalid_grant") + text.count("OAuth token refresh failed"),
        "icon_warnings": text.count("icon path must not contain"),
    }

baseline_usage = usage_summary(baseline_events)
optimized_usage = usage_summary(optimized_events)
comparison = {
    "schema_version": "2026-05-24",
    "benchmark_harness": "codex-exec",
    "fixed_commit": fixed_commit,
    "codex_version": codex_version,
    "baseline_codex_args": (out_dir / "baseline-codex-args.txt").read_text().strip(),
    "optimized_codex_args": (out_dir / "optimized-codex-args.txt").read_text().strip(),
    "baseline_codex_exec_args": (out_dir / "baseline-codex-exec-args.txt").read_text().strip(),
    "optimized_codex_exec_args": (out_dir / "optimized-codex-exec-args.txt").read_text().strip(),
    "baseline_exit_status": int((out_dir / "baseline.exit-status").read_text()),
    "optimized_exit_status": int((out_dir / "optimized.exit-status").read_text()),
    "baseline_quality_status": (out_dir / "baseline-quality-status").read_text().strip(),
    "optimized_quality_status": (out_dir / "optimized-quality-status").read_text().strip(),
    "baseline": summarize(baseline, "baseline Codex exec"),
    "optimized": summarize(optimized, "guidance-assisted Codex exec"),
    "baseline_codex_usage": baseline_usage,
    "optimized_codex_usage": optimized_usage,
    "stderr_summary": {
        "baseline": stderr_summary("baseline"),
        "optimized": stderr_summary("optimized"),
    },
    "delta": {
        "estimated_tokens": optimized["metrics"]["estimated_tokens"] - baseline["metrics"]["estimated_tokens"],
        "tool_output_tokens": optimized["metrics"]["tool_output_tokens"] - baseline["metrics"]["tool_output_tokens"],
        "avoidable_waste_low_points": optimized["estimated_waste_pct"]["low"] - baseline["estimated_waste_pct"]["low"],
        "avoidable_waste_high_points": optimized["estimated_waste_pct"]["high"] - baseline["estimated_waste_pct"]["high"],
        "rereads": optimized["metrics"]["rereads"] - baseline["metrics"]["rereads"],
        "context_growth_events": optimized["metrics"]["context_growth_events"] - baseline["metrics"]["context_growth_events"],
        "failed_commands": optimized["metrics"]["failed_commands"] - baseline["metrics"]["failed_commands"],
        "codex_input_tokens": optimized_usage["input_tokens"] - baseline_usage["input_tokens"],
        "codex_cached_input_tokens": optimized_usage["cached_input_tokens"] - baseline_usage["cached_input_tokens"],
        "codex_output_tokens": optimized_usage["output_tokens"] - baseline_usage["output_tokens"],
        "codex_reasoning_output_tokens": optimized_usage["reasoning_output_tokens"] - baseline_usage["reasoning_output_tokens"],
        "codex_total_uncached_plus_output_tokens": optimized_usage["total_uncached_plus_output_tokens"] - baseline_usage["total_uncached_plus_output_tokens"],
    },
    "privacy_boundary": "Publish sanitized reports, filtered Codex event JSONL, and comparison JSON only. Do not publish raw private prompts, secrets, or unredacted local paths.",
    "comparability_note": "Codex exec exposes token usage but not the same Claude Code total_cost_usd field. Compare quality, event-stream analyzer metrics, and token counts separately from Claude Code dollar cost.",
}
(out_dir / "comparison.json").write_text(json.dumps(comparison, indent=2) + "\n")
PY

echo "Benchmark artifacts written to $OUT_DIR"
echo "Review comparison: $OUT_DIR/comparison.json"
