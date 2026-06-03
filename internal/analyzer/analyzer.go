package analyzer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const Version = "0.1.0"
const maxFlattenJSONStringBytes = 4096

var fileReadRE = regexp.MustCompile(`(?i)\b(?:cat|sed|nl|bat|less|head|tail)\s+(?:-[^\s]+\s+)*([A-Za-z0-9_./@~+-]+\.[A-Za-z0-9_+-]+)`)

type parsedLine struct {
	Text      string
	IsTool    bool
	IsError   bool
	Command   string
	ToolName  string
	TurnIndex int
}

func Analyze(jobID string, input []byte) (Report, error) {
	return AnalyzeForSource(jobID, "unknown", input)
}

func AnalyzeForSource(jobID string, source string, input []byte) (Report, error) {
	if len(bytes.TrimSpace(input)) == 0 {
		return Report{}, errors.New("empty upload")
	}

	scrubbed, redactions := Scrub(input)
	lines, parserType := parseLines(scrubbed)
	if len(lines) == 0 {
		return Report{}, errors.New("no parseable content")
	}

	events := normalizeEvents(source, scrubbed)
	signals := signalsFromEvents(events, 1)
	metrics, timeline := computeMetrics(lines)
	applySignalsToMetrics(&metrics, signals)
	ecosystem := DetectEcosystem(scrubbed, lines)
	ecosystem.ToolingUtilization = computeToolingUtilization(scrubbed, lines, metrics)
	findings := buildFindings(metrics, lines, ecosystem)
	findings = appendSignalFindings(findings, signals)
	pluginSavings := estimatePluginSavings(metrics, findings, signals)
	score := scoreFromSavings(pluginSavings)
	waste := wasteRangeFromSavings(pluginSavings)

	report := Report{
		JobID:          jobID,
		Version:        Version,
		Score:          score,
		EstimatedWaste: waste,
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
	report.AggregateEvent = aggregateEvent(report, parserType, len(input))
	AttachRecommendation(&report)
	AttachBaselineReceipt(&report, BaselineOptions{InputSizeBytes: len(input)})
	return report, nil
}

func applySignalsToMetrics(metrics *Metrics, signals AnalysisSignals) {
	if signals.InputTokens+signals.OutputTokens > 0 {
		metrics.EstimatedTokens = max(metrics.EstimatedTokens, signals.InputTokens+signals.OutputTokens)
	}
	if signals.ToolOutputBytes > 0 {
		metrics.ToolOutputTokens = max(metrics.ToolOutputTokens, max(1, signals.ToolOutputBytes/4))
	}
	if signals.ArgsHashedRetryLoops > metrics.RetryDepthMax {
		metrics.RetryDepthMax = signals.ArgsHashedRetryLoops
	}
	metrics.ContextGrowthEvents += signals.CacheInvalidationEvents
}

func parseLines(input []byte) ([]parsedLine, string) {
	scanner := bufio.NewScanner(bytes.NewReader(input))
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	var lines []parsedLine
	parserType := "text"
	turn := 0
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		line := parsedLine{Text: raw}
		var obj map[string]any
		if json.Unmarshal([]byte(raw), &obj) == nil {
			parserType = "jsonl"
			line.Text = flattenJSON(obj)
			if hasJSONTypeContaining(obj, "tool") {
				line.IsTool = true
			}
			if name := firstJSONStringByKey(obj, "name"); name != "" {
				line.ToolName = name
			}
			if tool := firstJSONStringByKey(obj, "tool"); tool != "" {
				line.ToolName = tool
				line.IsTool = true
			}
			if cmd := firstJSONStringByKey(obj, "command"); cmd != "" {
				line.Command = cmd
				line.IsTool = true
			}
			if errText := firstJSONStringByKey(obj, "error"); errText != "" {
				line.IsError = true
			}
			if firstJSONBoolByKey(obj, "is_error") {
				line.IsError = true
			}
		}
		lower := strings.ToLower(line.Text)
		if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "traceback") {
			line.IsError = true
		}
		if line.Command == "" {
			line.Command = extractCommand(line.Text)
		}
		if line.IsTool || line.Command != "" {
			line.IsTool = true
		}
		turn++
		line.TurnIndex = turn
		lines = append(lines, line)
	}
	return lines, parserType
}

func flattenJSON(obj map[string]any) string {
	var parts []string
	var walk func(any)
	walk = func(v any) {
		switch typed := v.(type) {
		case string:
			if len(typed) > maxFlattenJSONStringBytes {
				typed = typed[:maxFlattenJSONStringBytes] + " [truncated]"
			}
			parts = append(parts, typed)
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(obj)
	return strings.Join(parts, " ")
}

func hasJSONTypeContaining(value any, needle string) bool {
	needle = strings.ToLower(needle)
	var found bool
	var walk func(any)
	walk = func(v any) {
		if found {
			return
		}
		switch typed := v.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for key, item := range typed {
				if key == "type" {
					if text, ok := item.(string); ok && strings.Contains(strings.ToLower(text), needle) {
						found = true
						return
					}
				}
				walk(item)
			}
		}
	}
	walk(value)
	return found
}

func firstJSONStringByKey(value any, target string) string {
	var found string
	var walk func(any)
	walk = func(v any) {
		if found != "" {
			return
		}
		switch typed := v.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for key, item := range typed {
				if key == target {
					if text, ok := item.(string); ok {
						found = text
						return
					}
				}
				walk(item)
			}
		}
	}
	walk(value)
	return found
}

func firstJSONBoolByKey(value any, target string) bool {
	var found bool
	var walk func(any)
	walk = func(v any) {
		if found {
			return
		}
		switch typed := v.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for key, item := range typed {
				if key == target {
					if boolean, ok := item.(bool); ok && boolean {
						found = true
						return
					}
				}
				walk(item)
			}
		}
	}
	walk(value)
	return found
}

func extractCommand(text string) string {
	for _, marker := range []string{"command:", "$ ", "bash -lc"} {
		if idx := strings.Index(strings.ToLower(text), marker); idx >= 0 {
			return strings.TrimSpace(text[idx+len(marker):])
		}
	}
	return ""
}

func computeMetrics(lines []parsedLine) (Metrics, []TimelinePoint) {
	var m Metrics
	m.Turns = len(lines)
	fileReads := map[string]int{}
	var timeline []TimelinePoint
	currentTokens := 0
	currentToolTokens := 0
	currentRereads := 0
	currentRetries := 0
	consecutiveErrors := 0

	for _, line := range lines {
		tokens := estimateTokens(line.Text)
		m.EstimatedTokens += tokens
		currentTokens += tokens
		if line.IsTool {
			m.ToolOutputTokens += tokens
			currentToolTokens += tokens
		}
		if line.IsError {
			m.FailedCommands++
			consecutiveErrors++
			if consecutiveErrors > m.RetryDepthMax {
				m.RetryDepthMax = consecutiveErrors
			}
			currentRetries++
		} else if !line.IsTool {
			consecutiveErrors = 0
		}
		for _, match := range fileReadRE.FindAllStringSubmatch(line.Text, -1) {
			if len(match) > 1 {
				key := normalizeEvidencePath(match[1])
				fileReads[key]++
				if fileReads[key] > 1 {
					m.Rereads++
					currentRereads++
				}
			}
		}
		if line.TurnIndex%10 == 0 {
			timeline = append(timeline, TimelinePoint{
				Turn:            line.TurnIndex,
				EstimatedTokens: currentTokens,
				ToolTokens:      currentToolTokens,
				Rereads:         currentRereads,
				Retries:         currentRetries,
			})
		}
	}
	if len(lines)%10 != 0 {
		timeline = append(timeline, TimelinePoint{
			Turn:            len(lines),
			EstimatedTokens: currentTokens,
			ToolTokens:      currentToolTokens,
			Rereads:         currentRereads,
			Retries:         currentRetries,
		})
	}
	for i := 1; i < len(timeline); i++ {
		if timeline[i].EstimatedTokens-timeline[i-1].EstimatedTokens > 6000 {
			m.ContextGrowthEvents++
		}
	}
	return m, timeline
}

func buildFindings(m Metrics, lines []parsedLine, eco Ecosystem) []Finding {
	var findings []Finding
	if m.Rereads >= 3 {
		findings = append(findings, Finding{
			ID:          "repeated_file_reads",
			Title:       "Excessive repeated file reads",
			FailureMode: FailureRepeatedNavigation,
			Severity:    severity(m.Rereads, 3, 10),
			CostImpact:  "medium-high",
			Evidence: FindingEvidence{
				Count:    m.Rereads,
				TopFiles: topRereadFiles(lines),
			},
			Recommendation: "Prefer targeted searches and summarize file state before rereading the same files.",
			Deterministic:  true,
		})
	}
	if m.ToolOutputTokens > 0 && m.EstimatedTokens > 0 {
		share := int(float64(m.ToolOutputTokens) / float64(m.EstimatedTokens) * 100)
		if share >= 35 {
			findings = append(findings, Finding{
				ID:          "tool_output_bloat",
				Title:       "Large shell/tool output overhead",
				FailureMode: FailureToolOutputFlooding,
				Severity:    severity(share, 35, 55),
				CostImpact:  "high",
				Evidence: FindingEvidence{
					TokenShare: share,
				},
				Recommendation: "Cap command output and use narrower queries before pasting long terminal output into context.",
				Deterministic:  true,
			})
		}
	}
	if m.RetryDepthMax >= 3 {
		findings = append(findings, Finding{
			ID:          "retry_loop",
			Title:       "Retry-loop behavior",
			FailureMode: FailureCrossCutting,
			Severity:    severity(m.RetryDepthMax, 3, 6),
			CostImpact:  "medium",
			Evidence: FindingEvidence{
				Count: m.RetryDepthMax,
			},
			Recommendation: "Stop after repeated failures, inspect the invariant, and restart with a smaller debugging scope.",
			Deterministic:  true,
		})
	}
	if m.ContextGrowthEvents >= 2 {
		findings = append(findings, Finding{
			ID:          "context_growth_spikes",
			Title:       "Context growth spikes",
			FailureMode: FailureToolOutputFlooding,
			Severity:    severity(m.ContextGrowthEvents, 2, 5),
			CostImpact:  "medium",
			Evidence: FindingEvidence{
				Count:       m.ContextGrowthEvents,
				Description: fmt.Sprintf("%d timeline windows exceeded the growth threshold", m.ContextGrowthEvents),
			},
			Recommendation: "Compact after task pivots and avoid combining architecture, debugging, and implementation in one long session.",
			Deterministic:  true,
		})
	}

	// MCP/skill bloat findings driven by the WP03 classifier band. We emit a
	// finding only for the "high" and "severe" bands — "watch" stays in the
	// dashboard metric, and "normal"/"unknown" emit nothing.
	tu := eco.ToolingUtilization
	appendBand := func(id, title, band, sev, rec string) {
		findings = append(findings, Finding{
			ID:             id,
			Title:          title,
			FailureMode:    FailureCrossCutting,
			Severity:       sev,
			CostImpact:     "medium-high",
			Evidence:       FindingEvidence{Description: "Bloat band: " + band},
			Recommendation: rec,
			Deterministic:  true,
		})
	}
	switch tu.MCP.WarningBand {
	case WarningBandSevere:
		appendBand("mcp_bloat_severe", "MCP tool surface severely underutilized", WarningBandSevere, "high",
			"Disable unused MCP servers by default and lazy-load heavy MCP servers only when needed.")
	case WarningBandHigh:
		appendBand("mcp_bloat_high", "MCP tool surface underutilized", WarningBandHigh, "medium",
			"Scope project-specific MCPs to project config instead of global config; prefer narrower MCP servers over all-tools-enabled setups.")
	}
	switch tu.Skill.WarningBand {
	case WarningBandSevere:
		appendBand("skill_bloat_severe", "Skill surface severely underutilized", WarningBandSevere, "high",
			"Move rarely used instructions out of always-loaded skill context; keep only high-signal skills in the default agent context.")
	case WarningBandHigh:
		appendBand("skill_bloat_high", "Skill surface underutilized", WarningBandHigh, "medium",
			"Split general skills from project-specific skills.")
	}

	return findings
}

func immediateFixes(findings []Finding) []string {
	if len(findings) == 0 {
		return []string{"Keep sessions scoped and compact before major task pivots."}
	}
	fixes := make([]string, 0, len(findings))
	for _, finding := range findings {
		fixes = append(fixes, finding.Recommendation)
	}
	return fixes
}

func topRereadFiles(lines []parsedLine) []string {
	counts := map[string]int{}
	for _, line := range lines {
		for _, match := range fileReadRE.FindAllStringSubmatch(line.Text, -1) {
			if len(match) > 1 {
				counts[normalizeEvidencePath(match[1])]++
			}
		}
	}
	type pair struct {
		file  string
		count int
	}
	var pairs []pair
	for file, count := range counts {
		if count > 1 {
			pairs = append(pairs, pair{file: file, count: count})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].count > pairs[j].count })
	var out []string
	for i, p := range pairs {
		if i >= 5 {
			break
		}
		out = append(out, p.file)
	}
	return out
}

func normalizeEvidencePath(path string) string {
	path = strings.Trim(path, `"'`)
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return path
}

func aggregateEvent(report Report, parserType string, inputSize int) AggregateSafeEvent {
	findings := map[string]string{}
	for _, finding := range report.Findings {
		findings[finding.ID] = finding.Severity
	}
	return AggregateSafeEvent{
		Event:           "analysis_completed",
		ParserType:      parserType,
		InputSizeBucket: bucket(inputSize, []int{1024, 1024 * 1024, 10 * 1024 * 1024, 50 * 1024 * 1024}),
		TurnBucket:      bucket(report.Metrics.Turns, []int{10, 50, 100, 200}),
		ScoreBucket:     bucket(report.Score, []int{20, 40, 60, 80}),
		WasteBucket:     bucket(report.EstimatedWaste.High, []int{10, 20, 40, 60}),
		Findings:        findings,
		Redactions:      report.Redactions,
		Ecosystem:       report.Ecosystem,
	}
}

func normalizeReportCollections(report *Report) {
	if report.Findings == nil {
		report.Findings = []Finding{}
	}
	if report.Timeline == nil {
		report.Timeline = []TimelinePoint{}
	}
	if report.ImmediateFixes == nil {
		report.ImmediateFixes = []string{}
	}
	if report.Redactions == nil {
		report.Redactions = map[string]int{}
	}
	if report.AnalysisSignals.SampleWarnings == nil {
		report.AnalysisSignals.SampleWarnings = []string{}
	}
	if report.PluginSavings.FindingEstimates == nil {
		report.PluginSavings.FindingEstimates = []FindingSavingsEstimate{}
	}
	if report.BaselineReceipt.SourceMix == nil {
		report.BaselineReceipt.SourceMix = []SourceMixEntry{}
	}
	if report.BaselineReceipt.Window.SizeBuckets == nil {
		report.BaselineReceipt.Window.SizeBuckets = []string{}
	}
	if report.BaselineReceipt.RecommendationIDs == nil {
		report.BaselineReceipt.RecommendationIDs = []string{}
	}
	if report.BaselineReceipt.RecommendationClasses == nil {
		report.BaselineReceipt.RecommendationClasses = []RecommendationClass{}
	}
	if report.BaselineReceipt.ToolState.KnownReducerIDs == nil {
		report.BaselineReceipt.ToolState.KnownReducerIDs = []ToolID{}
	}
	if report.BaselineReceipt.ToolState.KnownRecommendationTargetIDs == nil {
		report.BaselineReceipt.ToolState.KnownRecommendationTargetIDs = []ToolID{}
	}
	if report.BaselineReceipt.ToolState.KnownPluginIDs == nil {
		report.BaselineReceipt.ToolState.KnownPluginIDs = []string{}
	}
	if report.BaselineReceipt.ToolState.KnownMCPServerIDs == nil {
		report.BaselineReceipt.ToolState.KnownMCPServerIDs = []string{}
	}
	if report.BaselineReceipt.ToolState.KnownSkillIDs == nil {
		report.BaselineReceipt.ToolState.KnownSkillIDs = []string{}
	}
	if report.SavingsValidation != nil {
		if report.SavingsValidation.MetricDeltas == nil {
			report.SavingsValidation.MetricDeltas = []SavingsMetricDelta{}
		}
		if report.SavingsValidation.Warnings == nil {
			report.SavingsValidation.Warnings = []string{}
		}
	}
	for index := range report.SourceReports {
		if report.SourceReports[index].Findings == nil {
			report.SourceReports[index].Findings = []Finding{}
		}
		if report.SourceReports[index].Timeline == nil {
			report.SourceReports[index].Timeline = []TimelinePoint{}
		}
		if report.SourceReports[index].ImmediateFixes == nil {
			report.SourceReports[index].ImmediateFixes = []string{}
		}
		if report.SourceReports[index].AnalysisSignals.SampleWarnings == nil {
			report.SourceReports[index].AnalysisSignals.SampleWarnings = []string{}
		}
		if report.SourceReports[index].PluginSavings.FindingEstimates == nil {
			report.SourceReports[index].PluginSavings.FindingEstimates = []FindingSavingsEstimate{}
		}
	}
	normalizeEcosystemCollections(&report.Ecosystem)
}

func normalizeEcosystemCollections(ecosystem *Ecosystem) {
	if ecosystem.CodingAgents == nil {
		ecosystem.CodingAgents = []string{}
	}
	if ecosystem.WorkflowFrameworks == nil {
		ecosystem.WorkflowFrameworks = []string{}
	}
	if ecosystem.MCPServersKnown == nil {
		ecosystem.MCPServersKnown = []string{}
	}
	if ecosystem.KnownSkills == nil {
		ecosystem.KnownSkills = []string{}
	}
	if ecosystem.KnownPlugins == nil {
		ecosystem.KnownPlugins = []string{}
	}
	if ecosystem.PackageManagers == nil {
		ecosystem.PackageManagers = []string{}
	}
	if ecosystem.ToolingUtilization.MCP.KnownServerIDs == nil {
		ecosystem.ToolingUtilization.MCP.KnownServerIDs = []string{}
	}
	if ecosystem.ToolingUtilization.MCP.UniqueKnownCalledIDs == nil {
		ecosystem.ToolingUtilization.MCP.UniqueKnownCalledIDs = []string{}
	}
	if ecosystem.ToolingUtilization.Skill.KnownExposedIDs == nil {
		ecosystem.ToolingUtilization.Skill.KnownExposedIDs = []string{}
	}
	if ecosystem.ToolingUtilization.Skill.KnownExecutedIDs == nil {
		ecosystem.ToolingUtilization.Skill.KnownExecutedIDs = []string{}
	}
}

func estimateTokens(text string) int {
	return max(1, len(text)/4)
}

func severity(value, medium, high int) string {
	if value >= high {
		return "high"
	}
	if value >= medium {
		return "medium"
	}
	return "low"
}

func bucket(value int, thresholds []int) string {
	prev := 0
	for _, threshold := range thresholds {
		if value < threshold {
			return fmt.Sprintf("%d_%d", prev, threshold)
		}
		prev = threshold
	}
	return fmt.Sprintf("%d_plus", prev)
}

func sumRedactions(redactions map[string]int) int {
	total := 0
	for _, count := range redactions {
		total += count
	}
	return total
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func ReadAllLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	limited := io.LimitReader(r, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("upload exceeds %d bytes", maxBytes)
	}
	return data, nil
}
