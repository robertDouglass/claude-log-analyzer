# Repeated Benchmark Suite

Research date: 2026-05-24

The benchmark suite treats LLM variance as a first-class risk. A single baseline/optimized pair is useful for smoke testing a mechanism, but every product-facing verdict below now uses three fresh baseline/optimized pairs.

## Replication Policy

| Level | Requirement | Product language |
| --- | --- | --- |
| Smoke | Direct install or command smoke test only | "Mechanism works in isolation." |
| First-pass A/B | One fresh baseline/optimized pair with the same prompt, same commit, and passing quality gate | "Promising/negative in one controlled run." |
| Repeated A/B | At least three fresh baseline/optimized pairs with the same prompt, same commit, and passing quality gate | "Repeated benchmark result." |

Strong savings claims require repeated A/B evidence and must name the token category: input/context, tool-output, visible output, reasoning, native harness cost, or published API-rate estimate. Product copy should scale repeated cost percentages, not one-task cents.

## Permanent Runner

Run named suites from the auditable fixture file:

```sh
REPEATS=3 ./scripts/benchmark-suite.sh
```

Validate target preparation and suite wiring without launching an agent:

```sh
DRY_RUN=1 ONLY=codegraph-claude REPEATS=1 ./scripts/benchmark-suite.sh
```

Run selected suites:

```sh
ONLY=agent-analyzer-guided-v3,rtk-explicit,codex-guided REPEATS=3 ./scripts/benchmark-suite.sh
```

Run candidate suites such as CodeGraph or Headroom:

```sh
ONLY=codegraph-claude REPEATS=3 ./scripts/benchmark-suite.sh
ONLY=headroom-claude REPEATS=3 ./scripts/benchmark-suite.sh
ONLY=headroom-proxy-claude REPEATS=3 ./scripts/benchmark-suite.sh
```

Run a single harness directly:

```sh
TASK_PROMPT_FILE=docs/benchmarks/fixtures/tasks/owner-breakdown-v3-noisy.txt \
SOURCE_REPO=/tmp/agent-analyzer-owner-breakdown-target-v1 \
BASE_REF=benchmark/owner-breakdown-v1-base \
QUALITY_COMMAND='go test ./...' \
HARNESS=claude \
RUN_NAME=rtk-explicit \
REPEATS=3 \
OPTIMIZED_GUIDANCE_FILE=docs/benchmarks/fixtures/guidance/rtk-explicit-guidance.txt \
AGENT_PLUGIN_ENABLED=0 \
TOOLING_REVIEW_ENABLED=0 \
./scripts/benchmark-repeat.sh
```

The runner writes:

- `manifest.json` with harness, repeat count, target commit, fixture hashes, tool versions, git dirty status, timestamps, and sanitized environment settings
- `run-01/comparison.json`
- `run-02/comparison.json`
- `run-03/comparison.json`
- `aggregate.json` with mean, median, min, max, and standard deviation for every numeric delta

Sanitized primary recordings from completed suites are committed under `docs/benchmarks/primary-data/`. That directory keeps the per-run comparisons and quality evidence auditable without publishing raw Claude/Codex logs or copied worktrees.

## Reproducible Publish Checklist

For any future suite run:

```sh
DRY_RUN=1 ONLY=<suite-id> REPEATS=3 ./scripts/benchmark-suite.sh
ONLY=<suite-id> REPEATS=3 ./scripts/benchmark-suite.sh
./scripts/promote-benchmark-primary-data.py <suite-id>
ONLY=<suite-id> ./scripts/publish-benchmark-results.py
./scripts/validate-benchmark-artifacts.py
```

The published aggregate now includes a compact `reproducibility` block with the
committed primary-data `manifest.json` SHA-256. The validator checks that this
public hash still matches the committed primary manifest, so a proof page cannot
silently drift away from its auditable run metadata.

For MCP-backed runs, `benchmark-repeat.sh` generates a per-run MCP config when the config contains `CODE_CHUNKS_COLLECTION_NAME_OVERRIDE`. That keeps claude-context Milvus collections from leaking across repeats.

## Public Artifact Audit

Before publishing proof-page changes, run:

```sh
./scripts/validate-benchmark-artifacts.py
```

The validator checks that repeated verdicts have at least three quality-passing fresh pairs, diagnostic/smoke runs are not promoted into recommendation evidence, primary-data SHA-256 entries match the committed files, proof aggregates map to committed primary recordings, fixture references exist, and private local path patterns are absent from the public benchmark artifacts.

Candidate suites can set `promotion_policy: "candidate_until_reviewed"`.
Those suites may publish diagnostic aggregate artifacts, but they do not enter
the repeated recommendation bucket until a later reviewed change removes the
candidate gate. CodeGraph and both Headroom suites use this gate.

## Fixture Contract

The permanent fixture lives under `docs/benchmarks/fixtures/`:

- `tool-suite.json`: named suites, harnesses, required tools, fixed commit, quality command, and optimized guidance files
- `scripts/prepare-owner-breakdown-benchmark-target.sh`: deterministic local target generator for future owner-breakdown runs
- `tasks/owner-breakdown-v3-noisy.txt`: the task prompt
- `guidance/*.txt`: optimized guidance for each recommendation
- `mcp/claude-context-local.json`: local Ollama/Milvus MCP config template

Current reproducible target:

```text
/tmp/agent-analyzer-owner-breakdown-target-v1
benchmark/owner-breakdown-v1-base
```

The generator currently produces commit
`1116b800a71cc532b37422f9b56b4391b3cf81f9` with fixed git metadata. Historical
primary-data artifacts committed before 2026-06-04 still record the older
`b96b8a7f5cc57c4335bc7bc85ec726c836ed0996` target in their per-run
`comparison.json` files; use those recorded fields when auditing old proof
results.

Quality gate:

```sh
go test ./...
```

## Final 3x Results

All rows below passed the quality gate in all three repeats.

| Suite | Harness | Estimated tokens mean delta | Tool-output mean delta | Output/reasoning signal | Cost signal | API-rate percent | Verdict |
| --- | --- | ---: | ---: | --- | --- | ---: | --- |
| Agent Analyzer guided | Claude Code | `-12,370` | `-12,698` | Claude output `-504` | native `-$0.044219`; API estimate `-$0.059207` | `-24.0%` | Positive |
| claude-context limit 3 | Claude Code | `+7,327` | `+4,170` | Claude output `+1,169` | native `+$0.048434`; API estimate `+$0.058038` | `+26.0%` | Removed |
| claude-rlm discovery | Claude Code | `+19,477` | `+6,020` | Claude root output `-1,197`; optimized side used 2 sessions per repeat | root-session cost `-$0.075322`; full sub-agent cost not exposed | n/a | Removed |
| context-mode batch | Claude Code | `-12,359` | `-13,257` | Claude output `+170` | native `-$0.036390`; API estimate `-$0.052175` | `-20.4%` | Conditional |
| grepai path-constrained | Claude Code | `-14,567` | `-15,571` | Claude output `+443` | native `-$0.017598`; API estimate `-$0.037657` | `-14.5%` | Conditional |
| claude-token-efficient | Claude Code | `-391` | `-754` | Claude output `-79` | native `-$0.003828`; API estimate `-$0.004208` | `-1.8%` | Removed from default |
| RTK explicit | Claude Code | `-12,446` | `-12,716` | Claude output `+114` | native `-$0.031479`; API estimate `-$0.044316` | `-18.2%` | Conditional |
| Probe | Claude Code | `+874` | `-745` | Claude output `+548` | native `+$0.038069`; API estimate `+$0.038340` | `+16.6%` | Removed |
| Semble | Claude Code | `-16,301` | `-16,060` | Claude output `-480` | native `-$0.089147`; API estimate `-$0.114194` | `-41.5%` | Positive |
| Squeez | Claude Code | `-8,471` | `-8,917` | Claude output `+73` | native `-$0.014049`; API estimate `-$0.028224` | `-12.1%` | Removed: conflicts with Spec Kitty |
| CodeGraph | Claude Code | `+6,094` | `+4,046` | Claude output `+450` | native `+$0.081250`; API estimate `+$0.095826` | `+54.3%` | Research-only diagnostic |
| Headroom MCP | Claude Code | `-138` | `-266` | Claude output `-29` | native `-$0.003130`; API estimate `-$0.002205` | `-1.3%` | Research-only diagnostic |
| Headroom proxy | Claude Code | `-1,109` | `-759` | Claude output `-233` | native `+$0.064141`; API estimate `+$0.084046` | `+49.7%` | Not recommended |
| Agent Analyzer text guidance | Codex | `-14,520` | `-14,527` | output `-483`; reasoning `-45`; uncached+output `-24,369` | API estimate `-$0.062392` | `-31.8%` | Positive here |
| Caveman | Claude Code | `+4,355` | `+4,868` | Claude output `-370` | native `+$0.009919`; API estimate `+$0.009211` | `+3.9%` | Removed |
| Caveman | Codex | `-9,210` | `-9,109` | output `-172`; reasoning `-2`; uncached+output `-4,739` | API estimate `-$0.033986` | `-18.3%` | Harness-specific |

CodeGraph has a pinned candidate suite (`codegraph-claude`) and passed quality
3/3, but it increased estimated tokens, tool-output tokens, Claude output, and
API-rate cost on this fixture. It remains research-only and must not be emitted
as an Agent Analyzer recommendation from this result.

Headroom has a pinned candidate suite (`headroom-claude`) and passed quality
3/3, but the result does not prove a recommendation-grade improvement: one
repeat regressed, the mean estimated-token delta was only `-138`, and the
published API-rate savings mean was only `1.3%`. It remains research-only/not
recommended and must not be emitted as an Agent Analyzer recommendation from
this result.

Headroom proxy has a separate pinned candidate suite (`headroom-proxy-claude`)
using `ANTHROPIC_BASE_URL` only on the optimized side. It passed quality 3/3
and reduced analyzer-estimated tokens, tool-output tokens, and Claude output
tokens, but increased native Claude Code cost in all three repeats and raised
the published API-rate estimate by `49.7%`. It remains research-only/not
recommended and must not be emitted as an Agent Analyzer recommendation from
this result.

ccusage and ccstatusline are telemetry-only. They are useful for cost/context awareness, but they are not task interventions and are no longer represented as direct token reducers in the paid pack.

The core Agent Analyzer API-rate percentage is the value used for scale messaging: `$0.0592073 / $0.2468368 = 23.986%`. That equals about `$1,199/month` on `$5,000/month` of comparable Claude Sonnet API-equivalent coding usage.

claude-rlm is included as a fit test, not as a true high-context proof. The skill targets very long contexts and recursive decomposition. On this medium-context owner-breakdown fixture it passed quality, but the extra RLM sub-agent increased analyzer-estimated tokens, tool output, and failed commands. The root Claude stdout cost fields exclude sub-agent usage, so the root-session cost reduction is not a full cost claim.

## Public Artifacts

Sanitized aggregate artifacts are published under `web/proof/reports/aggregate-*.json`. Raw Claude/Codex logs, local paths, prompts, and secrets are not published.

The public proof pages should prefer aggregate JSON files over single-run comparison JSON files. Single-run files remain useful for debugging and historical context, but not for final verdicts.
