#!/usr/bin/env python3
"""Copy sanitized suite outputs into docs/benchmarks/primary-data.

This intentionally copies only auditable aggregate/comparison/quality evidence.
Raw agent logs, assistant stdout/stderr, plugin zips, and copied worktrees stay
inside local .data and are never committed.
"""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SUITES_DIR = ROOT / ".data" / "benchmarks" / "suites"
PRIMARY_DATA = ROOT / "docs" / "benchmarks" / "primary-data"
PRIMARY_SUITES = PRIMARY_DATA / "suites"
INDEX_JSON = PRIMARY_DATA / "index.json"

SUITE_FILES = ("aggregate.json", "manifest.json", "suite-status.json")
RUN_FILES = (
    "comparison.json",
    "baseline.exit-status",
    "optimized.exit-status",
    "baseline-quality-status",
    "optimized-quality-status",
    "baseline-quality.stdout.txt",
    "baseline-quality.stderr.txt",
    "optimized-quality.stdout.txt",
    "optimized-quality.stderr.txt",
    "task-prompt.txt",
    "suite-run.exit-status",
)


def scrub_text(text: str) -> str:
    replacements = {
        str(ROOT): "$ANALYZER_REPO",
        str(Path.home()): "$HOME",
    }
    tmpdir = os.environ.get("TMPDIR")
    if tmpdir:
        replacements[tmpdir.rstrip("/")] = "$TMPDIR"
    for old, new in replacements.items():
        if old:
            text = text.replace(old, new)
    text = text.replace("/private/tmp/", "$TMPDIR/")
    text = text.replace("/var/folders/", "$TMPDIR/")
    return text


def copy_scrubbed(src: Path, dst: Path) -> None:
    dst.parent.mkdir(parents=True, exist_ok=True)
    try:
        text = src.read_text()
    except UnicodeDecodeError:
        shutil.copy2(src, dst)
        return
    dst.write_text(scrub_text(text))


def run_dirs(suite_dir: Path, aggregate: dict | None) -> list[Path]:
    names = []
    if aggregate:
        names = [name for name in aggregate.get("run_dirs", []) if name]
    if not names:
        names = [path.name for path in suite_dir.glob("run-*") if path.is_dir()]
    return [suite_dir / name for name in sorted(names)]


def promote_suite(suite_id: str) -> None:
    src = SUITES_DIR / suite_id
    if not src.exists():
        raise SystemExit(f"missing suite output: {src}")
    aggregate_path = src / "aggregate.json"
    if not aggregate_path.exists():
        raise SystemExit(f"missing suite aggregate: {aggregate_path}")
    aggregate = json.loads(aggregate_path.read_text())
    dst = PRIMARY_SUITES / suite_id
    if dst.exists():
        shutil.rmtree(dst)
    for name in SUITE_FILES:
        candidate = src / name
        if candidate.exists():
            copy_scrubbed(candidate, dst / name)
    for run_dir in run_dirs(src, aggregate):
        for name in RUN_FILES:
            candidate = run_dir / name
            if candidate.exists():
                copy_scrubbed(candidate, dst / run_dir.name / name)


def rebuild_index() -> None:
    files = []
    paths = [PRIMARY_DATA / "README.md"]
    paths.extend(sorted(path for path in PRIMARY_SUITES.rglob("*") if path.is_file()))
    for path in paths:
        rel = path.relative_to(ROOT)
        data = path.read_bytes()
        files.append({
            "path": str(rel),
            "bytes": len(data),
            "sha256": hashlib.sha256(data).hexdigest(),
        })
    index = {
        "schema_version": "2026-05-24",
        "description": "Primary sanitized benchmark recordings committed to git. Raw Claude/Codex JSONL logs and worktrees are intentionally excluded by the privacy boundary.",
        "source_directory": ".data/benchmarks/suites",
        "published_artifacts_directory": "web/proof/reports",
        "included_files": "aggregate, manifest, suite status, per-run comparison, task prompt, exit status, and quality-gate output files.",
        "excluded_files": "raw Claude/Codex logs, raw assistant transcript stdout/stderr, plugin zip artifacts, and copied worktrees.",
        "scrubbing": "Absolute local repository, home, and temporary-directory paths are replaced with placeholders.",
        "file_count": len(files),
        "files": files,
    }
    INDEX_JSON.write_text(json.dumps(index, indent=2) + "\n")


def main() -> int:
    suite_ids = sys.argv[1:]
    if not suite_ids:
        env_only = os.environ.get("ONLY", "")
        suite_ids = [item for item in env_only.split(",") if item]
    if not suite_ids:
        raise SystemExit("usage: promote-benchmark-primary-data.py <suite-id> [suite-id ...]")
    for suite_id in suite_ids:
        promote_suite(suite_id)
    rebuild_index()
    print(json.dumps({"promoted": suite_ids, "index": str(INDEX_JSON.relative_to(ROOT))}, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
