package analyzer

import (
	"errors"
	"sort"
)

func AggregateReports(jobID string, reports []Report, inputSize int) (Report, error) {
	return AggregateReportsWithParserType(jobID, reports, inputSize, "paid_bundle")
}

func AggregateReportsWithParserType(jobID string, reports []Report, inputSize int, parserType string) (Report, error) {
	if len(reports) == 0 {
		return Report{}, errors.New("no reports to aggregate")
	}
	if parserType == "" {
		parserType = "paid_bundle"
	}
	var metrics Metrics
	metrics.SessionCount = len(reports)
	redactions := map[string]int{}
	ecosystem := Ecosystem{}
	signals := AnalysisSignals{}
	for _, report := range reports {
		metrics.Turns += report.Metrics.Turns
		metrics.EstimatedTokens += report.Metrics.EstimatedTokens
		metrics.ToolOutputTokens += report.Metrics.ToolOutputTokens
		metrics.Rereads += report.Metrics.Rereads
		metrics.FailedCommands += report.Metrics.FailedCommands
		metrics.ContextGrowthEvents += report.Metrics.ContextGrowthEvents
		if report.Metrics.RetryDepthMax > metrics.RetryDepthMax {
			metrics.RetryDepthMax = report.Metrics.RetryDepthMax
		}
		for family, count := range report.Redactions {
			redactions[family] += count
		}
		ecosystem = mergeEcosystems(ecosystem, report.Ecosystem)
		signals = mergeSignals(signals, report.AnalysisSignals)
	}
	timeline := aggregateTimeline(reports)
	signals.SampleConfidence, signals.SampleWarnings = sampleConfidence(len(reports), signals)
	findings := aggregateFindings(metrics)
	findings = appendSignalFindings(findings, signals)
	pluginSavings := estimatePluginSavings(metrics, findings, signals)
	score := scoreFromSavings(pluginSavings)
	report := Report{
		JobID:          jobID,
		Version:        Version,
		Score:          score,
		EstimatedWaste: wasteRangeFromSavings(pluginSavings),
		Metrics:        metrics,
		Findings:       findings,
		Ecosystem:      ecosystem,
		Redactions:     redactions,
		SecurityReceipt: SecurityReceipt{
			RawTranscriptSentToLLM: false,
			OutboundDuringAnalysis: false,
			SecretsRedacted:        sumRedactions(redactions),
			RawLogTTL:              "15m",
		},
		Timeline:        timeline,
		AnalysisSignals: signals,
		PluginSavings:   pluginSavings,
		ImmediateFixes:  immediateFixes(findings),
	}
	normalizeReportCollections(&report)
	report.AggregateEvent = aggregateEvent(report, parserType, inputSize)
	AttachRecommendation(&report)
	AttachBaselineReceipt(&report, BaselineOptions{InputSizeBytes: inputSize})
	return report, nil
}

func aggregateTimeline(reports []Report) []TimelinePoint {
	if len(reports) != 1 {
		return nil
	}
	if len(reports[0].Timeline) == 0 {
		return nil
	}
	timeline := make([]TimelinePoint, len(reports[0].Timeline))
	copy(timeline, reports[0].Timeline)
	return timeline
}

func aggregateFindings(metrics Metrics) []Finding {
	var findings []Finding
	if metrics.Rereads >= 3 {
		findings = append(findings, Finding{
			ID:          "repeated_file_reads",
			Title:       "Excessive repeated file reads",
			FailureMode: FailureRepeatedNavigation,
			Severity:    severity(metrics.Rereads, 3, 25),
			CostImpact:  "medium-high",
			Evidence: FindingEvidence{
				Count:       metrics.Rereads,
				Description: "Repeated reads across analyzed sessions",
			},
			Recommendation: "Prefer targeted searches and summarize file state before rereading the same files.",
			Deterministic:  true,
		})
	}
	if metrics.ToolOutputTokens > 0 && metrics.EstimatedTokens > 0 {
		share := int(float64(metrics.ToolOutputTokens) / float64(metrics.EstimatedTokens) * 100)
		if share >= 35 {
			findings = append(findings, Finding{
				ID:          "tool_output_bloat",
				Title:       "Large shell/tool output overhead",
				FailureMode: FailureToolOutputFlooding,
				Severity:    severity(share, 35, 55),
				CostImpact:  "high",
				Evidence: FindingEvidence{
					TokenShare:  share,
					Description: "Tool output share across analyzed sessions",
				},
				Recommendation: "Cap command output and use narrower queries before pasting long terminal output into context.",
				Deterministic:  true,
			})
		}
	}
	if metrics.RetryDepthMax >= 3 {
		findings = append(findings, Finding{
			ID:          "retry_loop",
			Title:       "Retry-loop behavior",
			FailureMode: FailureCrossCutting,
			Severity:    severity(metrics.RetryDepthMax, 3, 6),
			CostImpact:  "medium",
			Evidence: FindingEvidence{
				Count:       metrics.RetryDepthMax,
				Description: "Maximum retry depth across analyzed sessions",
			},
			Recommendation: "Stop after repeated failures, inspect the invariant, and restart with a smaller debugging scope.",
			Deterministic:  true,
		})
	}
	if metrics.ContextGrowthEvents >= 2 {
		findings = append(findings, Finding{
			ID:          "context_growth_spikes",
			Title:       "Context growth spikes",
			FailureMode: FailureToolOutputFlooding,
			Severity:    severity(metrics.ContextGrowthEvents, 2, 5),
			CostImpact:  "medium",
			Evidence: FindingEvidence{
				Count:       metrics.ContextGrowthEvents,
				Description: "Timeline windows exceeded growth threshold across analyzed sessions",
			},
			Recommendation: "Compact after task pivots and avoid combining architecture, debugging, and implementation in one long session.",
			Deterministic:  true,
		})
	}
	return findings
}

func mergeEcosystems(left, right Ecosystem) Ecosystem {
	left.Client = firstNonEmpty(left.Client, right.Client)
	left.OperatingSystem = firstNonEmpty(left.OperatingSystem, right.OperatingSystem)
	left.Shell = firstNonEmpty(left.Shell, right.Shell)
	left.VersionControl = firstNonEmpty(left.VersionControl, right.VersionControl)
	left.CodingAgents = mergeStrings(left.CodingAgents, right.CodingAgents)
	left.WorkflowFrameworks = mergeStrings(left.WorkflowFrameworks, right.WorkflowFrameworks)
	left.MCPServersKnown = mergeStrings(left.MCPServersKnown, right.MCPServersKnown)
	left.KnownSkills = mergeStrings(left.KnownSkills, right.KnownSkills)
	left.KnownPlugins = mergeStrings(left.KnownPlugins, right.KnownPlugins)
	left.PackageManagers = mergeStrings(left.PackageManagers, right.PackageManagers)
	left.UnknownMCPServerCount += right.UnknownMCPServerCount
	left.UnknownSkillCount += right.UnknownSkillCount
	left.UnknownPluginCount += right.UnknownPluginCount
	// FR-007/FR-008: merge the two newer Ecosystem fields across input
	// reports. See contracts/aggregate-merge.md for definitive rules.
	// Bucket fields hold argmax_rank because bucket boundary recomputation
	// would require summed exposure counts which are not part of the
	// ToolingUtilization struct shape (C-001: shape-locked).
	left.ToolingUtilization.MCP = mergeMCPUtilization(left.ToolingUtilization.MCP, right.ToolingUtilization.MCP)
	left.ToolingUtilization.Skill = mergeSkillUtilization(left.ToolingUtilization.Skill, right.ToolingUtilization.Skill)
	left.WorkflowFingerprints = mergeWorkflowFingerprints(left.WorkflowFingerprints, right.WorkflowFingerprints)
	return left
}

func mergeStrings(left, right []string) []string {
	seen := map[string]bool{}
	for _, value := range left {
		if value != "" {
			seen[value] = true
		}
	}
	for _, value := range right {
		if value != "" {
			seen[value] = true
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(left, right string) string {
	if left != "" {
		return left
	}
	return right
}

// unionSorted returns the deduplicated, ascending-sorted union of a and b.
// Empty strings are dropped. Inputs are not mutated; output is a fresh slice.
// Returns nil when both inputs are empty (so JSON `omitempty` drops the
// field rather than emitting `null` or `[]`).
func unionSorted(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(a)+len(b))
	for _, v := range a {
		if v != "" {
			seen[v] = true
		}
	}
	for _, v := range b {
		if v != "" {
			seen[v] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// warningBandRank returns the rank of a warning band; higher = more severe.
// severe > high > watch > normal > unknown.
// Unknown values rank below "unknown" so the higher of two known values wins
// even when one input is malformed.
func warningBandRank(band string) int {
	switch band {
	case WarningBandSevere:
		return 4
	case WarningBandHigh:
		return 3
	case WarningBandWatch:
		return 2
	case WarningBandNormal:
		return 1
	case WarningBandUnknown:
		return 0
	default:
		return -1
	}
}

// maxWarningBand returns the higher of two warning bands by rank.
// When both are empty/unrecognized, returns the empty string.
func maxWarningBand(a, b string) string {
	ra := warningBandRank(a)
	rb := warningBandRank(b)
	if ra >= rb {
		return a
	}
	return b
}

// confidenceRank returns the rank of a confidence value; higher = stronger.
// high > medium > low. Unknown values rank below "low" so a known value
// always beats an empty/garbage value.
func confidenceRank(c string) int {
	switch c {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// maxConfidence returns the higher of two confidence values by rank.
func maxConfidence(a, b string) string {
	if confidenceRank(a) >= confidenceRank(b) {
		return a
	}
	return b
}

// maxInferenceSource picks the higher-authority inference source across two
// inputs. Ranks (high to low): header > calls > none > "". The header value
// is the most authoritative because it reflects a structured exposure record
// rather than a derived signal. Choosing by rank (rather than first-wins)
// keeps the merge commutative across input order.
func maxInferenceSource(a, b string) string {
	rank := func(s string) int {
		switch s {
		case InferenceSourceHeader:
			return 3
		case InferenceSourceCalls:
			return 2
		case InferenceSourceNone:
			return 1
		default:
			return 0
		}
	}
	if rank(a) >= rank(b) {
		return a
	}
	return b
}

// maxBucketRank holds the argmax_rank of two closed-enum bucket labels.
// Used when bucket boundary recomputation is not feasible at the merge layer
// (the underlying numeric inputs that produced the bucket are not part of
// ToolingUtilization). Lexicographic on non-empty inputs is wrong — the
// closed-enum order is the semantic order. We use rank tables for known
// label spaces and fall back to lexicographic only for unrecognized values.
//
// Recognized label spaces (rank ascending):
//   - count buckets:      none < 1-3 < 4-10 < 11-25 < 26-50 < 51-100 < 100+
//   - token buckets:      none < <1k < 1k-5k < 5k-15k < 15k-50k < 50k+
//   - efficiency buckets: unused < underutilized < moderate < well-utilized
//   - "unknown" sorts to the bottom of every space.
func maxBucketRank(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	ra, ok1 := bucketLabelRank[a]
	rb, ok2 := bucketLabelRank[b]
	if ok1 && ok2 {
		if ra >= rb {
			return a
		}
		return b
	}
	// Lexicographic fallback keeps the merge deterministic even when an
	// unrecognized label arrives (defensive against shape drift).
	if a >= b {
		return a
	}
	return b
}

// bucketLabelRank assigns an ordinal to every bucket label produced by
// internal/analyzer/tooling_buckets.go so maxBucketRank can hold the
// argmax across two inputs. Labels not present here fall through to
// lexicographic comparison.
var bucketLabelRank = map[string]int{
	// shared low rank for unknown across every bucket family
	"unknown": 0,
	// count buckets
	"none":   1,
	"1-3":    2,
	"4-10":   3,
	"11-25":  4,
	"26-50":  5,
	"51-100": 6,
	"100+":   7,
	// token buckets (share "none" with count buckets)
	"<1k":     2,
	"1k-5k":   3,
	"5k-15k":  4,
	"15k-50k": 5,
	"50k+":    6,
	// efficiency buckets
	"unused":        1,
	"underutilized": 2,
	"moderate":      3,
	"well-utilized": 4,
}

// clampPct clamps a percentage to [0, 100].
func clampPct(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

func mergedMCPUtilizationRatio(out MCPUtilization) int {
	denom := len(out.KnownServerIDs) + out.UnknownServerCount
	numer := len(out.UniqueKnownCalledIDs) + out.UniqueUnknownCalledCount
	if denom <= 0 {
		denom = numer
	}
	if denom <= 0 {
		return 0
	}
	if numer > denom {
		numer = denom
	}
	return clampPct(numer * 100 / denom)
}

func mergedSkillUtilizationRatio(out SkillUtilization) int {
	denom := len(out.KnownExposedIDs) + out.UnknownExposedCount
	numer := len(out.KnownExecutedIDs) + out.UnknownExecutedCount
	if denom <= 0 {
		denom = numer
	}
	if denom <= 0 {
		return 0
	}
	if numer > denom {
		numer = denom
	}
	return clampPct(numer * 100 / denom)
}

// mergeMCPUtilization merges two MCPUtilization values per FR-008.
// See contracts/aggregate-merge.md for the row-by-row contract.
func mergeMCPUtilization(a, b MCPUtilization) MCPUtilization {
	out := MCPUtilization{
		KnownServerIDs:           unionSorted(a.KnownServerIDs, b.KnownServerIDs),
		UnknownServerCount:       a.UnknownServerCount + b.UnknownServerCount,
		ServerCountBucket:        maxBucketRank(a.ServerCountBucket, b.ServerCountBucket),
		ExposedToolCountBucket:   maxBucketRank(a.ExposedToolCountBucket, b.ExposedToolCountBucket),
		ContextTokenBucket:       maxBucketRank(a.ContextTokenBucket, b.ContextTokenBucket),
		ExposureKnown:            a.ExposureKnown || b.ExposureKnown,
		InferenceSource:          maxInferenceSource(a.InferenceSource, b.InferenceSource),
		CallCount:                a.CallCount + b.CallCount,
		KnownCallCount:           a.KnownCallCount + b.KnownCallCount,
		UnknownCallCount:         a.UnknownCallCount + b.UnknownCallCount,
		UniqueKnownCalledIDs:     unionSorted(a.UniqueKnownCalledIDs, b.UniqueKnownCalledIDs),
		UniqueUnknownCalledCount: a.UniqueUnknownCalledCount + b.UniqueUnknownCalledCount,
		ContextEfficiencyBucket:  maxBucketRank(a.ContextEfficiencyBucket, b.ContextEfficiencyBucket),
		WarningBand:              maxWarningBand(a.WarningBand, b.WarningBand),
	}
	// UtilizationRatioPct follows the single-report semantic from
	// computeToolingUtilization: distinct servers called / distinct servers
	// exposed. Known IDs are unioned; unknown IDs are count-only and therefore
	// summed conservatively to avoid storing private names.
	out.UtilizationRatioPct = mergedMCPUtilizationRatio(out)
	return out
}

// mergeSkillUtilization merges two SkillUtilization values per FR-008.
func mergeSkillUtilization(a, b SkillUtilization) SkillUtilization {
	out := SkillUtilization{
		KnownExposedIDs:         unionSorted(a.KnownExposedIDs, b.KnownExposedIDs),
		UnknownExposedCount:     a.UnknownExposedCount + b.UnknownExposedCount,
		ExposedCountBucket:      maxBucketRank(a.ExposedCountBucket, b.ExposedCountBucket),
		ContextTokenBucket:      maxBucketRank(a.ContextTokenBucket, b.ContextTokenBucket),
		ExposureKnown:           a.ExposureKnown || b.ExposureKnown,
		InferenceSource:         maxInferenceSource(a.InferenceSource, b.InferenceSource),
		ExecutedCount:           a.ExecutedCount + b.ExecutedCount,
		KnownExecutedIDs:        unionSorted(a.KnownExecutedIDs, b.KnownExecutedIDs),
		UnknownExecutedCount:    a.UnknownExecutedCount + b.UnknownExecutedCount,
		ContextEfficiencyBucket: maxBucketRank(a.ContextEfficiencyBucket, b.ContextEfficiencyBucket),
		WarningBand:             maxWarningBand(a.WarningBand, b.WarningBand),
	}
	// UtilizationRatioPct follows the single-report semantic from
	// computeToolingUtilization: distinct skills executed / distinct skills
	// exposed. Unknown names remain count-only and are summed conservatively.
	out.UtilizationRatioPct = mergedSkillUtilizationRatio(out)
	return out
}

// mergeWorkflowFingerprints merges []EcosystemFingerprint by id per FR-007.
//   - sources: unionSorted
//   - evidence_count: SUM (C-007)
//   - confidence: maxConfidence (high > medium > low)
//   - active / installed: OR
//   - version_bucket: retain when all inputs agree on a non-empty value;
//     else empty (no "mixed" sentinel introduced — C-001)
//
// Output is sorted ascending by id so deep-equality holds across input
// permutations (commutativity + associativity invariants).
func mergeWorkflowFingerprints(a, b []EcosystemFingerprint) []EcosystemFingerprint {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	// Two-step accumulation: collect all fingerprints into a map keyed by
	// id, recording per-id whether every input fingerprint agreed on a
	// single non-empty version bucket. Sentinel value "" tracks the
	// "no value seen yet" state separately from "saw a disagreement".
	type acc struct {
		fp          EcosystemFingerprint
		bucketState string // "init", "agree", "disagree"
		bucketValue string
	}
	byID := map[string]*acc{}
	absorb := func(fp EcosystemFingerprint) {
		if fp.ID == "" {
			// Defensive: skip malformed fingerprints rather than collapsing
			// them all under the empty-string key. Structural tests in
			// internal/analyzer/sdd/ already enforce non-empty IDs at the
			// per-report layer.
			return
		}
		cur, ok := byID[fp.ID]
		if !ok {
			cur = &acc{
				fp: EcosystemFingerprint{
					ID:            fp.ID,
					Confidence:    fp.Confidence,
					Sources:       append([]string(nil), fp.Sources...),
					EvidenceCount: fp.EvidenceCount,
					Active:        fp.Active,
					Installed:     fp.Installed,
				},
				bucketState: "init",
			}
			if fp.VersionBucket != "" {
				cur.bucketState = "agree"
				cur.bucketValue = fp.VersionBucket
			}
			byID[fp.ID] = cur
			return
		}
		cur.fp.Sources = unionSorted(cur.fp.Sources, fp.Sources)
		cur.fp.EvidenceCount += fp.EvidenceCount
		cur.fp.Confidence = maxConfidence(cur.fp.Confidence, fp.Confidence)
		cur.fp.Active = cur.fp.Active || fp.Active
		cur.fp.Installed = cur.fp.Installed || fp.Installed
		switch cur.bucketState {
		case "init":
			if fp.VersionBucket != "" {
				cur.bucketState = "agree"
				cur.bucketValue = fp.VersionBucket
			}
		case "agree":
			if fp.VersionBucket == "" || fp.VersionBucket != cur.bucketValue {
				cur.bucketState = "disagree"
				cur.bucketValue = ""
			}
		}
	}
	for _, fp := range a {
		absorb(fp)
	}
	for _, fp := range b {
		absorb(fp)
	}
	// Normalize empty Sources to nil for stable JSON output (omitempty).
	out := make([]EcosystemFingerprint, 0, len(byID))
	for _, cur := range byID {
		if cur.bucketState == "agree" {
			cur.fp.VersionBucket = cur.bucketValue
		} else {
			cur.fp.VersionBucket = ""
		}
		if len(cur.fp.Sources) == 0 {
			cur.fp.Sources = nil
		}
		out = append(out, cur.fp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
