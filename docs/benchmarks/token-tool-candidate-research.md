# Token Tool Candidate Research

Research date: 2026-05-24

This note records the additional tools and approaches added after the first benchmark pass. Each candidate is evaluated by token category and by a three-repeat task benchmark.

## Candidate Mechanisms

| Candidate | Mechanism | Intended token category | Product interpretation |
| --- | --- | --- | --- |
| Probe | Local AST-aware/BM25 code search with bounded output | Input/context through compact retrieval | Negative here: tool output dipped, but total/cost rose. |
| Semble | Local code-aware chunks with semantic and BM25 retrieval | Input/context through compact retrieval | Positive here: repeated task runs saved estimated, tool-output, output, and cost. |
| Squeez | Explicit shell-output compression via `squeez wrap` | Tool-output/input-context | Removed from recommendations: repeated task runs saved tool-output/cost, but it conflicts with Spec Kitty workflows. |
| RTK | Explicit shell-output compression via `rtk` | Tool-output/input-context | Conditional: useful for noisy shell output; keep global hooks waiver-gated. |
| CodeGraph | Local CodeGraph MCP server with pre-indexed symbol/call graph queries | Input/context through code-aware retrieval | Research-only diagnostic. Source/package reviewed and 3x benchmarked, but this fixture showed higher token/cost use. |

## Smoke Results

Smoke tests remain useful only as mechanism checks:

- Probe returned relevant snippets under bounded `--max-tokens` searches.
- Semble returned relevant parser, aggregate, render, sort, and test files from path-limited searches.
- Squeez compressed noisy failing Go test output from 7,088 bytes to 1,603 bytes while preserving failure details.
- RTK compressed the same noisy failing Go test output from 7,088 bytes to 963 bytes.
- CodeGraph package smoke review verified `@colbymchenry/codegraph@0.9.9`, MIT license, pinned npx launch, local `.codegraph/` index setup, MCP command shape `codegraph serve --mcp`, and uninstall/uninit boundaries.

Smoke success did not automatically predict task-level savings. The repeated task benchmark below is the product evidence.

## Final 3x Task Results

All rows passed `go test ./...` in all three repeats.

| Candidate | Quality | Estimated tokens | Tool-output tokens | Output signal | Cost signal | Verdict |
| --- | --- | ---: | ---: | --- | --- | --- |
| Probe | 3/3 | `+874` | `-745` | Claude output `+548` | native `+$0.038069`; API estimate `+$0.038340` | Negative here |
| Semble | 3/3 | `-16,301` | `-16,060` | Claude output `-480` | native `-$0.089147`; API estimate `-$0.114194` | Positive here |
| Squeez | 3/3 | `-8,471` | `-8,917` | Claude output `+73` | native `-$0.014049`; API estimate `-$0.028224` | Removed: conflicts with Spec Kitty |
| RTK | 3/3 | `-12,446` | `-12,716` | Claude output `+114` | native `-$0.031479`; API estimate `-$0.044316` | Conditional |
| CodeGraph | 3/3 | `+6,094` | `+4,046` | Claude output `+450` | native `+$0.081250`; API estimate `+$0.095826` | Research-only; quality passed, but cost/tokens got worse. |

## Product Actions

- Add Semble as a positive but fixture-scoped candidate.
- Keep RTK as the explicit shell-output compression recommendation, not a silent global hook default.
- Do not recommend Squeez because it conflicts with Spec Kitty workflows.
- Do not add Probe as a default recommendation for this task family.
- Keep smoke-test claims separate from task benchmark claims.
- Keep CodeGraph research-only. The `codegraph-claude` suite passed quality 3/3 but increased cost and token use, so there is no promotion change.

## Artifacts

- `web/proof/reports/aggregate-probe.json`
- `web/proof/reports/aggregate-semble.json`
- `web/proof/reports/aggregate-squeez.json`
- `web/proof/reports/aggregate-rtk-explicit.json`
- CodeGraph diagnostic artifact: `web/proof/reports/aggregate-codegraph-claude.json`
