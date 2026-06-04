#!/usr/bin/env python3
"""Validate public benchmark proof artifacts.

This is intentionally stricter than a JSON syntax check. It protects the
boundary between repeated recommendation evidence and diagnostic/smoke results,
and it verifies that the committed primary-data index can be audited.
"""

from __future__ import annotations

import hashlib
import json
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
BENCHMARK_DOCS = ROOT / "docs" / "benchmarks"
PRIMARY_DATA = BENCHMARK_DOCS / "primary-data"
PRIMARY_SUITES = PRIMARY_DATA / "suites"
PROOF = ROOT / "web" / "proof"
REPORTS = PROOF / "reports"
RESULTS_JSON = PROOF / "results.json"
SUITE_FILES = [
    BENCHMARK_DOCS / "fixtures" / "tool-suite.json",
    BENCHMARK_DOCS / "fixtures" / "tool-suite-spec-kitty-high-context.json",
]

FORBIDDEN_PRIVATE_PATTERNS = [
    re.compile(r"/Users/"),
    re.compile(r"/private/tmp/"),
    re.compile(r"/var/folders/"),
    re.compile(r"Documents/ClaudeAnalyzer"),
]


def load_json(path: Path) -> dict:
    try:
        return json.loads(path.read_text())
    except Exception as exc:  # pragma: no cover - prints actionable CLI error
        raise AssertionError(f"{rel(path)} is not valid JSON: {exc}") from exc


def rel(path: Path) -> str:
    return str(path.relative_to(ROOT))


def require(condition: bool, message: str, errors: list[str]) -> None:
    if not condition:
        errors.append(message)


def check_no_private_paths(errors: list[str]) -> None:
    paths = []
    for base in (BENCHMARK_DOCS, PROOF):
        paths.extend(p for p in base.rglob("*") if p.is_file())

    for path in sorted(paths):
        text = path.read_text(errors="ignore")
        for pattern in FORBIDDEN_PRIVATE_PATTERNS:
            if pattern.search(text):
                errors.append(f"{rel(path)} contains private local path pattern {pattern.pattern!r}")
                break


def check_fixture_files(errors: list[str]) -> None:
    for suite_file in SUITE_FILES:
        suite = load_json(suite_file)
        target = suite.get("target", {})
        for key in ("task_prompt_file", "mcp_config_file"):
            value = target.get(key)
            if value:
                require((ROOT / value).exists(), f"{rel(suite_file)} target {key} missing: {value}", errors)
        prepare_command = target.get("prepare_command", "")
        if prepare_command and "scripts/" in prepare_command:
            script = prepare_command.split("scripts/", 1)[1].split()[0].strip("'\"")
            require((ROOT / "scripts" / script).exists(), f"{rel(suite_file)} target prepare_command script missing: scripts/{script}", errors)
        for item in suite.get("suites", []):
            for key in (
                "guidance_file",
                "pre_task_prompt_file",
                "mcp_config_file",
                "task_prompt_file",
                "optimized_guidance_file",
                "optimized_pre_task_prompt_file",
                "optimized_mcp_config_file",
            ):
                value = item.get(key)
                if value:
                    require((ROOT / value).exists(), f"{item.get('id')} {key} missing: {value}", errors)


def check_primary_index(errors: list[str]) -> None:
    index_path = PRIMARY_DATA / "index.json"
    index = load_json(index_path)
    files = index.get("files", [])
    require(index.get("file_count") == len(files), "primary-data index file_count does not match files length", errors)

    for item in files:
        path_value = item.get("path")
        sha_value = item.get("sha256")
        require(bool(path_value), "primary-data index entry missing path", errors)
        require(bool(sha_value), f"primary-data index entry missing sha256 for {path_value}", errors)
        path = ROOT / path_value
        require(path.exists(), f"primary-data indexed file missing: {path_value}", errors)
        if path.exists():
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            require(digest == sha_value, f"primary-data sha256 mismatch: {path_value}", errors)


def suite_dirs() -> dict[str, Path]:
    if not PRIMARY_SUITES.exists():
        return {}
    return {path.name: path for path in sorted(PRIMARY_SUITES.iterdir()) if path.is_dir()}


def report_aggregates() -> dict[str, Path]:
    return {
        path.name.removeprefix("aggregate-").removesuffix(".json"): path
        for path in sorted(REPORTS.glob("aggregate-*.json"))
    }


def suite_metadata() -> dict[str, dict]:
    metadata = {}
    for suite_file in SUITE_FILES:
        suite = load_json(suite_file)
        for item in suite.get("suites", []):
            suite_id = item.get("id")
            if suite_id:
                metadata[suite_id] = item
    return metadata


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


def check_suite_artifacts(errors: list[str]) -> None:
    suites = suite_dirs()
    reports = report_aggregates()
    metadata = suite_metadata()
    require(set(suites) == set(reports), "primary-data suites and published aggregate reports differ", errors)

    results = load_json(RESULTS_JSON)
    repeated = results.get("repeated_suite_artifacts", {})
    diagnostic = results.get("diagnostic_suite_artifacts", {})

    for suite_id, suite_dir in suites.items():
        aggregate_path = suite_dir / "aggregate.json"
        manifest_path = suite_dir / "manifest.json"
        require(aggregate_path.exists(), f"{suite_id} missing primary aggregate.json", errors)
        require(manifest_path.exists(), f"{suite_id} missing manifest.json", errors)
        if not aggregate_path.exists():
            continue

        aggregate = load_json(aggregate_path)
        manifest = load_json(manifest_path) if manifest_path.exists() else {}
        public = load_json(reports[suite_id])
        suite_entry = metadata.get(suite_id, {})
        promotion_policy = suite_entry.get("promotion_policy", "recommendation_evidence")
        require(public.get("suite_id") == suite_id, f"{suite_id} published aggregate has wrong suite_id", errors)
        require(public.get("quality_passed") == quality_passed(aggregate), f"{suite_id} published quality_passed disagrees with primary aggregate", errors)
        require(public.get("required_repeats") == aggregate.get("required_repeats"), f"{suite_id} required_repeats mismatch", errors)
        require(public.get("completed_repeats") == aggregate.get("completed_repeats"), f"{suite_id} completed_repeats mismatch", errors)
        require(public.get("promotion_policy", "recommendation_evidence") == promotion_policy, f"{suite_id} promotion_policy mismatch", errors)
        reproducibility = public.get("reproducibility") or {}
        manifest_digest = hashlib.sha256(manifest_path.read_bytes()).hexdigest() if manifest_path.exists() else None
        require(reproducibility.get("manifest_sha256") == manifest_digest, f"{suite_id} public manifest_sha256 mismatch", errors)
        require(reproducibility.get("harness") == manifest.get("harness"), f"{suite_id} public harness does not match manifest", errors)
        require(reproducibility.get("required_repeats") == manifest.get("required_repeats"), f"{suite_id} public required_repeats does not match manifest", errors)
        if manifest.get("completed_repeats") is not None:
            require(reproducibility.get("completed_repeats") == manifest.get("completed_repeats"), f"{suite_id} public completed_repeats does not match manifest", errors)
        env = manifest.get("environment") or {}
        for key, public_key in (
            ("BENCHMARK_SUITE_SHA256", "suite_file_sha256"),
            ("BENCHMARK_TASK_PROMPT_SHA256", "task_prompt_sha256"),
            ("BENCHMARK_GUIDANCE_SHA256", "guidance_sha256"),
            ("BENCHMARK_PRE_TASK_PROMPT_SHA256", "pre_task_prompt_sha256"),
            ("BENCHMARK_MCP_CONFIG_SHA256", "mcp_config_sha256"),
        ):
            if env.get(key):
                require(reproducibility.get(public_key) == env.get(key), f"{suite_id} public {public_key} does not match manifest", errors)

        run_dirs = aggregate.get("run_dirs", [])
        require(len(run_dirs) == aggregate.get("completed_repeats"), f"{suite_id} run_dirs length does not match completed_repeats", errors)
        for run_dir in run_dirs:
            path = suite_dir / run_dir
            require(path.exists(), f"{suite_id} listed run dir missing: {run_dir}", errors)
            for filename in (
                "comparison.json",
                "baseline.exit-status",
                "optimized.exit-status",
                "baseline-quality-status",
                "optimized-quality-status",
                "task-prompt.txt",
            ):
                require((path / filename).exists(), f"{suite_id}/{run_dir} missing {filename}", errors)

        is_repeated = suite_id in repeated
        is_diagnostic = suite_id in diagnostic
        require(is_repeated != is_diagnostic, f"{suite_id} must appear in exactly one proof artifact bucket", errors)
        if is_repeated:
            require(public.get("quality_passed") is True, f"{suite_id} repeated artifact is not quality-passed", errors)
            require(public.get("completed_repeats", 0) >= 3, f"{suite_id} repeated artifact has fewer than 3 repeats", errors)
        if is_diagnostic:
            candidate_policy = promotion_policy in {"candidate_until_reviewed", "research_only", "diagnostic_only"}
            require(
                candidate_policy or not (public.get("quality_passed") and public.get("completed_repeats", 0) >= 3),
                f"{suite_id} diagnostic artifact looks promotable without a candidate/research promotion policy",
                errors,
            )


def check_docs_and_pages(errors: list[str]) -> None:
    required = [
        BENCHMARK_DOCS / "repeated-benchmark-suite.md",
        BENCHMARK_DOCS / "api-cost-translation.md",
        BENCHMARK_DOCS / "external-token-benchmark-comparison.md",
        BENCHMARK_DOCS / "spec-kitty-high-context-fixtures.md",
        PROOF / "index.html",
        PROOF / "methodology.html",
        PROOF / "results.html",
        PROOF / "benchmark-comparison.html",
    ]
    for path in required:
        require(path.exists(), f"required benchmark page/doc missing: {rel(path)}", errors)

    landing = (ROOT / "web" / "index.html").read_text()
    report_template = (ROOT / "cmd" / "api" / "report_html.go").read_text()
    for needle in ("/proof/", "/proof/results.html", "/proof/methodology.html"):
        require(needle in landing, f"landing page missing benchmark link {needle}", errors)
    for needle in ("/proof/results.html", "/proof/methodology.html"):
        require(needle in report_template, f"report template missing benchmark link {needle}", errors)


def main() -> int:
    errors: list[str] = []
    check_docs_and_pages(errors)
    check_fixture_files(errors)
    check_primary_index(errors)
    check_suite_artifacts(errors)
    check_no_private_paths(errors)

    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1
    print("Benchmark artifacts validated.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
