# Token-Saving Recommendation Engine

## Scope

This doc covers the additive recommendation-engine surface introduced in
Phase A of [issue #68](https://github.com/Priivacy-ai/agent-log-analyzer/issues/68).
The engine is a pure deterministic function that turns an analyzer's dominant
waste signals and a per-tool state map into at most one primary and one
secondary `TokenSavingRecommendation`. It does not read files, networks, or
the environment, and it never echoes raw log data into its output.

Phase A defines the registry, the state model, the decision policy, and the
test surface. Phase B wires real inputs into the engine — the
[#38 fingerprint registry](https://github.com/Priivacy-ai/agent-log-analyzer/issues/38)
populates `ToolStateMap`, the
[#39 MCP/skill utilization analytics](https://github.com/Priivacy-ai/agent-log-analyzer/issues/39)
add `mcp_skill_bloat` signals, and the
[#67 safe CLI probes](https://github.com/Priivacy-ai/agent-log-analyzer/issues/67)
populate `cli_presence` / `cli_version` evidence. None of that work requires
touching engine code.

See:

- [spec.md](../../kitty-specs/token-saving-recommendation-engine-phase-a-01KRZKCJ/spec.md)
- [plan.md](../../kitty-specs/token-saving-recommendation-engine-phase-a-01KRZKCJ/plan.md)
- [research.md](../../kitty-specs/token-saving-recommendation-engine-phase-a-01KRZKCJ/research.md)
- [data-model.md](../../kitty-specs/token-saving-recommendation-engine-phase-a-01KRZKCJ/data-model.md)
- [Public Go API contract](../../kitty-specs/token-saving-recommendation-engine-phase-a-01KRZKCJ/contracts/token_saving_engine_go_api.md)
- Sibling allowlist tables: [token-saving-tooling-matrix.md](token-saving-tooling-matrix.md)
- Paid artifact contract: [plugin-artifacts.md](plugin-artifacts.md)

## Contents

1. Token-saving tool classes
2. Allowlist policy
3. State model
4. Recommendation contract
5. Risk levels and install policies
6. Waiver gate
7. Privacy constraints
8. Phase B integration plan

## Token-saving tool classes

Every registry entry carries a `RecommendationClass`. The class drives rule
firing and the different-class secondary rule. There are eight classes; the
canonical list is in `data-model.md` §"RecommendationClass" and the
constants live in `internal/analyzer/token_saving_recommendations.go`.

**`usage_visibility`** — Independent visibility into token, request, and
quota usage. Responds to `no_usage_visibility`: the user has no out-of-band
view of what Claude Code is spending. Anchored by `ccusage` (rank 1).
`ccstatusline` and usage monitors are reference-only. Benchmark framing:
telemetry can verify savings, but it is not itself a direct token reducer.

**`mcp_skill_hygiene`** — Pruning, lazy-loading, and scoping for MCP servers
and skills. Responds to `mcp_skill_bloat`. This class is structural: the
engine recommends *removing* noise rather than introducing another MCP. By
design it has no candidate tool to install; firing this rule emits an audit
recommendation whose `primary_tool_id` references the hygiene play, not a
new dependency.

**`mcp_output_reducer`** — Compressing or sandboxing MCP tool output.
Responds to `mcp_tool_output_bloat`. Anchored by `context_mode` (rank 1)
with `distill` and `token_optimizer_mcp` as research-only candidates.

**`shell_output_reducer`** — Compressing or proxying shell command output.
Responds to `shell_output_bloat`. Anchored by `RTK (Rust Token Killer,
rtk-ai/rtk)` (rank 1, waiver-gated), with `leanctx` and `headroom` as
research-only entries. Squeez is intentionally not in the recommendable
registry because it conflicts with Spec Kitty workflows.

**`retrieval`** — Code-aware retrieval that replaces broad file reads.
Responds to `repeated_file_reads` and `broad_repo_exploration`. The
benchmark-narrowed defaults promote `Semble` and `grepai` for scoped local
retrieval. `CodeGraph` has a verified source URL and a pinned benchmark
candidate suite, but remains research-only after its 3x diagnostic run added
estimated tokens, tool-output tokens, Claude output, and API-rate cost in the
current fixture. `claude_context` remains research-only after adding overhead in
the current fixture. Stacking is forbidden unless waste persists in a
different retrieval mode.

**`reread_guard`** — Session-scoped read deduplication and per-file memory.
Responds to `unchanged_file_rereads`. Includes `read_once`, `openwolf`, and
`memsearch`.

**`context_hygiene`** — Catch-all class for session hygiene around retry
loops and growth spikes. Responds to `retry_loop` and `context_growth_spikes`.
Phase A keeps this class small; entries are added as Phase B research lands.

**`output_verbosity`** — Diff-level changes to CLAUDE.md and output style.
Responds to `output_verbosity`. The benchmark-narrowed policy treats this
as an advisory workflow recommendation. `claude_token_efficient` showed only
small/noisy savings and `caveman` remains opt-in only.

## Allowlist policy

The registry is a package-level Go literal in
`internal/analyzer/token_saving_tools.go`. Every field is enum-typed where
the value comes from a closed vocabulary, so `go vet` and the compiler catch
malformed entries before any test runs. The registry is read-only at
runtime: there is no mutation API and no on-disk format.

Tools outside the registry are **counted, never recommended**. If the
caller's `ToolStateMap` references a `ToolID` not present in `AllTools()`,
the engine increments `RecommendationSet.UnknownIDCount` and drops the
entry from decision-making. Unknown IDs never appear in `Primary`,
`Secondary`, or `Skipped` output fields.

Installable recommendations also carry `primary_tool_name` and
`primary_tool_url`, copied only from the audited registry entry. These
fields are never derived from logs or user configuration. Advisory
recommendations leave them empty.

Adding, removing, or modifying any registry entry must bump
`RegistryVersion()` — a CI test compares the live value to a checked-in
golden constant and fails fast otherwise. The current benchmark-narrowed
registry is `"phase-a-2026-06-04-codegraph-negative-benchmark"`; see NFR-005.

For URL verification, see `research.md` §"Per-tool research notes". The
short version: Phase A does **not** invent or guess source URLs. Every
entry whose public URL cannot be verified ships with
`install_policy = research_only`, `SourceURL = ""`, and a `Notes` field
explaining the gap. Phase B is expected to verify and promote the
research-only entries as their sources stabilize.

## State model

`ToolState` captures what the analyzer believes about a tool on the
current user's machine. The six values are declared in `data-model.md`
§"ToolState":

| Value | Meaning |
| --- | --- |
| `unknown` | No evidence either way. |
| `mentioned_low` | A log mention of the tool, not strong enough to act on. |
| `installed_medium` | Binary or plugin present; no evidence of use. |
| `configured_medium` | Settings present (MCP registered, skill scoped, etc.); not necessarily active. |
| `active_high` | Observed running against the relevant signal. |
| `rejected_medium` | The user has explicitly opted out (failure / rejection evidence). |

When multiple evidence sources imply different states for the same tool,
the engine resolves to the highest-trust value using this precedence
(highest first):

```
rejected_medium > active_high > configured_medium > installed_medium > mentioned_low > unknown
```

`rejected_medium` tops the order because re-recommending a tool the user
already turned down is the exact behavior the engine exists to avoid.
`active_high` outranks `configured_medium` because an actual running
observation is stronger than a settings-file fingerprint. Mere mentions
are the weakest signal. The precedence is enforced by
`(*ToolStateMap).Resolve` and pinned by a table-driven test.

Transitions are *informational* — the engine never mutates state. Phase B
data sources upgrade tools toward higher trust by attaching new
`EvidenceSource` entries to a `ToolStateEntry`; the engine simply re-runs
the conflict-resolution pass on each call.

## Recommendation contract

The engine evaluates a **fixed 8-step rule list** (research.md §3) in this
exact order:

1. `mcp_skill_bloat` → class `mcp_skill_hygiene` (prune-first; never adds an MCP)
2. `mcp_tool_output_bloat` → class `mcp_output_reducer`
3. `shell_output_bloat` → class `shell_output_reducer`
4. `repeated_file_reads` / `broad_repo_exploration` → class `retrieval`
5. `unchanged_file_rereads` → class `reread_guard`
6. `retry_loop` / `context_growth_spikes` → class `context_hygiene`
7. `output_verbosity` → class `output_verbosity`
8. `no_usage_visibility` → class `usage_visibility`

Input/context-token reductions sit ahead of output-style tweaks and
telemetry because that ordering matches the benchmark results: reducers
change token flow, while accounting tools verify it. `mcp_skill_bloat` is
placed above the other reducers so the engine removes noise before
recommending another tool.

### The ≤ 1 + ≤ 1 invariant

A single `Recommend` call returns at most one `Primary` and at most one
`Secondary` recommendation (constraint C-006). Either or both may be
`nil`. The engine never emits a third recommendation, and Phase B callers
are not allowed to retry with a different input to "harvest" more —
multiple recommendations per pass would re-introduce the stacking problem
the engine exists to prevent.

### Different-class secondary

When `Primary` is set, the engine continues scanning the same rule list
for the next firing rule whose candidate tool belongs to a **different
`RecommendationClass`** from the Primary. The first such match becomes
`Secondary`. Two `retrieval`-class tools, for example, are never emitted
together; the secondary slot must improve a *different* dimension of
waste.

### Skip notes

When a rule would otherwise pick a tool whose state is `active_high` (or
`rejected_medium`), the engine emits a `SkipNote` instead and advances to
the next eligible tool in the same class — or to the next rule if the
class has no further candidates. Each `SkipNote` carries:

- `tool_id` — the tool that was *not* recommended
- `reason` — typically `active_persistent` or `rejected_alternative`
- `for_signal` — the signal that would have promoted the tool

`Skipped` entries are how callers prove a recommendation was thoughtful
rather than blind. They are also how the paid-pack generator explains "we
saw RTK was already active, so we recommend `leanctx` next" to the end
user.

### Worked example

Inputs:

- `signals = ["shell_output_bloat", "repeated_file_reads"]`
- `state["rtk"].State = active_high`
- `state["serena"]` absent

Walk:

1. Rule 4 (`shell_output_bloat`) fires. The class is `shell_output_reducer`;
   the rank-1 candidate is `rtk`. `state["rtk"] == active_high`, so the
   engine appends a `SkipNote{rtk, active_persistent, shell_output_bloat}`
   and advances to the next `shell_output_reducer` entry (`leanctx`).
   `leanctx` is `research_only` in Phase A, so it is not emitted as a
   primary recommendation.
2. Rule 5 (`repeated_file_reads`) fires. The class is `retrieval`; the
   rank-1 candidate is `serena` (or whichever retrieval entry has been
   verified in the current registry). State is `unknown`, so the engine
   emits a `Primary` recommendation with `reason = absent` and class
   `retrieval`.
3. The engine continues for `Secondary`. Rule 4 already produced only a
   `SkipNote` (no class-claim), so the secondary scan looks for a
   different-class firing rule. None of the remaining signals fire, so
   `Secondary` stays `nil`.

Result: one `SkipNote` (RTK), one `Primary` (retrieval-class), no
`Secondary`. The output is byte-identical across runs because every map
in the output is iterated through a sorted-key helper.

## Risk levels and install policies

`RiskLevel` summarizes the operational surface of installing or running a
tool. The three values are declared in `data-model.md` §"RiskLevel".

| Risk | Operational guidance |
| --- | --- |
| `low` | Standard plugin / package install. No special user prompt beyond the usual install acknowledgement. |
| `medium` | Requires explicit user approval. The caller surface (paid-pack generator, plugin runtime) shows the rollback path before installing. |
| `high` | Rewrites shell, proxy, or MCP behaviour. The engine pairs this risk with `install_policy = recommend_with_waiver` and a non-empty `RollbackGuidance`. Never auto-installed. |

`InstallPolicy` is the engine-visible gate that decides what the caller
does with a recommendation. The five values are declared in
`data-model.md` §"InstallPolicy".

| Policy | Engine behavior | Caller behavior |
| --- | --- | --- |
| `bundle` | Eligible as primary or secondary; treated as a default recommendation. | Ships in the paid-pack default set. |
| `recommend` | Eligible as primary or secondary. | Shown as an approval-gated recommendation; user clicks install. |
| `recommend_with_waiver` | Eligible as primary or secondary. The engine sets the recommendation's `InstallPolicy` field to this value verbatim. | Must show the waiver UI before exposing install commands. |
| `research_only` | **Never emitted as a default primary or secondary.** The engine treats these entries as visible-but-frozen — they appear in `AllTools()` and `GetTool()` but are not selected by `Recommend`. | Surfaced only in documentation contexts (matrix doc, research notes). |
| `reference_only` | Never emitted as a recommendation at all; included in the registry purely for cross-referencing (e.g. discovery indexes, architecture reference repos). | Documented as background reading; never installed. |

The split between `research_only` and `reference_only` matters: the first
group may be promoted to `recommend` in Phase B once URLs and behavior are
verified; the second group never installs by design (it points at things
like `awesome_claude_code` and `claude_code_hooks_mastery`).

## Waiver gate

The waiver gate is **required** for any recommendation whose
`install_policy == recommend_with_waiver` or whose primary tool has
`InstallRisk == high` or `DataMovementRisk == high`. In Phase A this
covers `rtk` and any future shell/proxy rewriter or off-device data
mover.

RTK means `https://github.com/rtk-ai/rtk`. Do not map this recommendation
to the unrelated npm package named `rtk`.

The engine's responsibility ends at *surfacing* the policy: it copies the
tool's `InstallPolicy` verbatim into the recommendation's `InstallPolicy`
field, and it guarantees that the recommendation carries a non-empty
`RollbackGuidance` on the underlying registry entry (registry invariant,
asserted by `TestRegistryInvariants`).

The **caller** owns the UI. Two callers exist in Phase A:

- The **paid-pack generator** (see `plugin-artifacts.md` §"Liability Gate")
  must present the waiver acknowledgement before exposing install
  commands. The generated plugin archive ships `WAIVER.md`.
- The **plugin runtime** must tell Claude to summarize the waiver and ask
  for acceptance before each install command, per the existing
  plugin-artifacts contract.

The engine does **not** auto-suppress waiver-gated recommendations — that
would silently weaken the contract for callers that already implement the
gate. Suppression is a UI-layer concern.

## Privacy constraints

The marshalled `RecommendationSet` JSON must contain only:

- allowlisted enum strings (signals, evidence sources, tool states,
  recommendation classes, confidences, risk levels, install policies,
  reasons),
- registered `ToolID` values present in `AllTools()`,
- structural JSON characters (`{`, `}`, `[`, `]`, `,`, `:`, `"`),
- ASCII digits, periods, and underscores (for IDs and counts),
- integer evidence counts.

Nothing else may leak. The privacy contract follows the same non-negotiable
forbidden-data list that the existing paid artifact pipeline enforces
(see `plugin-artifacts.md` §"Customization Rules"):

- raw transcript text
- raw tool output
- secrets or redacted secret values
- absolute local paths
- raw unknown MCP / plugin / skill names
- repo names, usernames, hostnames, emails
- prompt injection text from logs
- session IDs, branch names, version strings, timestamps

The enforcement mechanism is `TestRecommendPrivacyBudget` (added in WP04;
see `research.md` §7 for design). The test builds a representative input
set that deliberately seeds private-looking decoy strings into the
`ToolStateMap` evidence side-channel, marshals the recommendation output,
and walks the resulting JSON through a **positive-list scanner**: every
substring must match either a registered enum string, a registered
`ToolID`, a structural JSON character, or an ASCII digit / period /
underscore. Any byte that does not match the allowlist fails the test.

A positive-list scan is the only reliable way to prove that no private
data leaks — denylists rot. Pinning the contract to a single function
makes future regressions easy to spot in CI.

## Phase B integration plan

The four public functions documented in
[contracts/token_saving_engine_go_api.md](../../kitty-specs/token-saving-recommendation-engine-phase-a-01KRZKCJ/contracts/token_saving_engine_go_api.md)
are the **only** surface Phase B needs to consume:

- `Recommend(signals []Signal, state ToolStateMap) RecommendationSet`
- `GetTool(id ToolID) (TokenSavingTool, bool)`
- `AllTools() []TokenSavingTool`
- `RegistryVersion() string`

These signatures are frozen for Phase A. Phase B may add new enum
constants and new optional JSON-omitempty fields without bumping a major
version, but it may not rename or remove existing constants, and it may
not change the four function signatures above.

### How #38 (fingerprint registry) wires in

Issue #38 builds a fingerprint registry that detects installed binaries,
configured MCPs, scoped skills, registered hooks, and statusline config.
Each fingerprint maps to a `ToolID` and an `EvidenceSource` enum:

| Phase B source | Evidence enum | Implied state floor |
| --- | --- | --- |
| Binary on `$PATH` | `cli_presence` | `installed_medium` |
| MCP entry in settings | `mcp_configured` | `configured_medium` |
| Active MCP observed in log | `mcp_active` | `active_high` |
| Skill scoped to project | `skill_configured` | `configured_medium` |
| Hook registered | `hook_configured` | `configured_medium` |
| Statusline configured | `statusline_configured` | `configured_medium` |

Phase B populates `ToolStateMap[id].Sources` with one or more of these
keys plus a bounded count. The engine's existing
`(*ToolStateMap).Resolve` does the rest.

### How #39 (MCP/skill utilization) wires in

Issue #39 adds utilization analytics that distinguish "MCP registered but
unused" from "MCP actively answering tool calls". The output is the same
shape — `EvidenceMCPActive` versus `EvidenceMCPConfigured` — plus the new
`SignalMCPSkillBloat` when registered surface vastly outpaces utilization.
The engine already places `mcp_skill_bloat` at rule precedence 2 so the
prune-first hygiene play wins over any tool-add recommendation; no engine
edit is required.

### How #67 (safe CLI probes) wires in

Issue #67 adds safe, allowlisted version-command probes that confirm a
binary's identity and (optionally) read its version string. The probe
outputs become `EvidenceCLIPresence` and `EvidenceCLIVersion` entries on
existing tools. This upgrades many tools from `mentioned_low` /
`unknown` toward `installed_medium`, which materially changes
recommendations (e.g. RTK installed-but-inactive flips the rule from
"recommend RTK absent" to "audit RTK configuration"). No engine edit.

### What stays frozen

Phase B does **not** edit `internal/analyzer/token_saving_tools.go` or
`internal/analyzer/token_saving_recommendations.go` beyond:

- adding new enum constants (additive only),
- adding new registry entries when a research-only URL is verified,
- bumping `RegistryVersion()` on any registry edit.

If Phase B needs to change rule precedence, conflict resolution, or
recommendation ID composition, that is a Phase A-equivalent contract
change and must bump `EngineVersion()` with a corresponding test update.

## Known Phase A gaps

The Phase A post-merge mission review surfaced three behavioural gaps
that downstream consumers should be aware of before integrating against
the engine. None are blockers for Phase B wiring; all three close
themselves naturally once Phase B verifies the relevant tool URLs or
adds the missing taxonomy.

### Gap 1 — RTK fallback chain is mechanically present but emits no Primary

Spec FR-010 describes a fallback chain for `shell_output_bloat`: when
RTK is `active_high` (and bloat persists) or `rejected_medium`, the
engine should recommend `leanctx` (and optionally `headroom`). Phase A
ships `leanctx` and `headroom` as `research_only` because the brief
did not include verifiable public source URLs for either tool, and
Phase A's policy is to never recommend research-only entries by
default. As a result, when RTK is active or rejected:

- `pickPrimary` walks past RTK (via the skip-and-continue branches),
- finds no further recommend-eligible candidate in the
  `shell_output_reducer` class,
- returns `Primary = nil` with a single `SkipNote` for RTK.

Acceptance scenarios AS-05 and AS-06 accept this `Primary = nil`
outcome explicitly. Callers should treat this as: "Phase A has no
verified shell-output fallback yet — surface the SkipNote so the user
sees the active-or-rejected RTK observation, and treat the absent
recommendation as 'no additional play available'." Phase B unblocks
this by verifying `leanctx` (and optionally `headroom`) URLs and
promoting them from `research_only` to `recommend`.

### Gap 2 — `unchanged_file_rereads` emits an empty advisory in Phase A

Spec FR-013 promises a `reread_guard` recommendation (`read_once`,
`leanctx`) for `unchanged_file_rereads`. Phase A's registry ships
every `reread_guard` candidate as `research_only` (URLs unverified)
and the single reference-only entry at the rank-99 sentinel. When
`SignalUnchangedFileRereads` fires alone, the engine's
empty-candidate-list branch produces a synthetic advisory
recommendation:

- `PrimaryToolID = ""`,
- `Reason = ReasonAbsent` (from the rule's `PrimaryReason`),
- `ConfidenceMedium`, `RiskLow`, `InstallPolicy = recommend`,
- empty `EvidenceCounts`,
- `RecommendationID = "rec.reread_guard.none.unchanged_file_rereads"`.

Callers should treat advisory recommendations (any `Primary` with
`PrimaryToolID == ""`) as "the engine has policy-level guidance for
this signal but no verified tool to point at yet". The recommended
caller behaviour is to surface the advisory's `Reason` enum as text
(for `reread_guard`: a generic "audit your read-once / session-memory
configuration") rather than to try to render an empty tool name.
Phase B closes this gap by verifying `read_once` / `openwolf` URLs.

### Gap 3 — `ToolStateMap.Resolve` is a caller-facing helper, not part of `Recommend`'s pipeline

The engine assumes the input `ToolStateMap` is already
conflict-resolved: each `ToolStateEntry.State` is the post-precedence
trust level the caller has decided on. `Recommend` does not call
`ToolStateMap.Resolve` internally; it reads `state[id].State`
directly.

`ToolStateMap.Resolve` is exposed as a public helper so callers that
collect multiple evidence sources per tool can combine them
deterministically before populating the map. Its precedence is the
canonical one from FR-018:

```
rejected_medium > active_high > configured_medium >
installed_medium > mentioned_low > unknown
```

The precedence is pinned by a dedicated unit test
(`TestToolStateMapResolve`). Phase B fingerprint and utilization
inputs from issues #38 and #39 must run their per-tool evidence
through this helper (or an equivalent caller-side combiner) before
handing the result to `Recommend`. If Phase B prefers the engine to
do the combining, that is a Phase A-equivalent contract change and
must bump `EngineVersion()` with a corresponding test update.
The frozen surface is what lets the parallel epics ship in any order.
