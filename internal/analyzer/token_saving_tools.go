// Package analyzer — token-saving tool registry.
//
// WP01 of mission token-saving-recommendation-engine-phase-a-01KRZKCJ.
//
// This file defines the immutable allowlist of token-saving tools the
// Phase A recommendation engine may emit. It is the canonical source of
// truth for tool IDs, recommendation classes, install policies, and
// per-tool detector evidence sources.
//
// Named-type bridge to WP02
// -------------------------
// The struct below references the enum-typed fields RecommendationClass,
// RiskLevel, InstallPolicy, and EvidenceSource. Their named-type
// declarations (`type Foo string`) and constants live in
// token_saving_types.go (WP02). The registry literal below uses
// untyped string literals (e.g. "recommend", "usage_visibility") which
// Go implicitly converts to the named types per the composite-literal
// element-type and struct-field-type, so this file remains valid
// without re-declaring those names.
package analyzer

import "sort"

// ToolID is the canonical allowlist identifier for a token-saving tool.
// Canonical form is lowercase + underscore-separated (e.g. "ccusage",
// "context_mode", "claude_code_usage_monitor"). Any ToolID that is not
// present in AllTools() is treated as unknown by the engine.
type ToolID string

// registryVersion is bumped whenever the registry literal below changes
// in any observable way (added/removed tool, field edit, ordering
// change). NFR-005 in spec.md gates this; a CI test compares the live
// value to a checked-in golden constant.
const registryVersion = "phase-a-2026-06-05-headroom-diagnostic-benchmark"

// TokenSavingTool is one immutable registry entry. The struct shape is
// frozen by contracts/token_saving_engine_go_api.md.
type TokenSavingTool struct {
	ID                  ToolID              `json:"id"`
	DisplayName         string              `json:"display_name"`
	SourceURL           string              `json:"source_url"`
	Category            string              `json:"category"`
	RecommendationClass RecommendationClass `json:"recommendation_class"`
	ClassRank           int                 `json:"class_rank"`
	DetectorSources     []EvidenceSource    `json:"detector_sources"`
	InstallRisk         RiskLevel           `json:"install_risk"`
	DataMovementRisk    RiskLevel           `json:"data_movement_risk"`
	RollbackGuidance    string              `json:"rollback_guidance,omitempty"`
	FreeReportAllowed   bool                `json:"free_report_allowed"`
	PaidPackAllowed     bool                `json:"paid_pack_allowed"`
	ResearchOnly        bool                `json:"research_only"`
	InstallPolicy       InstallPolicy       `json:"install_policy"`
	Notes               string              `json:"notes,omitempty"`
}

// registry is the package-private, immutable allowlist. Ordering inside
// the literal mirrors AllTools()'s sort: by RecommendationClass group,
// then ascending ClassRank.
var registry = []TokenSavingTool{
	// ── usage_visibility ────────────────────────────────────────────
	{
		ID:                  "ccusage",
		DisplayName:         "ccusage",
		SourceURL:           "https://github.com/ryoppippi/ccusage",
		Category:            "observability",
		RecommendationClass: "usage_visibility",
		ClassRank:           1,
		DetectorSources:     []EvidenceSource{"cli_presence", "cli_version", "log_active_command"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   true,
		PaidPackAllowed:     true,
		ResearchOnly:        false,
		InstallPolicy:       "recommend",
		Notes:               "Independent accounting layer; telemetry only, not a direct reducer.",
	},
	{
		ID:                  "ccstatusline",
		DisplayName:         "ccstatusline",
		SourceURL:           "https://github.com/sirmalloc/ccstatusline",
		Category:            "observability",
		RecommendationClass: "usage_visibility",
		ClassRank:           2,
		DetectorSources:     []EvidenceSource{"cli_presence", "statusline_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        false,
		InstallPolicy:       "reference_only",
		Notes:               "Statusline awareness outside the prompt path; telemetry only, not a direct reducer.",
	},
	{
		ID:                  "claude_code_usage_monitor",
		DisplayName:         "Claude Code Usage Monitor",
		SourceURL:           "https://github.com/Maciek-roboblog/Claude-Code-Usage-Monitor",
		Category:            "observability",
		RecommendationClass: "usage_visibility",
		ClassRank:           3,
		DetectorSources:     []EvidenceSource{"report_mention"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        false,
		InstallPolicy:       "reference_only",
		Notes:               "External monitor; does not live inside the plugin runtime.",
	},
	{
		ID:                  "claude_code_usage_tracker",
		DisplayName:         "Claude Code Usage Tracker",
		SourceURL:           "https://github.com/LyndonWangWork/Claude-Code-Usage-Tracker",
		Category:            "observability",
		RecommendationClass: "usage_visibility",
		ClassRank:           4,
		DetectorSources:     []EvidenceSource{"report_mention"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        false,
		InstallPolicy:       "reference_only",
		Notes:               "External tracker; reference-only, not bundled.",
	},
	{
		ID:                  "tokenusage",
		DisplayName:         "tokenusage",
		SourceURL:           "",
		Category:            "observability",
		RecommendationClass: "usage_visibility",
		ClassRank:           5,
		DetectorSources:     []EvidenceSource{"cli_presence", "report_mention"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Brief allowlist; canonical source URL unverified — Phase B verification gap.",
	},
	{
		ID:                  "claude_meter",
		DisplayName:         "claude_meter",
		SourceURL:           "",
		Category:            "observability",
		RecommendationClass: "usage_visibility",
		ClassRank:           6,
		DetectorSources:     []EvidenceSource{"cli_presence", "report_mention"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Brief allowlist; canonical source URL unverified — Phase B verification gap.",
	},

	// ── mcp_output_reducer ──────────────────────────────────────────
	{
		ID:                  "context_mode",
		DisplayName:         "context-mode",
		SourceURL:           "https://github.com/mksglu/context-mode",
		Category:            "mcp",
		RecommendationClass: "mcp_output_reducer",
		ClassRank:           1,
		DetectorSources:     []EvidenceSource{"mcp_configured", "mcp_active"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   true,
		PaidPackAllowed:     true,
		ResearchOnly:        false,
		InstallPolicy:       "recommend",
		Notes:               "MCP-side context reducer; primary candidate for mcp_tool_output_bloat.",
	},
	{
		ID:                  "distill",
		DisplayName:         "distill",
		SourceURL:           "",
		Category:            "mcp",
		RecommendationClass: "mcp_output_reducer",
		ClassRank:           2,
		DetectorSources:     []EvidenceSource{"mcp_configured", "mcp_active"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Brief allowlist; canonical source URL unverified — Phase B verification gap.",
	},
	{
		ID:                  "token_optimizer_mcp",
		DisplayName:         "token-optimizer (MCP)",
		SourceURL:           "",
		Category:            "mcp",
		RecommendationClass: "mcp_output_reducer",
		ClassRank:           3,
		DetectorSources:     []EvidenceSource{"mcp_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Brief flags as research / low confidence; URL unverified — Phase B verification gap.",
	},

	// ── shell_output_reducer ────────────────────────────────────────
	{
		ID:                  "rtk",
		DisplayName:         "RTK (Rust Token Killer, rtk-ai/rtk)",
		SourceURL:           "https://github.com/rtk-ai/rtk",
		Category:            "shell",
		RecommendationClass: "shell_output_reducer",
		ClassRank:           1,
		DetectorSources:     []EvidenceSource{"cli_presence", "hook_configured", "log_active_command"},
		InstallRisk:         "high",
		DataMovementRisk:    "high",
		RollbackGuidance:    "Remove the RTK hook from `.claude/settings.json` and restart the session.",
		FreeReportAllowed:   true,
		PaidPackAllowed:     true,
		ResearchOnly:        false,
		InstallPolicy:       "recommend_with_waiver",
		Notes:               "Rewrites shell command execution. This is github.com/rtk-ai/rtk, not the unrelated npm package named rtk.",
	},
	{
		ID:                  "leanctx",
		DisplayName:         "leanctx",
		SourceURL:           "",
		Category:            "shell",
		RecommendationClass: "shell_output_reducer",
		ClassRank:           2,
		DetectorSources:     []EvidenceSource{"cli_presence", "hook_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Brief allowlist; canonical source URL unverified — Phase B verification gap.",
	},
	{
		ID:                  "headroom",
		DisplayName:         "Headroom",
		SourceURL:           "https://github.com/chopratejas/headroom",
		Category:            "shell",
		RecommendationClass: "shell_output_reducer",
		ClassRank:           3,
		DetectorSources:     []EvidenceSource{"cli_presence", "hook_configured"},
		InstallRisk:         "medium",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Source reviewed as chopratejas/headroom v0.23.0; 3x diagnostic benchmark was mixed/noisy with one token/cost regression and only small mean savings, so it is not recommended.",
	},

	// ── retrieval ───────────────────────────────────────────────────
	{
		ID:                  "claude_context",
		DisplayName:         "claude-context",
		SourceURL:           "https://github.com/zilliztech/claude-context",
		Category:            "retrieval",
		RecommendationClass: "retrieval",
		ClassRank:           90,
		DetectorSources:     []EvidenceSource{"mcp_configured", "mcp_active"},
		InstallRisk:         "high",
		DataMovementRisk:    "high",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Repeated benchmark was negative on the current fixture; revisit only with a larger retrieval-amortization benchmark.",
	},
	{
		ID:                  "grepai",
		DisplayName:         "grepai",
		SourceURL:           "https://github.com/yoanbernabeu/grepai",
		Category:            "retrieval",
		RecommendationClass: "retrieval",
		ClassRank:           2,
		DetectorSources:     []EvidenceSource{"cli_presence", "cli_version"},
		InstallRisk:         "medium",
		DataMovementRisk:    "medium",
		FreeReportAllowed:   true,
		PaidPackAllowed:     true,
		ResearchOnly:        false,
		InstallPolicy:       "recommend",
		Notes:               "Local-first; requires embedding provider setup.",
	},
	{
		ID:                  "serena",
		DisplayName:         "serena",
		SourceURL:           "",
		Category:            "retrieval",
		RecommendationClass: "retrieval",
		ClassRank:           3,
		DetectorSources:     []EvidenceSource{"mcp_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Brief allowlist; canonical source URL unverified — Phase B promotion expected once verified.",
	},
	{
		ID:                  "codegraph",
		DisplayName:         "CodeGraph",
		SourceURL:           "https://github.com/colbymchenry/codegraph",
		Category:            "retrieval",
		RecommendationClass: "retrieval",
		ClassRank:           4,
		DetectorSources:     []EvidenceSource{"mcp_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Source reviewed as @colbymchenry/codegraph; 3x diagnostic benchmark increased tokens and cost, so it remains research-only.",
	},
	{
		ID:                  "codebase_memory_mcp",
		DisplayName:         "codebase-memory-mcp",
		SourceURL:           "",
		Category:            "retrieval",
		RecommendationClass: "retrieval",
		ClassRank:           5,
		DetectorSources:     []EvidenceSource{"mcp_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Brief allowlist; canonical source URL unverified — Phase B verification gap.",
	},
	{
		ID:                  "code_review_graph",
		DisplayName:         "code-review-graph",
		SourceURL:           "",
		Category:            "retrieval",
		RecommendationClass: "retrieval",
		ClassRank:           6,
		DetectorSources:     []EvidenceSource{"mcp_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Brief allowlist; canonical source URL unverified — Phase B verification gap.",
	},
	{
		ID:                  "semble",
		DisplayName:         "semble",
		SourceURL:           "https://github.com/MinishLab/semble",
		Category:            "retrieval",
		RecommendationClass: "retrieval",
		ClassRank:           1,
		DetectorSources:     []EvidenceSource{"cli_presence", "mcp_configured"},
		InstallRisk:         "medium",
		DataMovementRisk:    "medium",
		FreeReportAllowed:   true,
		PaidPackAllowed:     true,
		ResearchOnly:        false,
		InstallPolicy:       "recommend",
		Notes:               "Best repeated retrieval result on the noisy fixture when used with path limits.",
	},
	{
		ID:                  "jcodemunch_mcp",
		DisplayName:         "jcodemunch-mcp",
		SourceURL:           "",
		Category:            "retrieval",
		RecommendationClass: "retrieval",
		ClassRank:           8,
		DetectorSources:     []EvidenceSource{"mcp_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Brief allowlist; canonical source URL unverified — Phase B verification gap.",
	},
	{
		ID:                  "token_savior",
		DisplayName:         "token-savior",
		SourceURL:           "",
		Category:            "retrieval",
		RecommendationClass: "retrieval",
		ClassRank:           9,
		DetectorSources:     []EvidenceSource{"mcp_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Brief allowlist; canonical source URL unverified — Phase B verification gap.",
	},
	{
		ID:                  "cocoindex_code",
		DisplayName:         "cocoindex-code",
		SourceURL:           "",
		Category:            "retrieval",
		RecommendationClass: "retrieval",
		ClassRank:           10,
		DetectorSources:     []EvidenceSource{"mcp_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Brief allowlist; canonical source URL unverified — Phase B verification gap.",
	},

	// ── reread_guard ────────────────────────────────────────────────
	{
		ID:                  "read_once",
		DisplayName:         "read-once",
		SourceURL:           "",
		Category:            "memory",
		RecommendationClass: "reread_guard",
		ClassRank:           1,
		DetectorSources:     []EvidenceSource{"hook_configured", "skill_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Brief allowlist; canonical source URL unverified — Phase B verification gap.",
	},
	{
		ID:                  "openwolf",
		DisplayName:         "openwolf",
		SourceURL:           "",
		Category:            "memory",
		RecommendationClass: "reread_guard",
		ClassRank:           2,
		DetectorSources:     []EvidenceSource{"hook_configured", "skill_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Brief allowlist; canonical source URL unverified — Phase B verification gap.",
	},
	{
		ID:                  "memsearch",
		DisplayName:         "memsearch",
		SourceURL:           "https://github.com/zilliztech/memsearch",
		Category:            "memory",
		RecommendationClass: "reread_guard",
		ClassRank:           3,
		DetectorSources:     []EvidenceSource{"mcp_configured", "skill_configured"},
		InstallRisk:         "medium",
		DataMovementRisk:    "medium",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Matrix doc flags as too stateful for the initial pack; URL verified, behavior research-only.",
	},

	// ── output_verbosity ────────────────────────────────────────────
	{
		ID:                  "claude_token_efficient",
		DisplayName:         "claude-token-efficient",
		SourceURL:           "https://github.com/drona23/claude-token-efficient",
		Category:            "style",
		RecommendationClass: "output_verbosity",
		ClassRank:           1,
		DetectorSources:     []EvidenceSource{"skill_configured", "plugin_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Repeated benchmark showed only small/noisy savings; keep as manual verbosity hygiene, not a default reducer.",
	},
	{
		ID:                  "caveman",
		DisplayName:         "caveman",
		SourceURL:           "https://github.com/JuliusBrussee/caveman",
		Category:            "style",
		RecommendationClass: "output_verbosity",
		ClassRank:           2,
		DetectorSources:     []EvidenceSource{"skill_configured", "plugin_configured"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        true,
		InstallPolicy:       "research_only",
		Notes:               "Opt-in only; URL verified, ships research_only per matrix doc.",
	},

	// ── reference-only (no recommendation class promotion) ──────────
	{
		ID:                  "claude_code_hooks_mastery",
		DisplayName:         "Claude Code Hooks Mastery",
		SourceURL:           "https://github.com/disler/claude-code-hooks-mastery",
		Category:            "reference",
		RecommendationClass: "reread_guard",
		ClassRank:           99,
		DetectorSources:     []EvidenceSource{"report_mention"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        false,
		InstallPolicy:       "reference_only",
		Notes:               "Reference architecture for hook authors; not a runtime tool.",
	},
	{
		ID:                  "awesome_claude_code",
		DisplayName:         "awesome-claude-code",
		SourceURL:           "https://github.com/hesreallyhim/awesome-claude-code",
		Category:            "reference",
		RecommendationClass: "output_verbosity",
		ClassRank:           99,
		DetectorSources:     []EvidenceSource{"report_mention"},
		InstallRisk:         "low",
		DataMovementRisk:    "low",
		FreeReportAllowed:   false,
		PaidPackAllowed:     false,
		ResearchOnly:        false,
		InstallPolicy:       "reference_only",
		Notes:               "Discovery index; reference only.",
	},
}

// GetTool returns the registry entry for id, or (zero, false) if id is
// not in the allowlist. Pure; safe to call concurrently.
func GetTool(id ToolID) (TokenSavingTool, bool) {
	for i := range registry {
		if registry[i].ID == id {
			return registry[i], true
		}
	}
	return TokenSavingTool{}, false
}

// AllTools returns a defensive copy of the registry, sorted by
// (RecommendationClass, ClassRank) ascending. Pure; safe to call
// concurrently. Callers may mutate the returned slice without affecting
// the package-private registry.
func AllTools() []TokenSavingTool {
	out := make([]TokenSavingTool, len(registry))
	copy(out, registry)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RecommendationClass != out[j].RecommendationClass {
			return out[i].RecommendationClass < out[j].RecommendationClass
		}
		return out[i].ClassRank < out[j].ClassRank
	})
	return out
}

// RegistryVersion returns the registry's stable identifier (e.g.
// "phase-a-2026-05-20-tool-url-audit"). The value changes only when the registry
// literal changes; a CI test guards this invariant (NFR-005).
func RegistryVersion() string {
	return registryVersion
}
