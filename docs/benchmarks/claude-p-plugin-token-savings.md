# Claude Code -p Plugin Token Savings Benchmark

This runbook implements the proof protocol for the Agent Analyzer optimization plugin. It is designed to prove or falsify token-waste reduction without publishing raw Claude Code logs.

Codex cross-harness runbook and results: [codex-vs-claude-benchmark.md](codex-vs-claude-benchmark.md).

## Goal

Run the same bounded coding task twice from the same git commit:

1. baseline Claude Code `-p` with no generated Agent Analyzer plugin,
2. fresh Claude Code `-p` with the generated plugin from the baseline report.

Both logs are measured by Claude Analyzer. A result is publishable only when task quality is equal or better and the comparison includes the exact commands, versions, commit SHA, sanitized reports, and privacy boundary.

## Token Categories

The benchmark separates token categories before making a savings claim:

- Input/context tokens: prompt text, project instructions, tool schemas, file reads, tool results, and prior conversation context.
- Tool-output tokens: the subset of input/context tokens produced by shell, MCP, file, and search outputs.
- Output tokens: visible assistant text and tool-call JSON emitted by the model.
- Reasoning tokens: hidden model reasoning budget reported by some harnesses. Claude Code JSONL does not expose this as a separate field in every run, so Claude Code plugin claims must not imply direct reasoning-token savings unless a harness reports it.
- Cached input tokens: reused context that can make dollar cost diverge from live context size.
- Telemetry-only signals: accounting/status tools such as ccusage or statuslines; these measure usage but do not directly reduce any category.

Publish a savings claim only for the categories that improved. For example, Caveman-style terseness can reduce output tokens while still increasing tool-output or uncached input tokens.

## Published API Cost Translation

When converting token movement to direct API inference cost, keep this separate from Claude Code's native `total_cost_usd` field:

- Claude Code `--model sonnet` resolved to `claude-sonnet-4-6` in the benchmark usage.
- Use published Claude Sonnet 4.6 rates: `$3/MTok` input, `$6/MTok` 1-hour cache write, `$0.30/MTok` cache read, and `$15/MTok` output.
- The comparison JSON does not expose the 5-minute vs 1-hour cache-write split. The raw benchmark stdout for the published runs used `ephemeral_1h_input_tokens`, so the proof pages price `cache_creation_input_tokens` at the 1-hour rate.
- Do not replace native harness cost with API-rate estimates; publish both when both are available.

Repeated noisy-repo Agent Analyzer result, averaged across three fresh baseline/optimized pairs:

| Cost surface | Baseline | Optimized | Delta |
| --- | ---: | ---: | ---: |
| Published Claude Sonnet 4.6 API-rate estimate | `$0.246837` | `$0.187629` | `-$0.059207` |
| Claude Code native reported `total_cost_usd` | n/a | n/a | `-$0.044219` mean delta |

This means the final product result can be described as repeated measured context/tool-output reduction plus a repeated published-rate cost reduction for this fixture. The detailed formula and caveats are in [api-cost-translation.md](api-cost-translation.md).

## Required Task Shape

Use a task that:

- navigates multiple files,
- adds or fixes at least one test,
- has a clear done condition,
- can be reset to the same git commit,
- requires no external credentials or private network,
- does not depend on real-time external state.

Store the task prompt verbatim in a text file. Do not edit it between runs.

## Scripted Run

From this repository:

```sh
TASK_PROMPT_FILE=/absolute/path/to/task-prompt.txt \
SOURCE_REPO=/absolute/path/to/benchmark/repo \
BASE_REF=<fixed-commit-or-tag> \
OUT_DIR=.data/benchmarks/claude-p-plugin-token-savings \
./scripts/benchmark-claude-p.sh
```

The script writes:

- `baseline-report.json`
- `optimized-report.json`
- `comparison.json`
- `agent-analyzer-plugin.zip`
- stdout, stderr, exit status, and local log-path receipts for each run

Publish only the sanitized report JSON and `comparison.json`.

## Required Replication

The single-pair script is the primitive, not the final evidence standard. For product claims, run the wrapper:

```sh
TASK_PROMPT_FILE=/absolute/path/to/task-prompt.txt \
SOURCE_REPO=/absolute/path/to/benchmark/repo \
BASE_REF=<fixed-commit-or-tag> \
QUALITY_COMMAND="go test ./..." \
HARNESS=claude \
RUN_NAME=<tool-or-guidance-name> \
REPEATS=3 \
./scripts/benchmark-repeat.sh
```

This creates three fresh baseline/optimized pairs and an `aggregate.json` with mean, median, min, max, and standard deviation for every numeric delta. Treat one-pair results as first-pass only. See [repeated-benchmark-suite.md](repeated-benchmark-suite.md).

## Manual Protocol

1. Create two isolated worktrees from the same commit.
2. Run `claude -p "$(cat task-prompt.txt)"` in the baseline worktree.
3. Analyze the baseline JSONL log:

   ```sh
   agent-analyzer analyze --log "$BASELINE_LOG" --out baseline-report.json
   ```

4. Generate the plugin from the baseline report:

   ```sh
   agent-analyzer plugin --report baseline-report.json --out agent-analyzer-plugin.zip
   ```

5. Review plugin guidance in a separate setup session without installing optional tools:

   ```sh
   claude --plugin-dir agent-analyzer-plugin.zip -p "Review /agent-analyzer-status, /agent-analyzer-tooling, and /agent-analyzer-proof for this benchmark. Do not install optional tools and do not edit files."
   ```

6. Reset the optimized worktree to the same fixed commit.
7. Run the same prompt in a fresh optimized session:

   ```sh
   claude --plugin-dir agent-analyzer-plugin.zip -p "$(cat task-prompt.txt)"
   ```

8. Analyze the optimized JSONL log:

   ```sh
   agent-analyzer analyze --log "$OPTIMIZED_LOG" --out optimized-report.json
   ```

9. Compare:

   - total estimated tokens,
   - input/context token movement when the harness exposes it,
   - output-token movement when the harness exposes it,
   - reasoning-token movement when the harness exposes it,
   - cached-token movement when the harness exposes it,
   - estimated avoidable waste range,
   - repeated file reads,
   - noisy tool output,
   - retry-loop depth,
   - context-growth spikes,
   - failed commands,
   - completion quality.

## Public Copy

Use this copy only after the benchmark has measured the result:

> In a controlled Claude Code `-p` benchmark, Agent Analyzer generated a session-scoped optimization plugin from the baseline log, then measured the same task again from the same starting commit. The plugin-assisted run preserved task quality and reduced Agent Analyzer's measured avoidable context waste. Raw logs stayed local; the published proof includes only sanitized reports, exact methodology, commit SHA, versions, and comparison metrics.

If the optimized run does not improve waste metrics, publish the null result and the likely reason instead.

## Follow-up Issues

- [#163](https://github.com/Priivacy-ai/agent-log-analyzer/issues/163): Extend repeated trials across at least three fixed task prompts, now that the first permanent 3x suite exists.
- [#165](https://github.com/Priivacy-ai/agent-log-analyzer/issues/165): Add a multi-harness benchmark covering Claude Code, Codex, and OpenCode where comparable logs are available.
- [#164](https://github.com/Priivacy-ai/agent-log-analyzer/issues/164): Add a hosted proof dashboard that reads sanitized `comparison.json` files and refuses to render savings claims when quality gates fail.
