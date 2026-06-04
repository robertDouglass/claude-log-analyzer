#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Run named entries from the permanent benchmark fixture suite.

Optional:
  SUITE_FILE=docs/benchmarks/fixtures/tool-suite.json
  OUT_ROOT=.data/benchmarks/suites
  SOURCE_REPO=<target repo>           default from suite file
  REPEATS=3                           default from suite file
  ONLY=id1,id2                        run only selected suite ids
  DRY_RUN=1                           prepare and validate suite wiring only
  SKIP_TARGET_PREP=1                  skip target prepare_command
  SKIP_REQUIRES=1                     skip local dependency checks

Examples:
  ONLY=rtk-explicit REPEATS=3 ./scripts/benchmark-suite.sh
  ONLY=codex-guided,caveman-codex HARNESS=codex ./scripts/benchmark-suite.sh
USAGE
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

ANALYZER_REPO="${ANALYZER_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
SUITE_FILE="${SUITE_FILE:-$ANALYZER_REPO/docs/benchmarks/fixtures/tool-suite.json}"
OUT_ROOT="${OUT_ROOT:-$ANALYZER_REPO/.data/benchmarks/suites}"
ONLY="${ONLY:-}"
SKIP_REQUIRES="${SKIP_REQUIRES:-0}"
SKIP_TARGET_PREP="${SKIP_TARGET_PREP:-0}"
DRY_RUN="${DRY_RUN:-0}"

python3 - "$ANALYZER_REPO" "$SUITE_FILE" "$OUT_ROOT" "$ONLY" "$SKIP_REQUIRES" "$SKIP_TARGET_PREP" "$DRY_RUN" <<'PY'
import json
import os
import shutil
import subprocess
import sys
import hashlib
from pathlib import Path

analyzer_repo = Path(sys.argv[1]).resolve()
suite_file = Path(sys.argv[2]).resolve()
out_root = Path(sys.argv[3]).resolve()
only = {item for item in sys.argv[4].split(",") if item}
skip_requires = sys.argv[5] == "1"
skip_target_prep = sys.argv[6] == "1"
dry_run = sys.argv[7] == "1"

suite = json.loads(suite_file.read_text())
target = suite["target"]
source_repo = Path(os.environ.get("SOURCE_REPO", target["source_repo_default"])).resolve()
repeats = os.environ.get("REPEATS", str(suite.get("default_repeats", 3)))
base_ref = os.environ.get("BASE_REF", target["fixed_commit"])
quality_command = os.environ.get("QUALITY_COMMAND", target["quality_command"])
prepare_command = target.get("prepare_command", "")
setup_command = target.get("setup_command", "")
task_prompt_file = analyzer_repo / target["task_prompt_file"]
repeat_script = analyzer_repo / "scripts" / "benchmark-repeat.sh"
out_root.mkdir(parents=True, exist_ok=True)

def sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()

if prepare_command and not skip_target_prep:
    prep_env = os.environ.copy()
    prep_env.update({
        "ANALYZER_REPO": str(analyzer_repo),
        "SOURCE_REPO": str(source_repo),
        "BASE_REF": base_ref,
    })
    print(f"[target] preparing {source_repo} via {prepare_command}", flush=True)
    subprocess.run(prepare_command, cwd=str(analyzer_repo), env=prep_env, shell=True, check=True)

if not (source_repo / ".git").exists():
    raise SystemExit(f"benchmark target is not a git repository: {source_repo}")
target_commit = subprocess.check_output(
    ["git", "-C", str(source_repo), "rev-parse", base_ref],
    text=True,
).strip()

def resolve(path_value):
    if not path_value:
        return ""
    path = Path(path_value).expanduser()
    if path.is_absolute():
        return str(path)
    return str((analyzer_repo / path).resolve())

def optional_sha(path_value):
    resolved = resolve(path_value)
    if not resolved:
        return ""
    path = Path(resolved)
    if not path.exists() or not path.is_file():
        return ""
    return sha256_file(path)

def require_available(requirement):
    if ":" in requirement:
        kind, value = requirement.split(":", 1)
        if kind == "ollama":
            result = subprocess.run(["ollama", "list"], text=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            if result.returncode != 0 or value not in result.stdout:
                return False, f"missing Ollama model {value}"
            return True, ""
        if kind == "milvus":
            host, port = value.rsplit(":", 1)
            result = subprocess.run(["nc", "-z", host, port], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            return (result.returncode == 0, f"missing Milvus at {value}")
    return (shutil.which(requirement) is not None, f"missing executable {requirement}")

summary = []
for entry in suite["suites"]:
    suite_id = entry["id"]
    if only and suite_id not in only:
        continue

    missing = []
    if not skip_requires:
        for requirement in entry.get("requires", []):
            ok, message = require_available(requirement)
            if not ok:
                missing.append(message)
    suite_out = out_root / suite_id
    suite_out.mkdir(parents=True, exist_ok=True)
    if missing:
        skipped = {
            "schema_version": "2026-05-24",
            "suite_id": suite_id,
            "status": "skipped",
            "missing_requirements": missing,
        }
        (suite_out / "skipped.json").write_text(json.dumps(skipped, indent=2) + "\n")
        print(f"[{suite_id}] skipped: {', '.join(missing)}", flush=True)
        summary.append(skipped)
        continue

    env = os.environ.copy()
    env.update({
        "ANALYZER_REPO": str(analyzer_repo),
        "TASK_PROMPT_FILE": str(task_prompt_file),
        "SOURCE_REPO": str(source_repo),
        "BASE_REF": base_ref,
        "QUALITY_COMMAND": quality_command,
        "BENCHMARK_SUITE_FILE": str(suite_file),
        "BENCHMARK_SUITE_SHA256": sha256_file(suite_file),
        "BENCHMARK_TARGET_COMMIT": target_commit,
        "BENCHMARK_TARGET_PREPARE_COMMAND": prepare_command,
        "BENCHMARK_TASK_PROMPT_SHA256": sha256_file(task_prompt_file),
        "HARNESS": entry["harness"],
        "RUN_NAME": suite_id,
        "REPEATS": repeats,
        "OUT_DIR": str(suite_out),
    })
    if setup_command:
        env["BENCHMARK_SETUP_COMMAND"] = setup_command
    if entry.get("guidance_file"):
        env["OPTIMIZED_GUIDANCE_FILE"] = resolve(entry["guidance_file"])
        env["BENCHMARK_GUIDANCE_SHA256"] = optional_sha(entry["guidance_file"])
    if entry.get("pre_task_prompt_file"):
        env["OPTIMIZED_PRE_TASK_PROMPT_FILE"] = resolve(entry["pre_task_prompt_file"])
        env["BENCHMARK_PRE_TASK_PROMPT_SHA256"] = optional_sha(entry["pre_task_prompt_file"])
    if entry.get("mcp_config_file"):
        env["OPTIMIZED_MCP_CONFIG_FILE"] = resolve(entry["mcp_config_file"])
        env["BENCHMARK_MCP_CONFIG_SHA256"] = optional_sha(entry["mcp_config_file"])
    if entry.get("extra_plugin_dirs"):
        env["OPTIMIZED_EXTRA_PLUGIN_DIRS"] = ":".join(resolve(path) for path in entry["extra_plugin_dirs"])
    env.update(entry.get("env", {}))

    if dry_run:
        status = {
            "schema_version": "2026-05-24",
            "suite_id": suite_id,
            "status": "dry_run",
            "out_dir": str(suite_out),
            "source_repo": str(source_repo),
            "base_ref": base_ref,
            "target_commit": target_commit,
            "harness": entry["harness"],
            "repeats": int(repeats),
            "suite_file_sha256": sha256_file(suite_file),
            "task_prompt_sha256": sha256_file(task_prompt_file),
            "guidance_sha256": optional_sha(entry.get("guidance_file", "")),
            "pre_task_prompt_sha256": optional_sha(entry.get("pre_task_prompt_file", "")),
            "mcp_config_sha256": optional_sha(entry.get("mcp_config_file", "")),
        }
        (suite_out / "dry-run.json").write_text(json.dumps(status, indent=2) + "\n")
        print(f"[{suite_id}] dry-run ok target={target_commit[:12]}", flush=True)
        summary.append(status)
        continue

    print(f"[{suite_id}] running {entry['harness']} repeats={repeats}", flush=True)
    result = subprocess.run([str(repeat_script)], cwd=str(analyzer_repo), env=env)
    status = {
        "schema_version": "2026-05-24",
        "suite_id": suite_id,
        "status": "completed" if result.returncode == 0 else "failed",
        "exit_status": result.returncode,
        "out_dir": str(suite_out),
    }
    (suite_out / "suite-status.json").write_text(json.dumps(status, indent=2) + "\n")
    summary.append(status)
    if result.returncode != 0:
        print(f"[{suite_id}] failed with status {result.returncode}", flush=True)

(out_root / "suite-summary.json").write_text(json.dumps({
    "schema_version": "2026-05-24",
    "suite_file": str(suite_file),
    "source_repo": str(source_repo),
    "base_ref": base_ref,
    "target_commit": target_commit,
    "repeats": int(repeats),
    "results": summary,
}, indent=2) + "\n")

if any(item.get("status") == "failed" for item in summary):
    sys.exit(1)
PY
