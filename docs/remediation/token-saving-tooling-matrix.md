# Token-Saving Tooling Matrix

This matrix is the paid remediation allowlist. The plugin may recommend only the tools below, only when the matching finding exists, and only after the user accepts the waiver. Installation is never automatic.

## Category Rules

| Category | Meaning | Product rule |
| --- | --- | --- |
| Input/context tokens | Prompt text, project instructions, tool schemas, file reads, tool results, and prior conversation context sent back to the model. | Retrieval and compression tools must prove they replace broader reads or output; extra schemas, indexing summaries, MCP calls, and turns can make this worse. |
| Tool-output tokens | Shell, MCP, file, and search output that becomes model input/context. | This is the strongest default target because bounded shell output and focused reads repeatedly saved tokens without lowering task quality. |
| Output tokens | Visible assistant text and tool-call JSON. | Terse prose alone is not proof of lower full-session cost. |
| Reasoning tokens | Hidden reasoning budget reported by some harnesses, such as Codex. | Do not claim reasoning-token savings unless the harness exposes and measures them. |
| Cached input tokens | Reused context billed or quota-weighted differently from fresh input. | Keep cache effects separate from live-context quality and published API estimates. |
| Telemetry only | Usage visibility, dashboards, and statuslines. | Useful for measurement; not a reducer unless the user changes behavior. |

## Default Pack

| Tool or practice | Source | Repeated result | Product decision |
| --- | --- | ---: | --- |
| Agent Analyzer workflow | `docs/benchmarks/repeated-benchmark-suite.md` | `-12,370` estimated tokens, `-12,698` tool-output tokens, `-24.0%` published API-rate cost | Always include as the core generated plugin workflow. |
| Output-budgeted commands | Built into generated skills | Part of the positive Agent Analyzer run | Always include for tool-output bloat and final verification. |
| Retrieval hygiene | Built into generated skills | Part of the positive Agent Analyzer run | Always include for repeated file reads. |
| Session hygiene and retry breaker | Built into generated skills | Part of the positive Agent Analyzer run | Always include for context pivots and retry loops. |

## Conditional Third-Party Reducers

These tools reduced published API-rate cost in 3/3 repeated runs on the noisy benchmark. They are still conditional because each targets a specific token category.

| Tool | Source | Primary category | Mean published API-rate savings | Product decision |
| --- | --- | --- | ---: | --- |
| Semble | https://github.com/MinishLab/semble | Path-limited semantic retrieval | `41.5%` | Recommend for repeated file reads when bounded local retrieval replaces broad reads. |
| context-mode | https://github.com/mksglu/context-mode | Tool-output/input-context batching | `20.4%` | Recommend for tool-output bloat or context-growth spikes; note that visible output rose on average. |
| RTK | https://github.com/rtk-ai/rtk | Explicit shell-output compression | `18.2%` | Recommend explicit commands first; global hooks require separate approval. |
| grepai | https://github.com/yoanbernabeu/grepai | Path-constrained compact retrieval | `14.5%` | Recommend only with small limits and path filters. |

## Candidate Reducers, Not Yet Recommendations

| Tool | Source | Primary category | Evidence state | Product decision |
| --- | --- | --- | --- | --- |
| CodeGraph | https://github.com/colbymchenry/codegraph | Pre-indexed code-graph retrieval through MCP | 3/3 quality-passing diagnostic; `+6,094` estimated tokens and `+54.3%` API-rate cost | Keep research-only; do not emit in report packs from this fixture. |
| Headroom MCP | https://github.com/chopratejas/headroom | Explicit MCP output compression | 3/3 quality-passing diagnostic; `-138` estimated tokens and `-1.3%` API-rate cost, with one repeat regressing | Keep research-only/not recommended; do not emit in report packs from this fixture. |
| Headroom proxy | https://github.com/chopratejas/headroom | Anthropic-compatible proxy compression | 3/3 quality-passing diagnostic; `-1,109` estimated tokens but `+49.7%` API-rate cost | Keep research-only/not recommended; do not emit in report packs from this fixture. |

## Measurement, Not Reduction

| Tool | Source | Decision |
| --- | --- | --- |
| ccusage | https://github.com/ryoppippi/ccusage | Use as independent accounting if users want it; never label as a direct token reducer. |
| ccstatusline | https://github.com/sirmalloc/ccstatusline | Use as optional awareness outside the prompt path; never label as a direct reducer. |
| Claude Code Usage Monitor / Tracker | Public usage monitor projects | Optional visibility tools; keep outside the generated paid pack unless the user asks for monitoring. |

## Removed From Default Recommendations

| Tool | Source | Repeated result | Decision |
| --- | --- | ---: | --- |
| claude-context | https://github.com/zilliztech/claude-context | `+7,327` estimated tokens, `+26.0%` API-rate cost | Do not recommend for this workflow. Revisit only with a larger retrieval-amortization benchmark. |
| Probe | https://github.com/probelabs/probe | `+874` estimated tokens, `+16.6%` API-rate cost | Do not recommend as a reducer. |
| Caveman for Claude Code | https://github.com/JuliusBrussee/caveman | `+4,355` estimated tokens, `+3.9%` API-rate cost | Keep out of Claude plugin guidance. It helped Codex in this fixture, but worsened Claude Code. |
| claude-rlm | https://github.com/Tenobrus/claude-rlm | `+19,477` estimated tokens on root+subagent aggregate | Do not recommend for medium-context tasks; root stdout cost is incomplete for sub-agent usage. |
| claude-token-efficient | https://github.com/drona23/claude-token-efficient | `1.8%` API-rate savings | Too small/noisy for the default pack; use only as manual verbosity hygiene if a user asks. |
| Squeez | https://github.com/claudioemmanuel/squeez | `-12.1%` API-rate savings in the old noisy-shell fixture | Do not recommend. It conflicts with Spec Kitty workflows, so the benchmark result stays visible only as historical evidence. |
| Broad ecosystem lists and generic plugin installs | Various | Not benchmarked as reducers | Do not ship as default remediation advice. |

## Cost Translation Rule

Dollar claims must name both the token category and the pricing surface:

- Claude Sonnet 4.6 API estimates use input, 1-hour cache-write, cache-read, and output rates.
- Codex API estimates use input, cached-input, output, and separately reported reasoning-output tokens where available.
- Native Claude Code or Codex billing can differ from direct API inference rates.
- Monthly savings are scaled linearly from the repeated benchmark percentage: `monthly savings = comparable monthly baseline spend * savings percent`.

For the core Agent Analyzer workflow, the repeated published API-rate savings were `23.986%`. At comparable workload scale, that is about `$1,199/month` on `$5,000/month` of Claude Sonnet API-equivalent coding usage, or about `$2,399/month` on `$10,000/month`.

## Guardrails

- Prefer explicit commands before hooks or shell rewriting.
- Curl install scripts are allowed only as reviewed fallback instructions, never silent defaults.
- External retrieval tools must disclose API keys, data movement, indexing cost, and storage location.
- Generated artifacts must not include unknown private tool names from logs.
