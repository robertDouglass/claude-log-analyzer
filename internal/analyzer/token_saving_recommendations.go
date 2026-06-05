// Package analyzer — token-saving recommendation engine (Phase A).
//
// WP03 of mission token-saving-recommendation-engine-phase-a-01KRZKCJ.
//
// This file implements the deterministic decision logic: a fixed 8-step
// rule precedence list (research.md §3), candidate selection over the
// WP01 registry, the per-rule state machine (data-model.md
// §"State transitions"), the secondary-class invariant, and the public
// Recommend entry point whose signature is frozen by
// contracts/token_saving_engine_go_api.md.
//
// Determinism contract
// --------------------
// Every map iteration in this file routes through a sort helper from
// WP02 (sortedSignalIDs, ToolStateMap.SortedTools) or a locally declared
// sorted-keys helper. There is intentionally no `range someMap` without
// a paired sort. Go map iteration is randomised; relying on it would
// break NFR-001 invisibly. See research.md §6.
package analyzer

import (
	"sort"
	"strings"
)

// -----------------------------------------------------------------------------
// Fixed rule precedence (research.md §3)
// -----------------------------------------------------------------------------

// ruleSpec is one entry in the static rule-precedence table.
//
// FiringSignals lists the signals that trigger this rule; ANY of them
// present in the engine input causes the rule to fire.
//
// Class names the RecommendationClass the rule pulls candidates from.
//
// PrimaryReason is the Reason emitted when the selected candidate's
// resolved ToolState is the "absent" branch (Unknown / MentionedLow).
// For two rules (mcp_skill_bloat, retry_loop / context_growth_spikes)
// the absent branch carries a special policy reason — see the
// PrimaryReason values in rulePrecedence below.
type ruleSpec struct {
	FiringSignals []Signal
	Class         RecommendationClass
	PrimaryReason Reason
}

// rulePrecedence is the fixed, ordered list of rules the engine
// evaluates. ORDER MATTERS: this list is the contract documented in
// research.md §3. Any change to ordering, signal mapping, class
// mapping, or PrimaryReason MUST bump EngineVersion() (engineVersionString
// in token_saving_types.go) and update research.md §3.
//
// Notes:
//
//   - Rule 2 (mcp_skill_bloat) maps to ClassMCPSkillHygiene, a class
//     that has NO registry entries in Phase A by design. The brief says
//     "do not add another MCP by default" for this signal. The engine
//     therefore emits an *advisory* recommendation (PrimaryToolID="")
//     with Reason = ReasonPruneFirst.
//   - Rule 7 (retry_loop / context_growth_spikes) maps to
//     ClassContextHygiene, also empty in Phase A. The advisory carries
//     Reason = ReasonAuditConfig.
var rulePrecedence = []ruleSpec{
	{FiringSignals: []Signal{SignalMCPSkillBloat}, Class: ClassMCPSkillHygiene, PrimaryReason: ReasonPruneFirst},
	{FiringSignals: []Signal{SignalMCPToolOutputBloat}, Class: ClassMCPOutputReducer, PrimaryReason: ReasonAbsent},
	{FiringSignals: []Signal{SignalShellOutputBloat, SignalToolOutputBloat}, Class: ClassShellOutputReducer, PrimaryReason: ReasonAbsent},
	{FiringSignals: []Signal{SignalRepeatedFileReads, SignalBroadRepoExploration}, Class: ClassRetrieval, PrimaryReason: ReasonAbsent},
	{FiringSignals: []Signal{SignalUnchangedFileRereads}, Class: ClassRereadGuard, PrimaryReason: ReasonAbsent},
	{FiringSignals: []Signal{SignalRetryLoop, SignalContextGrowthSpikes}, Class: ClassContextHygiene, PrimaryReason: ReasonAuditConfig},
	{FiringSignals: []Signal{SignalOutputVerbosity}, Class: ClassOutputVerbosity, PrimaryReason: ReasonAbsent},
	{FiringSignals: []Signal{SignalNoUsageVisibility}, Class: ClassUsageVisibility, PrimaryReason: ReasonAbsent},
}

// -----------------------------------------------------------------------------
// Rule firing
// -----------------------------------------------------------------------------

// ruleFires reports whether any of r.FiringSignals appears in present.
func ruleFires(r ruleSpec, present map[Signal]bool) bool {
	for _, s := range r.FiringSignals {
		if present[s] {
			return true
		}
	}
	return false
}

// firingRules filters rulePrecedence by signals, preserving precedence
// order. signals MUST already be deduplicated/validated; callers in
// Recommend pass validSignals through sortedSignalIDs first.
func firingRules(signals []Signal) []ruleSpec {
	present := make(map[Signal]bool, len(signals))
	for _, s := range signals {
		present[s] = true
	}
	out := make([]ruleSpec, 0, len(rulePrecedence))
	for _, r := range rulePrecedence {
		if ruleFires(r, present) {
			out = append(out, r)
		}
	}
	return out
}

// representativeSignal returns the first FiringSignal of r that is
// present in the engine's deduped input signal set. Used for SkipNote
// attribution (ForSignal). Falls back to r.FiringSignals[0] if no
// match (defensive — by construction the rule wouldn't fire without
// a match, but the static lookup keeps this total).
func representativeSignal(r ruleSpec, present map[Signal]bool) Signal {
	for _, s := range r.FiringSignals {
		if present[s] {
			return s
		}
	}
	return r.FiringSignals[0]
}

// firingSignalsFor returns the subset of r.FiringSignals that are
// present in the deduped input set, sorted ascending. Used to populate
// TokenSavingRecommendation.SignalIDs.
func firingSignalsFor(r ruleSpec, present map[Signal]bool) []Signal {
	var hit []Signal
	for _, s := range r.FiringSignals {
		if present[s] {
			hit = append(hit, s)
		}
	}
	return sortedSignalIDs(hit)
}

// -----------------------------------------------------------------------------
// Candidate selection
// -----------------------------------------------------------------------------

// eligibleCandidates returns the tools belonging to class that are
// eligible for default recommendation. Entries with InstallPolicy of
// PolicyResearchOnly or PolicyReferenceOnly are dropped — they never
// appear as default recommendations. The returned slice preserves the
// class+rank ordering established by AllTools().
func eligibleCandidates(class RecommendationClass) []TokenSavingTool {
	all := AllTools()
	var out []TokenSavingTool
	for _, t := range all {
		if t.RecommendationClass != class {
			continue
		}
		if t.InstallPolicy == PolicyResearchOnly || t.InstallPolicy == PolicyReferenceOnly {
			continue
		}
		out = append(out, t)
	}
	return out
}

// -----------------------------------------------------------------------------
// Per-rule primary selection
// -----------------------------------------------------------------------------

// pickResult is the internal return of pickPrimary; it carries the
// emitted recommendation (or nil), the skipped notes accumulated while
// walking candidates, and — for downstream class-equality tests — the
// recommendation's RecommendationClass. The class is also derivable
// from the registry, but advisory recommendations carry an empty
// PrimaryToolID, so we track it explicitly here.
type pickResult struct {
	Rec     *TokenSavingRecommendation
	Class   RecommendationClass
	Skipped []SkipNote
}

// pickPrimary walks the rule's eligible candidates and applies the
// state machine (data-model.md §"State transitions"):
//
//   - active_high          → SkipNote{ActivePersistent}; advance.
//   - rejected_medium      → SkipNote{RejectedAlternative}; advance.
//   - configured_medium    → emit Reason=configured_inactive; return.
//   - installed_medium     → emit Reason=installed_inactive; return.
//   - mentioned_low|unknown→ emit Reason=rule.PrimaryReason; return.
//
// Special cases:
//
//   - AS-02 server-quota override: when the firing rule is the
//     usage_visibility rule AND the candidate is "ccusage" AND
//     state["ccusage"].State is ToolStateActiveHigh AND
//     state["ccusage"].Sources[EvidenceFailureOrRejection] > 0, override
//     the SkipNote-and-continue branch and emit Reason=server_quota_check
//     pointing at ccusage with ConfidenceHigh.
//
//   - Advisory recommendations: if a rule's class has NO eligible
//     candidates (Phase A: ClassMCPSkillHygiene, ClassContextHygiene),
//     emit an advisory recommendation with PrimaryToolID="",
//     Reason=rule.PrimaryReason, ConfidenceMedium, RiskLow,
//     InstallPolicy=PolicyRecommend, empty EvidenceCounts.
func pickPrimary(rule ruleSpec, state ToolStateMap, present map[Signal]bool) pickResult {
	forSig := representativeSignal(rule, present)
	signalIDs := firingSignalsFor(rule, present)

	candidates := eligibleCandidates(rule.Class)
	if len(candidates) == 0 {
		// Advisory branch (mcp_skill_hygiene, context_hygiene in Phase A).
		// No registry entry to point at — emit synthetic guidance.
		rec := &TokenSavingRecommendation{
			PrimaryToolID:  "",
			Reason:         rule.PrimaryReason,
			SignalIDs:      signalIDs,
			Confidence:     ConfidenceMedium,
			RiskLevel:      RiskLow,
			FailureModes:   failureModesForClass(rule.Class),
			InstallSurface: installSurfaceForClass(rule.Class),
			InstallPolicy:  PolicyRecommend,
			EvidenceCounts: map[EvidenceSource]int{},
		}
		return pickResult{Rec: rec, Class: rule.Class}
	}

	var skipped []SkipNote
	for _, cand := range candidates {
		entry := state[cand.ID] // zero ToolStateEntry if absent → State=="" → falls into default branch.
		st := entry.State
		if st == "" {
			st = ToolStateUnknown
		}

		// AS-02 server-quota override: ccusage already active_high but
		// failure_or_rejection evidence is present → escalate to
		// server_quota_check rather than skipping. EvidenceFailureOrRejection
		// is the Phase A proxy for "server quota mismatch evidence" (the
		// spec.md wording): the analyzer has no dedicated quota-mismatch
		// evidence source yet, and the only meaningful failure ccusage
		// can emit at this layer is a server-quota divergence. Phase B
		// may refine this into a dedicated server_quota_mismatch evidence
		// source; until then this proxy is intentional. See
		// docs/remediation/token-saving-recommendation-engine.md §"Known
		// Phase A gaps".
		if rule.Class == ClassUsageVisibility &&
			cand.ID == "ccusage" &&
			st == ToolStateActiveHigh &&
			entry.Sources[EvidenceFailureOrRejection] > 0 {
			rec := buildRecommendation(cand, entry, ReasonServerQuotaCheck, signalIDs, ConfidenceHigh)
			return pickResult{Rec: rec, Class: rule.Class, Skipped: skipped}
		}

		switch st {
		case ToolStateActiveHigh:
			skipped = append(skipped, SkipNote{
				ToolID:    cand.ID,
				Reason:    ReasonActivePersistent,
				ForSignal: forSig,
			})
			continue
		case ToolStateRejectedMedium:
			skipped = append(skipped, SkipNote{
				ToolID:    cand.ID,
				Reason:    ReasonRejectedAlternative,
				ForSignal: forSig,
			})
			continue
		case ToolStateConfiguredMedium:
			rec := buildRecommendation(cand, entry, ReasonConfiguredInactive, signalIDs, ConfidenceMedium)
			return pickResult{Rec: rec, Class: rule.Class, Skipped: skipped}
		case ToolStateInstalledMedium:
			rec := buildRecommendation(cand, entry, ReasonInstalledInactive, signalIDs, ConfidenceMedium)
			return pickResult{Rec: rec, Class: rule.Class, Skipped: skipped}
		case ToolStateMentionedLow, ToolStateUnknown:
			rec := buildRecommendation(cand, entry, rule.PrimaryReason, signalIDs, ConfidenceLow)
			return pickResult{Rec: rec, Class: rule.Class, Skipped: skipped}
		default:
			// Defensive: unknown ToolState string → treat as Unknown.
			rec := buildRecommendation(cand, entry, rule.PrimaryReason, signalIDs, ConfidenceLow)
			return pickResult{Rec: rec, Class: rule.Class, Skipped: skipped}
		}
	}

	// All candidates were skipped (active or rejected).
	return pickResult{Rec: nil, Class: rule.Class, Skipped: skipped}
}

// buildRecommendation assembles a TokenSavingRecommendation from a
// registry candidate, the caller's ToolStateEntry for that candidate,
// the chosen Reason, the firing signal IDs, and the derived Confidence.
//
// EvidenceCounts is filtered to the registered EvidenceSource
// allowlist (drops unknown keys defensively, even though ToolStateEntry
// is typed). RiskLevel uses the candidate's InstallRisk; data_movement
// risk is exposed separately on TokenSavingTool but not on the
// recommendation (it stays in the registry, available via GetTool).
func buildRecommendation(
	cand TokenSavingTool,
	entry ToolStateEntry,
	reason Reason,
	signalIDs []Signal,
	conf Confidence,
) *TokenSavingRecommendation {
	allowed := registeredEvidenceSources()
	counts := make(map[EvidenceSource]int, len(entry.Sources))
	for _, src := range sortedEvidenceKeys(entry.Sources) {
		if !allowed[src] {
			continue
		}
		counts[src] = entry.Sources[src]
	}
	return &TokenSavingRecommendation{
		PrimaryToolID:    cand.ID,
		PrimaryToolName:  cand.DisplayName,
		PrimaryToolURL:   cand.SourceURL,
		FailureModes:     failureModesForClass(cand.RecommendationClass),
		Reason:           reason,
		SignalIDs:        signalIDs,
		Confidence:       conf,
		RiskLevel:        cand.InstallRisk,
		DataMovementRisk: cand.DataMovementRisk,
		InstallPolicy:    cand.InstallPolicy,
		InstallSurface:   installSurfaceForTool(cand),
		ConflictsWith:    conflictsForTool(cand.ID),
		AmbiguityWarning: ambiguityWarningForTool(cand.ID),
		EvidenceCounts:   counts,
	}
}

func failureModesForClass(class RecommendationClass) []DefocusFailureMode {
	switch class {
	case ClassShellOutputReducer:
		return []DefocusFailureMode{FailureNoisyTerminalLogs}
	case ClassMCPOutputReducer:
		return []DefocusFailureMode{FailureToolOutputFlooding}
	case ClassRetrieval, ClassRereadGuard:
		return []DefocusFailureMode{FailureRepeatedNavigation}
	case ClassOutputVerbosity:
		return []DefocusFailureMode{FailureBroadReadsOrVerbosity}
	case ClassContextHygiene, ClassUsageVisibility, ClassMCPSkillHygiene:
		return []DefocusFailureMode{FailureCrossCutting}
	default:
		return nil
	}
}

func installSurfaceForClass(class RecommendationClass) string {
	switch class {
	case ClassMCPSkillHygiene:
		return "prune_or_lazy_load_existing_mcp_and_skills"
	case ClassContextHygiene:
		return "session_workflow_and_config_audit"
	default:
		return ""
	}
}

func installSurfaceForTool(tool TokenSavingTool) string {
	switch tool.ID {
	case "rtk":
		return "local_binary_plus_claude_hook"
	case "context_mode":
		return "claude_plugin_plus_mcp"
	case "claude_context":
		return "mcp_plus_external_vector_store"
	case "grepai", "semble":
		return "local_binary_plus_optional_embedding_provider"
	case "ccusage", "ccstatusline", "claude_token_efficient":
		return "local_cli_or_local_config"
	default:
		switch tool.Category {
		case "mcp":
			return "mcp_server"
		case "retrieval":
			return "retrieval_tool"
		case "style":
			return "local_instruction_config"
		default:
			return tool.Category
		}
	}
}

func conflictsForTool(id ToolID) []ToolID {
	switch id {
	case "rtk":
		return []ToolID{"leanctx", "headroom"}
	case "context_mode":
		return []ToolID{"token_optimizer_mcp", "headroom"}
	case "claude_context":
		return []ToolID{"grepai", "serena", "codegraph", "gathon", "semble"}
	case "grepai":
		return []ToolID{"claude_context", "serena", "codegraph", "gathon", "semble"}
	case "semble":
		return []ToolID{"claude_context", "grepai", "serena", "codegraph", "gathon"}
	case "claude_token_efficient":
		return []ToolID{"caveman"}
	default:
		return nil
	}
}

func ambiguityWarningForTool(id ToolID) string {
	if id == "rtk" {
		return "RTK means github.com/rtk-ai/rtk. Do not install or recommend the unrelated npm package named rtk."
	}
	return ""
}

// -----------------------------------------------------------------------------
// Recommendation ID composition
// -----------------------------------------------------------------------------

// composeRecommendationID builds the
// `rec.<class>.<tool_id>.<sorted_signals>` ID documented in
// research.md §2. Empty toolID becomes the literal "none"; empty
// signalIDs becomes the literal "none".
func composeRecommendationID(class RecommendationClass, toolID ToolID, signalIDs []Signal) string {
	sigPart := "none"
	if len(signalIDs) > 0 {
		parts := make([]string, len(signalIDs))
		for i, s := range signalIDs {
			parts[i] = string(s)
		}
		sigPart = strings.Join(parts, "_")
	}
	toolPart := string(toolID)
	if toolPart == "" {
		toolPart = "none"
	}
	return "rec." + string(class) + "." + toolPart + "." + sigPart
}

// -----------------------------------------------------------------------------
// Public entry point
// -----------------------------------------------------------------------------

// Recommend is the engine's only public entry point. Signature is frozen
// by contracts/token_saving_engine_go_api.md.
//
// Invariants:
//
//   - At most one Primary and at most one Secondary; the two (if both
//     present) have different RecommendationClass values.
//   - Skipped is sorted ascending by (ToolID, ForSignal).
//   - Signals is the deduped, sorted echo of the caller's input,
//     filtered to registered Signal values.
//   - UnknownIDCount counts both unknown Signals and unknown ToolIDs.
//   - For identical inputs the JSON of the returned set is byte-equal
//     across runs (NFR-001).
func Recommend(signals []Signal, state ToolStateMap) RecommendationSet {
	// 1. Filter + sort signals; count unknowns.
	sorted := sortedSignalIDs(signals)
	knownSignals := registeredSignals()
	validSignals := make([]Signal, 0, len(sorted))
	unknownCount := 0
	for _, s := range sorted {
		if knownSignals[s] {
			validSignals = append(validSignals, s)
		} else {
			unknownCount++
		}
	}

	// 2. Filter state by registered ToolIDs; count unknowns. Iteration
	//    goes through SortedTools() to satisfy determinism.
	validState := ToolStateMap{}
	for _, tid := range state.SortedTools() {
		if _, ok := GetTool(tid); ok {
			validState[tid] = state[tid]
		} else {
			unknownCount++
		}
	}

	set := RecommendationSet{
		RegistryVersion: RegistryVersion(),
		EngineVersion:   EngineVersion(),
		Signals:         validSignals,
		UnknownIDCount:  unknownCount,
	}

	rules := firingRules(validSignals)
	if len(rules) == 0 {
		return set
	}

	// 3. Primary: from the first firing rule.
	present := make(map[Signal]bool, len(validSignals))
	for _, s := range validSignals {
		present[s] = true
	}

	primary := pickPrimary(rules[0], validState, present)
	set.Skipped = append(set.Skipped, primary.Skipped...)
	if primary.Rec != nil {
		primary.Rec.RecommendationID = composeRecommendationID(
			primary.Class, primary.Rec.PrimaryToolID, primary.Rec.SignalIDs,
		)
		set.Primary = primary.Rec

		// 4. Secondary: first remaining rule with a DIFFERENT class
		//    whose pickPrimary returns a recommendation.
		for _, r := range rules[1:] {
			if r.Class == primary.Class {
				continue
			}
			sec := pickPrimary(r, validState, present)
			set.Skipped = append(set.Skipped, sec.Skipped...)
			if sec.Rec != nil {
				sec.Rec.RecommendationID = composeRecommendationID(
					sec.Class, sec.Rec.PrimaryToolID, sec.Rec.SignalIDs,
				)
				set.Secondary = sec.Rec
				break
			}
		}
	}

	// 5. Sort Skipped deterministically by (ToolID, ForSignal).
	sort.SliceStable(set.Skipped, func(i, j int) bool {
		if set.Skipped[i].ToolID != set.Skipped[j].ToolID {
			return string(set.Skipped[i].ToolID) < string(set.Skipped[j].ToolID)
		}
		return string(set.Skipped[i].ForSignal) < string(set.Skipped[j].ForSignal)
	})

	return set
}

// -----------------------------------------------------------------------------
// Enum allowlists (small helpers; closed sets mirror token_saving_types.go)
// -----------------------------------------------------------------------------

// registeredSignals returns the set of Signal constants the engine
// understands. Keep in sync with the constants in token_saving_types.go.
func registeredSignals() map[Signal]bool {
	return map[Signal]bool{
		SignalNoUsageVisibility:    true,
		SignalToolOutputBloat:      true,
		SignalShellOutputBloat:     true,
		SignalMCPToolOutputBloat:   true,
		SignalRepeatedFileReads:    true,
		SignalBroadRepoExploration: true,
		SignalUnchangedFileRereads: true,
		SignalMCPSkillBloat:        true,
		SignalOutputVerbosity:      true,
		SignalRetryLoop:            true,
		SignalContextGrowthSpikes:  true,
	}
}

// registeredEvidenceSources returns the set of EvidenceSource constants
// the engine understands. Keep in sync with the constants in
// token_saving_types.go.
func registeredEvidenceSources() map[EvidenceSource]bool {
	return map[EvidenceSource]bool{
		EvidenceCLIPresence:          true,
		EvidenceCLIVersion:           true,
		EvidenceLogActiveCommand:     true,
		EvidenceMCPConfigured:        true,
		EvidenceMCPActive:            true,
		EvidencePluginConfigured:     true,
		EvidenceSkillConfigured:      true,
		EvidenceHookConfigured:       true,
		EvidenceStatuslineConfigured: true,
		EvidenceReportMention:        true,
		EvidenceFailureOrRejection:   true,
	}
}

// sortedEvidenceKeys returns the keys of m sorted ascending by string
// value. Used inside buildRecommendation so the filter loop is
// deterministic even though the resulting map's JSON serialisation is
// already sorted by encoding/json.
func sortedEvidenceKeys(m map[EvidenceSource]int) []EvidenceSource {
	keys := make([]EvidenceSource, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return string(keys[i]) < string(keys[j])
	})
	return keys
}
