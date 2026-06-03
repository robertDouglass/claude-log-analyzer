package analyzer

import (
	"sort"
	"strings"
	"time"
)

const baselineReceiptSchemaVersion = "baseline_receipt.v1"
const savingsValidationSchemaVersion = "savings_validation.v1"

type BaselineOptions struct {
	WindowStart     time.Time
	WindowEnd       time.Time
	InputSizeBytes  int
	ReducerReceipts []ReducerReceipt
}

func AttachBaselineReceipt(report *Report, opts BaselineOptions) {
	report.BaselineReceipt = BuildBaselineReceipt(*report, opts)
	normalizeReportCollections(report)
}

func BuildBaselineReceipt(report Report, opts BaselineOptions) BaselineReceipt {
	receipt := BaselineReceipt{
		SchemaVersion:         baselineReceiptSchemaVersion,
		ReportVersion:         report.Version,
		Window:                baselineWindow(report, opts),
		SourceMix:             baselineSourceMix(report),
		Metrics:               baselineMetrics(report),
		RecommendationIDs:     recommendationIDs(report.Recommendation),
		RecommendationClasses: recommendationClasses(report.Recommendation),
		ToolState:             boundedToolState(report),
		ReducerReceipts:       normalizeReducerReceipts(opts.ReducerReceipts),
	}
	return receipt
}

func CompareSavingsValidation(baseline BaselineReceipt, current BaselineReceipt) SavingsValidation {
	deltas := savingsMetricDeltas(baseline.Metrics, current.Metrics)
	warnings := validationWarnings(baseline, current)
	receiptAgreement := directReceiptAgreement(current, deltas)
	tier := validationTier(baseline, current, deltas, warnings, receiptAgreement)
	return SavingsValidation{
		SchemaVersion:          savingsValidationSchemaVersion,
		EvidenceTier:           tier,
		Summary:                validationSummary(tier),
		MetricDeltas:           deltas,
		Warnings:               warnings,
		DirectReceiptAgreement: receiptAgreement,
		Baseline:               baseline,
		Current:                current,
	}
}

func AttachSavingsValidation(report *Report, baseline BaselineReceipt, opts BaselineOptions) {
	current := BuildBaselineReceipt(*report, opts)
	report.BaselineReceipt = current
	validation := CompareSavingsValidation(baseline, current)
	report.SavingsValidation = &validation
	normalizeReportCollections(report)
}

func baselineWindow(report Report, opts BaselineOptions) BaselineWindow {
	logCount := 1
	sourceCount := 1
	var sizeBuckets []string
	if len(report.SourceReports) > 0 {
		logCount = 0
		sourceCount = len(report.SourceReports)
		seenBuckets := map[string]bool{}
		for _, source := range report.SourceReports {
			logCount += source.LogCount
			for _, ref := range source.LogRefs {
				if ref.SizeBucket != "" && !seenBuckets[ref.SizeBucket] {
					sizeBuckets = append(sizeBuckets, ref.SizeBucket)
					seenBuckets[ref.SizeBucket] = true
				}
			}
		}
		sort.Strings(sizeBuckets)
	}
	if len(sizeBuckets) == 0 {
		sizeBuckets = []string{baselineSizeBucket(opts.InputSizeBytes)}
	}
	window := BaselineWindow{
		DurationBucket: "unknown",
		LogCount:       logCount,
		SourceCount:    sourceCount,
		SizeBuckets:    sizeBuckets,
	}
	if !opts.WindowStart.IsZero() {
		window.Start = opts.WindowStart.UTC().Format(time.RFC3339)
	}
	if !opts.WindowEnd.IsZero() {
		window.End = opts.WindowEnd.UTC().Format(time.RFC3339)
	}
	if !opts.WindowStart.IsZero() && !opts.WindowEnd.IsZero() && opts.WindowEnd.After(opts.WindowStart) {
		window.DurationBucket = durationBucket(opts.WindowEnd.Sub(opts.WindowStart))
	}
	return window
}

func baselineSourceMix(report Report) []SourceMixEntry {
	if len(report.SourceReports) > 0 {
		out := make([]SourceMixEntry, 0, len(report.SourceReports))
		for _, source := range report.SourceReports {
			if source.SourceID == "" {
				continue
			}
			out = append(out, SourceMixEntry{SourceID: source.SourceID, LogCount: source.LogCount})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
		return out
	}
	sourceID := report.Ecosystem.Client
	if sourceID == "" {
		sourceID = "unknown"
	}
	return []SourceMixEntry{{SourceID: sourceID, LogCount: 1}}
}

func baselineMetrics(report Report) BaselineMetrics {
	turns := report.Metrics.Turns
	if turns <= 0 {
		turns = 1
	}
	sessions := report.Metrics.SessionCount
	if sessions <= 0 {
		sessions = 1
	}
	uncachedPlusOutput := report.AnalysisSignals.InputTokens + report.AnalysisSignals.CacheCreationTokens + report.AnalysisSignals.OutputTokens
	return BaselineMetrics{
		Turns:                             report.Metrics.Turns,
		Sessions:                          sessions,
		EstimatedTokens:                   report.Metrics.EstimatedTokens,
		UncachedPlusOutputTokens:          uncachedPlusOutput,
		ToolOutputTokens:                  report.Metrics.ToolOutputTokens,
		CacheCreationTokens:               report.AnalysisSignals.CacheCreationTokens,
		RetryDepthMax:                     report.Metrics.RetryDepthMax,
		FailedCommands:                    report.Metrics.FailedCommands,
		Rereads:                           report.Metrics.Rereads,
		ContextGrowthEvents:               report.Metrics.ContextGrowthEvents,
		ToolCallCount:                     report.AnalysisSignals.ToolCallCount,
		ToolResultCount:                   report.AnalysisSignals.ToolResultCount,
		ToolOutputTokensPerTurnPermille:   perTurnPermille(report.Metrics.ToolOutputTokens, turns),
		UncachedPlusOutputPerTurnPermille: perTurnPermille(uncachedPlusOutput, turns),
		CacheCreationPerTurnPermille:      perTurnPermille(report.AnalysisSignals.CacheCreationTokens, turns),
		FailedCommandsPerTurnPermille:     perTurnPermille(report.Metrics.FailedCommands, turns),
		RereadsPerTurnPermille:            perTurnPermille(report.Metrics.Rereads, turns),
		ContextGrowthPerTurnPermille:      perTurnPermille(report.Metrics.ContextGrowthEvents, turns),
	}
}

func recommendationIDs(set *RecommendationSet) []string {
	if set == nil {
		return []string{}
	}
	var ids []string
	if set.Primary != nil && set.Primary.RecommendationID != "" {
		ids = append(ids, set.Primary.RecommendationID)
	}
	if set.Secondary != nil && set.Secondary.RecommendationID != "" {
		ids = append(ids, set.Secondary.RecommendationID)
	}
	sort.Strings(ids)
	return ids
}

func recommendationClasses(set *RecommendationSet) []RecommendationClass {
	if set == nil {
		return []RecommendationClass{}
	}
	seen := map[RecommendationClass]bool{}
	add := func(rec *TokenSavingRecommendation) {
		if rec == nil || rec.RecommendationID == "" {
			return
		}
		parts := strings.Split(rec.RecommendationID, ".")
		if len(parts) >= 3 {
			class := RecommendationClass(parts[1])
			if class != "" {
				seen[class] = true
			}
		}
	}
	add(set.Primary)
	add(set.Secondary)
	out := make([]RecommendationClass, 0, len(seen))
	for class := range seen {
		out = append(out, class)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func boundedToolState(report Report) BoundedToolState {
	state := BoundedToolState{
		KnownReducerIDs:              knownReducerIDs(report.Recommendation),
		KnownRecommendationTargetIDs: knownRecommendationTargetIDs(report.Recommendation),
		KnownPluginIDs:               sortedStrings(report.Ecosystem.KnownPlugins),
		KnownMCPServerIDs:            sortedStrings(report.Ecosystem.ToolingUtilization.MCP.KnownServerIDs),
		KnownSkillIDs:                sortedStrings(report.Ecosystem.KnownSkills),
		UnknownMCPServerCount:        report.Ecosystem.UnknownMCPServerCount,
		UnknownSkillCount:            report.Ecosystem.UnknownSkillCount,
		UnknownPluginCount:           report.Ecosystem.UnknownPluginCount,
	}
	if report.Recommendation != nil {
		state.UnknownToolIDCount = report.Recommendation.UnknownIDCount
	}
	return state
}

func knownReducerIDs(set *RecommendationSet) []ToolID {
	if set == nil {
		return []ToolID{}
	}
	seen := map[ToolID]bool{}
	add := func(rec *TokenSavingRecommendation) {
		if rec == nil || rec.PrimaryToolID == "" {
			return
		}
		for _, class := range []RecommendationClass{ClassMCPOutputReducer, ClassShellOutputReducer, ClassRetrieval, ClassRereadGuard, ClassOutputVerbosity} {
			if strings.Contains(rec.RecommendationID, "."+string(class)+".") {
				seen[rec.PrimaryToolID] = true
			}
		}
	}
	add(set.Primary)
	add(set.Secondary)
	return sortedToolIDs(seen)
}

func knownRecommendationTargetIDs(set *RecommendationSet) []ToolID {
	if set == nil {
		return []ToolID{}
	}
	seen := map[ToolID]bool{}
	add := func(rec *TokenSavingRecommendation) {
		if rec != nil && rec.PrimaryToolID != "" {
			seen[rec.PrimaryToolID] = true
		}
	}
	add(set.Primary)
	add(set.Secondary)
	return sortedToolIDs(seen)
}

func savingsMetricDeltas(base, current BaselineMetrics) []SavingsMetricDelta {
	return []SavingsMetricDelta{
		metricDelta("uncached_plus_output_per_turn", "Uncached+output tokens per turn", base.UncachedPlusOutputPerTurnPermille, current.UncachedPlusOutputPerTurnPermille),
		metricDelta("tool_output_tokens_per_turn", "Tool-output tokens per turn", base.ToolOutputTokensPerTurnPermille, current.ToolOutputTokensPerTurnPermille),
		metricDelta("cache_creation_per_turn", "Cache creation tokens per turn", base.CacheCreationPerTurnPermille, current.CacheCreationPerTurnPermille),
		metricDelta("failed_commands_per_turn", "Failed commands per turn", base.FailedCommandsPerTurnPermille, current.FailedCommandsPerTurnPermille),
		metricDelta("rereads_per_turn", "Repeated reads per turn", base.RereadsPerTurnPermille, current.RereadsPerTurnPermille),
		metricDelta("context_growth_per_turn", "Context growth events per turn", base.ContextGrowthPerTurnPermille, current.ContextGrowthPerTurnPermille),
	}
}

func metricDelta(id, label string, base, current int) SavingsMetricDelta {
	delta := current - base
	pct := 0
	if base != 0 {
		pct = int(float64(delta) / float64(base) * 100)
	}
	return SavingsMetricDelta{
		ID:               id,
		Label:            label,
		BaselinePermille: base,
		CurrentPermille:  current,
		DeltaPermille:    delta,
		DeltaPct:         pct,
		Improved:         current < base,
	}
}

func validationWarnings(base, current BaselineReceipt) []string {
	var warnings []string
	if base.Window.LogCount < 3 || current.Window.LogCount < 3 {
		warnings = append(warnings, "small_log_sample")
	}
	if sourceMixSignature(base.SourceMix) != sourceMixSignature(current.SourceMix) {
		warnings = append(warnings, "source_mix_changed")
	}
	if base.Metrics.Turns > 0 && current.Metrics.Turns > 0 {
		ratio := float64(current.Metrics.Turns) / float64(base.Metrics.Turns)
		if ratio < 0.5 || ratio > 2.0 {
			warnings = append(warnings, "workload_volume_changed")
		}
	}
	if base.Metrics.UncachedPlusOutputPerTurnPermille == 0 || current.Metrics.UncachedPlusOutputPerTurnPermille == 0 {
		warnings = append(warnings, "missing_native_token_counters")
	}
	return warnings
}

func validationTier(base, current BaselineReceipt, deltas []SavingsMetricDelta, warnings []string, receiptAgreement bool) string {
	if current.ControlledBenchmark != nil &&
		current.ControlledBenchmark.RepeatedPairs >= 3 &&
		current.ControlledBenchmark.SamePrompt &&
		current.ControlledBenchmark.SameCommit &&
		current.ControlledBenchmark.SameQualityGate &&
		current.ControlledBenchmark.BothSidesPassing {
		return "proven"
	}
	if receiptAgreement {
		return "verified"
	}
	improved := 0
	for _, delta := range deltas {
		if delta.Improved {
			improved++
		}
	}
	if improved >= 2 && !hasWarning(warnings, "workload_volume_changed") && !hasWarning(warnings, "source_mix_changed") {
		return "observed"
	}
	_ = base
	return "inconclusive"
}

func validationSummary(tier string) string {
	switch tier {
	case "proven":
		return "Repeated controlled A/B benchmark evidence supports a causal savings claim for this workflow."
	case "verified":
		return "Direct reducer receipts and independent normalized log counters both show savings movement."
	case "observed":
		return "Normalized log metrics improved, but the workload was not controlled."
	default:
		return "Workload, source mix, sample size, or counter quality changed too much for a savings claim."
	}
}

func directReceiptAgreement(current BaselineReceipt, deltas []SavingsMetricDelta) bool {
	if len(current.ReducerReceipts) == 0 {
		return false
	}
	var receiptSavings bool
	for _, receipt := range current.ReducerReceipts {
		if receipt.TokensSaved > 0 && receipt.Calls > 0 {
			receiptSavings = true
			break
		}
	}
	if !receiptSavings {
		return false
	}
	for _, delta := range deltas {
		if (delta.ID == "uncached_plus_output_per_turn" || delta.ID == "tool_output_tokens_per_turn") && delta.Improved {
			return true
		}
	}
	return false
}

func normalizeReducerReceipts(receipts []ReducerReceipt) []ReducerReceipt {
	if len(receipts) == 0 {
		return nil
	}
	out := make([]ReducerReceipt, 0, len(receipts))
	for _, receipt := range receipts {
		if receipt.ToolID == "" || receipt.Calls < 0 || receipt.TokensSaved < 0 {
			continue
		}
		if receipt.ReceiptSource == "" {
			receipt.ReceiptSource = "local_receipt"
		}
		if receipt.ReductionPct < 0 {
			receipt.ReductionPct = 0
		}
		if receipt.ReductionPct > 100 {
			receipt.ReductionPct = 100
		}
		out = append(out, receipt)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ToolID != out[j].ToolID {
			return out[i].ToolID < out[j].ToolID
		}
		return out[i].ReceiptSource < out[j].ReceiptSource
	})
	return out
}

func sourceMixSignature(mix []SourceMixEntry) string {
	parts := make([]string, 0, len(mix))
	for _, entry := range mix {
		parts = append(parts, entry.SourceID)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sortedToolIDs(seen map[ToolID]bool) []ToolID {
	out := make([]ToolID, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func perTurnPermille(value, turns int) int {
	if turns <= 0 {
		return 0
	}
	return int(float64(value) / float64(turns) * 1000)
}

func durationBucket(duration time.Duration) string {
	switch {
	case duration <= 0:
		return "unknown"
	case duration <= time.Hour:
		return "<1h"
	case duration <= 24*time.Hour:
		return "1h-1d"
	case duration <= 7*24*time.Hour:
		return "1d-7d"
	case duration <= 30*24*time.Hour:
		return "7d-30d"
	default:
		return "30d+"
	}
}

func baselineSizeBucket(bytes int) string {
	switch {
	case bytes <= 0:
		return "unknown"
	case bytes < 10*1024:
		return "<10 KB"
	case bytes < 100*1024:
		return "10-100 KB"
	case bytes < 1024*1024:
		return "100 KB-1 MB"
	case bytes < 5*1024*1024:
		return "1-5 MB"
	default:
		return ">5 MB"
	}
}

func hasWarning(warnings []string, target string) bool {
	for _, warning := range warnings {
		if warning == target {
			return true
		}
	}
	return false
}
