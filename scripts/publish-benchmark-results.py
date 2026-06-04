#!/usr/bin/env python3
"""Publish sanitized benchmark-suite aggregate files into web/proof/reports."""

from __future__ import annotations

import json
import sys
import statistics
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SUITES_DIR = ROOT / ".data" / "benchmarks" / "suites"
REPORTS_DIR = ROOT / "web" / "proof" / "reports"
RESULTS_JSON = ROOT / "web" / "proof" / "results.json"
SUITE_FILES = [
    ROOT / "docs" / "benchmarks" / "fixtures" / "tool-suite.json",
    ROOT / "docs" / "benchmarks" / "fixtures" / "tool-suite-spec-kitty-high-context.json",
]


def load_json(path: Path) -> dict:
    return json.loads(path.read_text())


def suite_metadata() -> dict[str, dict]:
    metadata = {}
    for suite_file in SUITE_FILES:
        if not suite_file.exists():
            continue
        suite = load_json(suite_file)
        for entry in suite.get("suites", []):
            suite_id = entry.get("id")
            if suite_id:
                metadata[suite_id] = entry
    return metadata


def metric_mean(aggregate: dict, key: str):
    stats = aggregate.get("delta_stats", {}).get(key)
    if not stats:
        return None
    return stats.get("mean")


def quality_passed(aggregate: dict) -> bool:
    if aggregate.get("completed_repeats", 0) < aggregate.get("required_repeats", 3):
        return False
    for item in aggregate.get("quality", []):
        if str(item.get("baseline_quality_status")) != "0":
            return False
        if str(item.get("optimized_quality_status")) != "0":
            return False
        if item.get("baseline_exit_status") != 0:
            return False
        if item.get("optimized_exit_status") != 0:
            return False
    return True


def claude_api_cost(usage: dict) -> float:
    return (
        usage.get("input_tokens", 0) * 3.00
        + usage.get("cache_creation_input_tokens", 0) * 6.00
        + usage.get("cache_read_input_tokens", 0) * 0.30
        + usage.get("output_tokens", 0) * 15.00
    ) / 1_000_000


def codex_api_cost(usage: dict) -> float:
    uncached_input = usage.get("input_tokens", 0) - usage.get("cached_input_tokens", 0)
    return (
        uncached_input * 1.75
        + usage.get("cached_input_tokens", 0) * 0.175
        + usage.get("output_tokens", 0) * 14.00
        + usage.get("reasoning_output_tokens", 0) * 14.00
    ) / 1_000_000


def published_api_estimate(suite_dir: Path) -> dict | None:
    rows = []
    harness = None
    multi_session_runs = []
    for comparison_path in sorted(suite_dir.glob("run-*/comparison.json")):
        run_dir = comparison_path.parent
        comparison = load_json(comparison_path)
        for label in ("baseline", "optimized"):
            paths = run_dir / f"{label}.log-paths"
            if paths.exists():
                count = len([line for line in paths.read_text().splitlines() if line.strip()])
                if count > 1:
                    multi_session_runs.append({"run": run_dir.name, "label": label, "session_count": count})
        if "baseline_claude_usage" in comparison:
            harness = "claude"
            baseline = claude_api_cost(comparison["baseline_claude_usage"])
            optimized = claude_api_cost(comparison["optimized_claude_usage"])
        elif "baseline_codex_usage" in comparison:
            harness = "codex"
            baseline = codex_api_cost(comparison["baseline_codex_usage"])
            optimized = codex_api_cost(comparison["optimized_codex_usage"])
        else:
            continue
        rows.append({
            "run": comparison_path.parent.name,
            "baseline_usd": baseline,
            "optimized_usd": optimized,
            "delta_usd": optimized - baseline,
        })

    if not rows:
        return None

    deltas = [row["delta_usd"] for row in rows]
    baseline_values = [row["baseline_usd"] for row in rows]
    optimized_values = [row["optimized_usd"] for row in rows]
    baseline_mean = statistics.fmean(baseline_values)
    optimized_mean = statistics.fmean(optimized_values)
    delta_mean = statistics.fmean(deltas)
    savings_rate = (-delta_mean / baseline_mean) if baseline_mean else None
    scaled_examples = None
    if savings_rate is not None:
        scaled_examples = {
            "savings_rate": savings_rate,
            "savings_percent": savings_rate * 100,
            "weekly_100_usd": 100 * savings_rate,
            "weekly_500_usd": 500 * savings_rate,
            "monthly_2000_usd": 2000 * savings_rate,
            "monthly_5000_usd": 5000 * savings_rate,
            "monthly_10000_usd": 10000 * savings_rate,
            "basis": "Scaled linearly from the suite's optimized-vs-baseline published API-rate delta: savings = comparable baseline spend * savings_percent.",
        }
    if harness == "claude":
        rates = {
            "model_assumption": "Claude Sonnet 4.6",
            "input_tokens_per_mtok_usd": 3.00,
            "cache_creation_input_tokens_per_mtok_usd": 6.00,
            "cache_read_input_tokens_per_mtok_usd": 0.30,
            "output_tokens_per_mtok_usd": 15.00,
        }
        note = "Uses exposed Claude Code aggregate usage fields repriced at published Claude Sonnet 4.6 API rates. Claude Code native billing can differ because raw runs also report internal model usage."
    else:
        rates = {
            "model_assumption": "GPT-5.3-Codex Standard",
            "input_tokens_per_mtok_usd": 1.75,
            "cached_input_tokens_per_mtok_usd": 0.175,
            "output_tokens_per_mtok_usd": 14.00,
            "reasoning_output_tokens_per_mtok_usd": 14.00,
        }
        note = "Uses Codex JSON usage fields repriced at published GPT-5.3-Codex Standard API rates. Reasoning tokens are billed at the output-token rate in this estimate."
    return {
        "harness": harness,
        "complete_cost_surface": not multi_session_runs,
        "multi_session_runs_not_in_stdout_usage": multi_session_runs,
        "rates": rates,
        "baseline_mean_usd": baseline_mean,
        "optimized_mean_usd": optimized_mean,
        "delta_mean_usd": delta_mean,
        "savings_rate": savings_rate,
        "savings_percent": savings_rate * 100 if savings_rate is not None else None,
        "scaled_examples": scaled_examples,
        "delta_min_usd": min(deltas),
        "delta_max_usd": max(deltas),
        "runs": rows,
        "note": note if not multi_session_runs else note + " This suite used additional Claude sessions whose usage is not included in the root stdout usage fields, so this API estimate is root-session-only and must not be used as a full cost claim.",
    }


def publish_aggregates() -> dict[str, dict]:
    REPORTS_DIR.mkdir(parents=True, exist_ok=True)
    metadata = suite_metadata()
    published = {}
    diagnostic = {}
    for aggregate_path in sorted(SUITES_DIR.glob("*/aggregate.json")):
        suite_id = aggregate_path.parent.name
        aggregate = load_json(aggregate_path)
        suite_entry = metadata.get(suite_id, {})
        promotion_policy = suite_entry.get("promotion_policy", "recommendation_evidence")
        public_name = f"aggregate-{suite_id}.json"
        public_path = REPORTS_DIR / public_name
        public = {
            "schema_version": "2026-05-24",
            "suite_id": suite_id,
            "source": "scripts/benchmark-repeat.sh aggregate.json",
            "required_repeats": aggregate.get("required_repeats"),
            "completed_repeats": aggregate.get("completed_repeats"),
            "quality_passed": quality_passed(aggregate),
            "failed_repeats": aggregate.get("failed_repeats", []),
            "delta_stats": aggregate.get("delta_stats", {}),
            "run_dirs": aggregate.get("run_dirs", []),
            "promotion_policy": promotion_policy,
            "candidate_source_url": suite_entry.get("candidate_source_url"),
            "candidate_issue": suite_entry.get("candidate_issue"),
            "published_api_cost_estimate": published_api_estimate(aggregate_path.parent),
            "privacy_boundary": "Raw Claude/Codex logs, raw prompts, local paths, and secrets are not included in public aggregate artifacts.",
        }
        public_path.write_text(json.dumps(public, indent=2) + "\n")
        entry = {
            "artifact": f"reports/{public_name}",
            "quality_passed": public["quality_passed"],
            "completed_repeats": public["completed_repeats"],
            "required_repeats": public["required_repeats"],
            "estimated_token_delta_mean": metric_mean(public, "estimated_tokens"),
            "tool_output_token_delta_mean": metric_mean(public, "tool_output_tokens"),
            "native_cost_delta_mean": metric_mean(public, "claude_total_cost_usd"),
            "codex_uncached_plus_output_delta_mean": metric_mean(public, "codex_total_uncached_plus_output_tokens"),
            "codex_reasoning_delta_mean": metric_mean(public, "codex_reasoning_output_tokens"),
            "published_api_cost_delta_mean": (
                public["published_api_cost_estimate"] or {}
            ).get("delta_mean_usd") if (
                public["published_api_cost_estimate"] or {}
            ).get("complete_cost_surface", True) else None,
            "published_api_cost_savings_percent": (
                public["published_api_cost_estimate"] or {}
            ).get("savings_percent") if (
                public["published_api_cost_estimate"] or {}
            ).get("complete_cost_surface", True) else None,
            "published_api_cost_scaled_examples": (
                public["published_api_cost_estimate"] or {}
            ).get("scaled_examples") if (
                public["published_api_cost_estimate"] or {}
            ).get("complete_cost_surface", True) else None,
            "promotion_policy": promotion_policy,
            "candidate_source_url": public["candidate_source_url"],
            "candidate_issue": public["candidate_issue"],
        }
        if public["quality_passed"] and public["completed_repeats"] >= 3 and promotion_policy == "recommendation_evidence":
            published[suite_id] = entry
        else:
            diagnostic[suite_id] = entry
    return {"repeated": published, "diagnostic": diagnostic}


def update_results_json(published: dict[str, dict]) -> None:
    results = load_json(RESULTS_JSON)
    results["repeated_suite_artifacts"] = published["repeated"]
    results["diagnostic_suite_artifacts"] = published["diagnostic"]
    results.setdefault("replication_policy", {})
    results["replication_policy"]["published_aggregate_count"] = len(published["repeated"])
    results["replication_policy"]["diagnostic_aggregate_count"] = len(published["diagnostic"])
    RESULTS_JSON.write_text(json.dumps(results, indent=2) + "\n")


def main() -> int:
    if not SUITES_DIR.exists():
        print(f"No suite directory exists: {SUITES_DIR}", file=sys.stderr)
        return 1
    published = publish_aggregates()
    update_results_json(published)
    print(json.dumps(published, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
