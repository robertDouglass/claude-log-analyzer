package analyzer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSavingsValidationObservedTier(t *testing.T) {
	base := baselineForValidation(1000, 800, 100, 10, 10)
	current := baselineForValidation(1000, 600, 70, 5, 10)

	got := CompareSavingsValidation(base, current)
	if got.EvidenceTier != "observed" {
		t.Fatalf("expected observed, got %#v", got)
	}
	if len(got.MetricDeltas) == 0 || !got.MetricDeltas[0].Improved {
		t.Fatalf("expected improved normalized deltas, got %#v", got.MetricDeltas)
	}
}

func TestSavingsValidationVerifiedTierRequiresReceiptAgreement(t *testing.T) {
	base := baselineForValidation(1000, 800, 100, 10, 10)
	current := baselineForValidation(1000, 700, 80, 5, 10)
	current.ReducerReceipts = []ReducerReceipt{{
		ToolID:         "context_mode",
		ReceiptSource:  "context_mode_session_stats",
		Calls:          4,
		TokensSaved:    1200,
		ProcessedBytes: 8000,
		ReturnedBytes:  4000,
		ReductionPct:   50,
	}}

	got := CompareSavingsValidation(base, current)
	if got.EvidenceTier != "verified" || !got.DirectReceiptAgreement {
		t.Fatalf("expected verified receipt agreement, got %#v", got)
	}
}

func TestSavingsValidationProvenTierRequiresControlledBenchmark(t *testing.T) {
	base := baselineForValidation(1000, 800, 100, 10, 10)
	current := baselineForValidation(1000, 900, 120, 11, 10)
	current.ControlledBenchmark = &ControlledBenchmarkProof{
		RepeatedPairs:    3,
		SamePrompt:       true,
		SameCommit:       true,
		SameQualityGate:  true,
		BothSidesPassing: true,
	}

	got := CompareSavingsValidation(base, current)
	if got.EvidenceTier != "proven" {
		t.Fatalf("expected proven, got %#v", got)
	}
}

func TestSavingsValidationInconclusiveOnWorkloadDrift(t *testing.T) {
	base := baselineForValidation(1000, 800, 100, 10, 10)
	current := baselineForValidation(100, 60, 7, 1, 10)

	got := CompareSavingsValidation(base, current)
	if got.EvidenceTier != "inconclusive" {
		t.Fatalf("expected inconclusive, got %#v", got)
	}
	if !hasWarning(got.Warnings, "workload_volume_changed") {
		t.Fatalf("expected workload warning, got %#v", got.Warnings)
	}
}

func TestBaselineReceiptDoesNotLeakPrivateNames(t *testing.T) {
	report := Report{
		Version:         Version,
		Metrics:         Metrics{Turns: 10, EstimatedTokens: 1000, ToolOutputTokens: 200},
		AnalysisSignals: AnalysisSignals{InputTokens: 10, CacheCreationTokens: 20, OutputTokens: 30},
		Ecosystem: Ecosystem{
			Client:                "claude_code",
			UnknownMCPServerCount: 1,
			UnknownSkillCount:     1,
			UnknownPluginCount:    1,
			KnownPlugins:          []string{"context_mode"},
		},
		Recommendation: &RecommendationSet{
			Primary: &TokenSavingRecommendation{
				RecommendationID: "rec.mcp_output_reducer.context_mode.mcp_tool_output_bloat",
				PrimaryToolID:    "context_mode",
			},
		},
	}
	receipt := BuildBaselineReceipt(report, BaselineOptions{})
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	serialized := string(data)
	for _, forbidden := range []string{"customer-private-mcp", "/Users/robert/private", "secret-tool"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("baseline receipt leaked %q: %s", forbidden, serialized)
		}
	}
	if !strings.Contains(serialized, `"unknown_mcp_server_count":1`) {
		t.Fatalf("expected unknown count, got %s", serialized)
	}
}

func baselineForValidation(turns, uncachedPerTurn, toolPerTurn, failedPerTurn, logCount int) BaselineReceipt {
	report := Report{
		Version: Version,
		Metrics: Metrics{
			Turns:            turns,
			EstimatedTokens:  uncachedPerTurn * turns,
			ToolOutputTokens: toolPerTurn * turns,
			FailedCommands:   failedPerTurn * turns,
			Rereads:          turns / 20,
			SessionCount:     logCount,
		},
		AnalysisSignals: AnalysisSignals{
			InputTokens:         uncachedPerTurn * turns / 2,
			CacheCreationTokens: uncachedPerTurn * turns / 4,
			OutputTokens:        uncachedPerTurn * turns / 4,
			ToolCallCount:       turns / 2,
			ToolResultCount:     turns / 2,
			SampleConfidence:    "high",
			SampleWarnings:      []string{},
		},
		Ecosystem: Ecosystem{Client: "claude_code"},
	}
	receipt := BuildBaselineReceipt(report, BaselineOptions{})
	receipt.Window.LogCount = logCount
	receipt.SourceMix = []SourceMixEntry{{SourceID: "claude_code", LogCount: logCount}}
	return receipt
}
