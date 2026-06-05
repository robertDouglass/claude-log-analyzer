package analyzer

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// validInstallPolicies enumerates the closed set defined by
// data-model.md §InstallPolicy. Kept as a local literal in WP01 so the
// invariant test does not need WP02's constants to exist.
var validInstallPolicies = map[string]bool{
	"bundle":                true,
	"recommend":             true,
	"recommend_with_waiver": true,
	"research_only":         true,
	"reference_only":        true,
}

// validRecommendationClasses enumerates the closed set defined by
// data-model.md §RecommendationClass.
var validRecommendationClasses = map[string]bool{
	"usage_visibility":     true,
	"mcp_skill_hygiene":    true,
	"mcp_output_reducer":   true,
	"shell_output_reducer": true,
	"retrieval":            true,
	"reread_guard":         true,
	"context_hygiene":      true,
	"output_verbosity":     true,
}

// canonicalToolID enforces the lowercase-underscore canonical form.
var canonicalToolID = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// briefAllowlist is the set of ToolIDs the brief explicitly enumerates.
// Every ID listed here must appear in the registry exactly once.
// Registry entries beyond this list are permitted only if their
// InstallPolicy is "reference_only" or "research_only" — i.e. the
// matrix-doc additions must not silently promote a tool to "recommend".
var briefAllowlist = []ToolID{
	"ccusage",
	"tokenusage",
	"claude_meter",
	"ccstatusline",
	"rtk",
	"leanctx",
	"headroom",
	"context_mode",
	"distill",
	"token_optimizer_mcp",
	"serena",
	"codegraph",
	"codebase_memory_mcp",
	"code_review_graph",
	"semble",
	"jcodemunch_mcp",
	"grepai",
	"claude_context",
	"token_savior",
	"cocoindex_code",
	"read_once",
	"openwolf",
	"claude_token_efficient",
	"caveman",
}

func TestRegistryInvariants(t *testing.T) {
	tools := AllTools()

	if len(tools) == 0 {
		t.Fatalf("registry is empty; expected at least the brief allowlist plus reference-only entries")
	}

	seenIDs := make(map[ToolID]int, len(tools))
	type classRankKey struct {
		class string
		rank  int
	}
	seenClassRank := make(map[classRankKey]ToolID, len(tools))

	for _, tool := range tools {
		// ID non-empty and canonical form.
		if tool.ID == "" {
			t.Errorf("registry entry has empty ID: %+v", tool)
			continue
		}
		if !canonicalToolID.MatchString(string(tool.ID)) {
			t.Errorf("tool %q: ID does not match canonical form ^[a-z][a-z0-9_]*$", tool.ID)
		}

		// Uniqueness.
		if prior, dup := seenIDs[tool.ID]; dup {
			t.Errorf("tool %q: duplicate ID (first seen at index %d)", tool.ID, prior)
		}
		seenIDs[tool.ID] = len(seenIDs)

		// InstallPolicy in the closed set. tool.InstallPolicy is the
		// named type InstallPolicy (declared in token_saving_types.go,
		// WP02); convert to string for map lookup against the
		// WP01-local literal set.
		if !validInstallPolicies[string(tool.InstallPolicy)] {
			t.Errorf("tool %q: InstallPolicy %q is not in the documented set", tool.ID, tool.InstallPolicy)
		}

		// ResearchOnly ↔ InstallPolicy == research_only.
		wantResearchOnly := tool.InstallPolicy == "research_only"
		if tool.ResearchOnly != wantResearchOnly {
			t.Errorf("tool %q: ResearchOnly=%v but InstallPolicy=%q (want ResearchOnly=%v)",
				tool.ID, tool.ResearchOnly, tool.InstallPolicy, wantResearchOnly)
		}

		// recommend_with_waiver ⇒ RollbackGuidance non-empty.
		if tool.InstallPolicy == "recommend_with_waiver" && tool.RollbackGuidance == "" {
			t.Errorf("tool %q: InstallPolicy=recommend_with_waiver requires non-empty RollbackGuidance", tool.ID)
		}

		// RecommendationClass in the closed set. Convert the named
		// type to string for map lookup (see note above).
		if !validRecommendationClasses[string(tool.RecommendationClass)] {
			t.Errorf("tool %q: RecommendationClass %q is not in the documented set",
				tool.ID, tool.RecommendationClass)
		}

		// (class, rank) pairs unique.
		key := classRankKey{class: string(tool.RecommendationClass), rank: tool.ClassRank}
		if prior, dup := seenClassRank[key]; dup {
			t.Errorf("tool %q: duplicate (class=%q, rank=%d) — also claimed by %q",
				tool.ID, key.class, key.rank, prior)
		} else {
			seenClassRank[key] = tool.ID
		}

		// SourceURL non-empty unless ResearchOnly.
		if tool.SourceURL == "" && !tool.ResearchOnly {
			t.Errorf("tool %q: empty SourceURL requires ResearchOnly=true", tool.ID)
		}

		// Rank-99 is the sentinel for reference-only entries (architecture
		// references, ecosystem indices) that must never become default
		// recommendations even if their InstallPolicy is later edited.
		// Mission-review RISK-10: tighten the invariant so a maintainer
		// who promotes a rank-99 entry to "recommend" trips this test.
		if tool.ClassRank == 99 && tool.InstallPolicy != "reference_only" {
			t.Errorf("tool %q: ClassRank=99 is reserved for reference_only entries; got InstallPolicy=%q",
				tool.ID, tool.InstallPolicy)
		}
	}
}

func TestRecommendableToolsHavePreciseSourceURLs(t *testing.T) {
	for _, tool := range AllTools() {
		switch tool.InstallPolicy {
		case PolicyBundle, PolicyRecommend, PolicyRecommendWithWaiver:
			if !isHTTPSRegistryURL(tool.SourceURL) {
				t.Errorf("recommendable tool %q has non-HTTPS SourceURL %q", tool.ID, tool.SourceURL)
			}
		}
	}
}

func TestRTKDisambiguatesUnrelatedNPMPackage(t *testing.T) {
	tool, ok := GetTool("rtk")
	if !ok {
		t.Fatal("rtk missing from registry")
	}
	if tool.DisplayName != "RTK (Rust Token Killer, rtk-ai/rtk)" {
		t.Fatalf("rtk DisplayName = %q", tool.DisplayName)
	}
	if tool.SourceURL != "https://github.com/rtk-ai/rtk" {
		t.Fatalf("rtk SourceURL = %q", tool.SourceURL)
	}
	if !strings.Contains(tool.Notes, "not the unrelated npm package named rtk") {
		t.Fatalf("rtk Notes must disambiguate npm package: %q", tool.Notes)
	}
}

func TestCodeGraphResearchOnlyAfterNegativeBenchmark(t *testing.T) {
	tool, ok := GetTool("codegraph")
	if !ok {
		t.Fatal("codegraph missing from registry")
	}
	if tool.SourceURL != "https://github.com/colbymchenry/codegraph" {
		t.Fatalf("codegraph SourceURL = %q", tool.SourceURL)
	}
	if tool.InstallPolicy != PolicyResearchOnly || !tool.ResearchOnly {
		t.Fatalf("codegraph must remain research-only after negative benchmark proof; got policy=%q research_only=%v",
			tool.InstallPolicy, tool.ResearchOnly)
	}
	if tool.FreeReportAllowed || tool.PaidPackAllowed {
		t.Fatalf("codegraph must not be emitted in report packs before promotion; free=%v paid=%v",
			tool.FreeReportAllowed, tool.PaidPackAllowed)
	}
	if !strings.Contains(strings.ToLower(tool.Notes), "increased tokens and cost") {
		t.Fatalf("codegraph Notes must explain negative benchmark result: %q", tool.Notes)
	}
}

func TestHeadroomResearchOnlyAfterDiagnosticBenchmark(t *testing.T) {
	tool, ok := GetTool("headroom")
	if !ok {
		t.Fatal("headroom missing from registry")
	}
	if tool.SourceURL != "https://github.com/chopratejas/headroom" {
		t.Fatalf("headroom SourceURL = %q", tool.SourceURL)
	}
	if tool.InstallPolicy != PolicyResearchOnly || !tool.ResearchOnly {
		t.Fatalf("headroom must remain research-only after mixed diagnostic proof; got policy=%q research_only=%v",
			tool.InstallPolicy, tool.ResearchOnly)
	}
	if tool.FreeReportAllowed || tool.PaidPackAllowed {
		t.Fatalf("headroom must not be emitted in report packs before promotion; free=%v paid=%v",
			tool.FreeReportAllowed, tool.PaidPackAllowed)
	}
	notes := strings.ToLower(tool.Notes)
	if !strings.Contains(notes, "proxy diagnostic increased") || !strings.Contains(notes, "not recommended") {
		t.Fatalf("headroom Notes must explain mixed diagnostic result: %q", tool.Notes)
	}
}

func TestRegistryAllowlistCoverage(t *testing.T) {
	// Step 1: brief allowlist has no internal duplicates.
	seenBrief := make(map[ToolID]int, len(briefAllowlist))
	for i, id := range briefAllowlist {
		if prior, dup := seenBrief[id]; dup {
			t.Errorf("briefAllowlist has duplicate %q at indices %d and %d", id, prior, i)
		}
		seenBrief[id] = i
	}

	// Step 2: every brief ID is present in the registry exactly once.
	for _, id := range briefAllowlist {
		got, ok := GetTool(id)
		if !ok {
			t.Errorf("brief allowlist tool %q is missing from the registry", id)
			continue
		}
		if got.ID != id {
			t.Errorf("GetTool(%q) returned entry with ID %q", id, got.ID)
		}
	}

	// Step 3: any registry entry beyond the brief list must be
	// reference_only or research_only. The matrix-doc additions must
	// not silently promote a tool to "recommend".
	briefSet := make(map[ToolID]struct{}, len(briefAllowlist))
	for _, id := range briefAllowlist {
		briefSet[id] = struct{}{}
	}
	for _, tool := range AllTools() {
		if _, inBrief := briefSet[tool.ID]; inBrief {
			continue
		}
		switch tool.InstallPolicy {
		case "reference_only", "research_only":
			// OK
		default:
			t.Errorf("non-brief registry entry %q has InstallPolicy=%q; "+
				"matrix-doc additions must ship reference_only or research_only",
				tool.ID, tool.InstallPolicy)
		}
	}
}

func TestRegistryVersionConstant(t *testing.T) {
	if got, want := RegistryVersion(), "phase-a-2026-06-05-headroom-proxy-negative-benchmark"; got != want {
		t.Errorf("RegistryVersion() = %q, want %q", got, want)
	}
}

func isHTTPSRegistryURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

func TestAllToolsSortedByClassAndRank(t *testing.T) {
	tools := AllTools()
	for i := 1; i < len(tools); i++ {
		prev, cur := tools[i-1], tools[i]
		if prev.RecommendationClass > cur.RecommendationClass {
			t.Errorf("AllTools() not sorted: %q (class=%q) precedes %q (class=%q)",
				prev.ID, prev.RecommendationClass, cur.ID, cur.RecommendationClass)
			continue
		}
		if prev.RecommendationClass == cur.RecommendationClass && prev.ClassRank > cur.ClassRank {
			t.Errorf("AllTools() not sorted within class %q: rank %d (%q) precedes rank %d (%q)",
				prev.RecommendationClass, prev.ClassRank, prev.ID, cur.ClassRank, cur.ID)
		}
	}
}

func TestGetToolUnknownReturnsZero(t *testing.T) {
	got, ok := GetTool(ToolID("does_not_exist_xyz"))
	if ok {
		t.Errorf("GetTool(unknown) returned ok=true with %+v", got)
	}
	// TokenSavingTool contains a slice field, so it is not == comparable.
	// Check the discriminating fields a caller would observe instead.
	if got.ID != "" || got.DisplayName != "" || got.SourceURL != "" ||
		got.InstallPolicy != "" || got.RecommendationClass != "" ||
		got.DetectorSources != nil {
		t.Errorf("GetTool(unknown) returned non-zero entry: %+v", got)
	}
}
