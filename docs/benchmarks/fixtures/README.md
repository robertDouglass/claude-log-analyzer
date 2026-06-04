# Benchmark Fixtures

This directory contains the permanent, auditable benchmark inputs used by the proof pages.

## Files

- `tool-suite.json`: canonical list of benchmark suites, harnesses, guidance files, local requirements, and default repeat count.
- `tasks/owner-breakdown-v3-noisy.txt`: fixed task prompt for the noisy Go benchmark.
- `guidance/*.txt`: optimized-run guidance for each intervention.
- `guidance/claude-token-efficient-profile.md`: the minimal CLAUDE.md profile used for the claude-token-efficient trial.
- `mcp/claude-context-local.json`: local claude-context MCP configuration used when Ollama and Milvus are available.
- `mcp/codegraph-npx.json`: pinned CodeGraph MCP configuration used by the candidate CodeGraph suite.

## Running

Prepare and validate the reproducible owner-breakdown target without launching
an agent:

```sh
DRY_RUN=1 ONLY=codegraph-claude REPEATS=1 ./scripts/benchmark-suite.sh
```

The suite file runs `scripts/prepare-owner-breakdown-benchmark-target.sh` by
default. That script rebuilds `/tmp/agent-analyzer-owner-breakdown-target-v1`
from committed shell-script fixtures, commits it with fixed git metadata, and
tags the base state as `benchmark/owner-breakdown-v1-base`.

Run one suite:

```sh
ONLY=rtk-explicit REPEATS=3 ./scripts/benchmark-suite.sh
```

Run all locally available suites:

```sh
REPEATS=3 ./scripts/benchmark-suite.sh
```

Audit the committed public artifacts:

```sh
./scripts/validate-benchmark-artifacts.py
```

The suite runner writes local raw artifacts under `.data/benchmarks/suites/<suite-id>/`. These raw local directories are not intended for publication. Public artifacts should be generated from `aggregate.json` and sanitized comparison JSON only.

Set `SKIP_TARGET_PREP=1` only when deliberately reusing an existing target
checkout. Normal future benchmarks should let the suite prepare the target so
the source repo, base ref, and failing tests are repeatable.

## Evidence Standard

A tool recommendation is not considered repeated evidence until:

- `REPEATS >= 3`
- all repeats create fresh baseline and optimized sessions
- baseline and optimized quality gates both pass
- `aggregate.json` is saved
- the public proof page references the aggregate rather than only a single run

Telemetry-only tools such as ccusage and ccstatusline do not have a task intervention delta. They should be validated separately and labeled as telemetry.

Suites with `promotion_policy: "candidate_until_reviewed"` are allowed to
produce repeatable benchmark artifacts, but the publisher keeps them in the
diagnostic bucket until a later registry/report-pack change deliberately
promotes them. This protects candidate tools such as CodeGraph from becoming
product recommendations simply because the local run completed.
