package analyzer

type Report struct {
	JobID             string             `json:"job_id"`
	Version           string             `json:"version"`
	Score             int                `json:"score"`
	EstimatedWaste    WasteRange         `json:"estimated_waste_pct"`
	Metrics           Metrics            `json:"metrics"`
	Findings          []Finding          `json:"findings"`
	Ecosystem         Ecosystem          `json:"ecosystem"`
	Redactions        map[string]int     `json:"redactions"`
	SecurityReceipt   SecurityReceipt    `json:"security_receipt"`
	Timeline          []TimelinePoint    `json:"timeline"`
	AnalysisSignals   AnalysisSignals    `json:"analysis_signals"`
	PluginSavings     SavingsEstimate    `json:"plugin_savings"`
	ImmediateFixes    []string           `json:"immediate_fixes"`
	SourceReports     []SourceReport     `json:"source_reports,omitempty"`
	AggregateEvent    AggregateSafeEvent `json:"aggregate_event"`
	Recommendation    *RecommendationSet `json:"recommendation,omitempty"`
	BaselineReceipt   BaselineReceipt    `json:"baseline_receipt"`
	SavingsValidation *SavingsValidation `json:"savings_validation,omitempty"`
}

type WasteRange struct {
	Low  int `json:"low"`
	High int `json:"high"`
}

type SavingsEstimate struct {
	PotentialTokensLow  int                      `json:"potential_tokens_low"`
	PotentialTokensHigh int                      `json:"potential_tokens_high"`
	PotentialPctLow     int                      `json:"potential_pct_low"`
	PotentialPctHigh    int                      `json:"potential_pct_high"`
	FindingEstimates    []FindingSavingsEstimate `json:"finding_estimates"`
}

type FindingSavingsEstimate struct {
	FindingID           string `json:"finding_id"`
	GrossTokens         int    `json:"gross_tokens"`
	CapturePctLow       int    `json:"capture_pct_low"`
	CapturePctHigh      int    `json:"capture_pct_high"`
	PotentialTokensLow  int    `json:"potential_tokens_low"`
	PotentialTokensHigh int    `json:"potential_tokens_high"`
}

type Metrics struct {
	Turns               int `json:"turns"`
	EstimatedTokens     int `json:"estimated_tokens"`
	ToolOutputTokens    int `json:"tool_output_tokens"`
	Rereads             int `json:"rereads"`
	RetryDepthMax       int `json:"retry_depth_max"`
	ContextGrowthEvents int `json:"context_growth_events"`
	FailedCommands      int `json:"failed_commands"`
	SessionCount        int `json:"session_count,omitempty"`
}

type AnalysisSignals struct {
	NormalizedEventCount    int      `json:"normalized_event_count"`
	ToolCallCount           int      `json:"tool_call_count"`
	ToolResultCount         int      `json:"tool_result_count"`
	ArgsHashedRetryLoops    int      `json:"args_hashed_retry_loops"`
	CacheReadTokens         int      `json:"cache_read_tokens"`
	CacheCreationTokens     int      `json:"cache_creation_tokens"`
	InputTokens             int      `json:"input_tokens"`
	OutputTokens            int      `json:"output_tokens"`
	CacheMissRatioPct       int      `json:"cache_miss_ratio_pct"`
	CacheInvalidationEvents int      `json:"cache_invalidation_events"`
	ToolOutputBytes         int      `json:"tool_output_bytes"`
	PatchLinesAdded         int      `json:"patch_lines_added"`
	PatchLinesRemoved       int      `json:"patch_lines_removed"`
	SampleConfidence        string   `json:"sample_confidence"`
	SampleWarnings          []string `json:"sample_warnings"`
}

type Finding struct {
	ID             string             `json:"id"`
	Title          string             `json:"title"`
	FailureMode    DefocusFailureMode `json:"failure_mode,omitempty"`
	Severity       string             `json:"severity"`
	CostImpact     string             `json:"cost_impact"`
	Evidence       FindingEvidence    `json:"evidence"`
	Recommendation string             `json:"recommendation"`
	Deterministic  bool               `json:"deterministic"`
}

type FindingEvidence struct {
	Count       int      `json:"count,omitempty"`
	TokenShare  int      `json:"token_share_pct,omitempty"`
	TopFiles    []string `json:"top_files,omitempty"`
	Description string   `json:"description,omitempty"`
}

type Ecosystem struct {
	Client                string                 `json:"client"`
	CodingAgents          []string               `json:"coding_agents"`
	OperatingSystem       string                 `json:"operating_system"`
	Shell                 string                 `json:"shell"`
	WorkflowFrameworks    []string               `json:"workflow_frameworks"`
	MCPServersKnown       []string               `json:"mcp_servers_known"`
	UnknownMCPServerCount int                    `json:"unknown_mcp_server_count"`
	KnownSkills           []string               `json:"known_skills"`
	UnknownSkillCount     int                    `json:"unknown_skill_count"`
	KnownPlugins          []string               `json:"known_plugins"`
	UnknownPluginCount    int                    `json:"unknown_plugin_count"`
	PackageManagers       []string               `json:"package_managers"`
	VersionControl        string                 `json:"version_control"`
	ToolingUtilization    ToolingUtilization     `json:"tooling_utilization"`
	WorkflowFingerprints  []EcosystemFingerprint `json:"workflow_fingerprints,omitempty"`
}

// EcosystemFingerprint is the report-emitted record for a single
// spec-driven-development tool detected on a transcript. The shape is
// intentionally bounded — no free-text fields, no map values — so the
// privacy invariants in NFR-001 are enforceable structurally.
type EcosystemFingerprint struct {
	ID            string   `json:"id"`
	Confidence    string   `json:"confidence"`
	Sources       []string `json:"sources"`
	EvidenceCount int      `json:"evidence_count"`
	Active        bool     `json:"active,omitempty"`
	Installed     bool     `json:"installed,omitempty"`
	VersionBucket string   `json:"version_bucket,omitempty"`
}

type ToolingUtilization struct {
	MCP   MCPUtilization   `json:"mcp"`
	Skill SkillUtilization `json:"skill"`
}

type MCPUtilization struct {
	KnownServerIDs           []string `json:"known_server_ids"`
	UnknownServerCount       int      `json:"unknown_server_count"`
	ServerCountBucket        string   `json:"server_count_bucket"`
	ExposedToolCountBucket   string   `json:"exposed_tool_count_bucket"`
	ContextTokenBucket       string   `json:"context_token_bucket"`
	ExposureKnown            bool     `json:"exposure_known"`
	InferenceSource          string   `json:"inference_source"`
	CallCount                int      `json:"call_count"`
	KnownCallCount           int      `json:"known_call_count"`
	UnknownCallCount         int      `json:"unknown_call_count"`
	UniqueKnownCalledIDs     []string `json:"unique_known_called_ids"`
	UniqueUnknownCalledCount int      `json:"unique_unknown_called_count"`
	UtilizationRatioPct      int      `json:"utilization_ratio_pct"`
	ContextEfficiencyBucket  string   `json:"context_efficiency_bucket"`
	WarningBand              string   `json:"warning_band"`
}

type SkillUtilization struct {
	KnownExposedIDs         []string `json:"known_exposed_ids"`
	UnknownExposedCount     int      `json:"unknown_exposed_count"`
	ExposedCountBucket      string   `json:"exposed_count_bucket"`
	ContextTokenBucket      string   `json:"context_token_bucket"`
	ExposureKnown           bool     `json:"exposure_known"`
	InferenceSource         string   `json:"inference_source"`
	ExecutedCount           int      `json:"executed_count"`
	KnownExecutedIDs        []string `json:"known_executed_ids"`
	UnknownExecutedCount    int      `json:"unknown_executed_count"`
	UtilizationRatioPct     int      `json:"utilization_ratio_pct"`
	ContextEfficiencyBucket string   `json:"context_efficiency_bucket"`
	WarningBand             string   `json:"warning_band"`
}

type SecurityReceipt struct {
	RawTranscriptSentToLLM bool   `json:"raw_transcript_sent_to_llm"`
	OutboundDuringAnalysis bool   `json:"outbound_during_analysis"`
	SecretsRedacted        int    `json:"secrets_redacted"`
	RawLogTTL              string `json:"raw_log_ttl"`
}

type TimelinePoint struct {
	Turn            int `json:"turn"`
	EstimatedTokens int `json:"estimated_tokens"`
	ToolTokens      int `json:"tool_tokens"`
	Rereads         int `json:"rereads"`
	Retries         int `json:"retries"`
}

type SourceReport struct {
	SourceID        string           `json:"source_id"`
	SourceLabel     string           `json:"source_label"`
	LogCount        int              `json:"log_count"`
	LogRefs         []AnalyzedLogRef `json:"log_refs,omitempty"`
	Score           int              `json:"score"`
	EstimatedWaste  WasteRange       `json:"estimated_waste_pct"`
	Metrics         Metrics          `json:"metrics"`
	Findings        []Finding        `json:"findings"`
	Timeline        []TimelinePoint  `json:"timeline"`
	AnalysisSignals AnalysisSignals  `json:"analysis_signals"`
	PluginSavings   SavingsEstimate  `json:"plugin_savings"`
	ImmediateFixes  []string         `json:"immediate_fixes"`
}

type BaselineReceipt struct {
	SchemaVersion         string                    `json:"schema_version"`
	ReportVersion         string                    `json:"report_version"`
	Window                BaselineWindow            `json:"window"`
	SourceMix             []SourceMixEntry          `json:"source_mix"`
	Metrics               BaselineMetrics           `json:"metrics"`
	RecommendationIDs     []string                  `json:"recommendation_ids"`
	RecommendationClasses []RecommendationClass     `json:"recommendation_classes"`
	ToolState             BoundedToolState          `json:"bounded_tool_state"`
	ReducerReceipts       []ReducerReceipt          `json:"reducer_receipts,omitempty"`
	ControlledBenchmark   *ControlledBenchmarkProof `json:"controlled_benchmark,omitempty"`
}

type BaselineWindow struct {
	Start          string   `json:"start,omitempty"`
	End            string   `json:"end,omitempty"`
	DurationBucket string   `json:"duration_bucket"`
	LogCount       int      `json:"log_count"`
	SourceCount    int      `json:"source_count"`
	SizeBuckets    []string `json:"size_buckets"`
}

type SourceMixEntry struct {
	SourceID string `json:"source_id"`
	LogCount int    `json:"log_count"`
}

type BaselineMetrics struct {
	Turns                             int `json:"turns"`
	Sessions                          int `json:"sessions"`
	EstimatedTokens                   int `json:"estimated_tokens"`
	UncachedPlusOutputTokens          int `json:"uncached_plus_output_tokens"`
	ToolOutputTokens                  int `json:"tool_output_tokens"`
	CacheCreationTokens               int `json:"cache_creation_tokens"`
	RetryDepthMax                     int `json:"retry_depth_max"`
	FailedCommands                    int `json:"failed_commands"`
	Rereads                           int `json:"rereads"`
	ContextGrowthEvents               int `json:"context_growth_events"`
	ToolCallCount                     int `json:"tool_call_count"`
	ToolResultCount                   int `json:"tool_result_count"`
	ToolOutputTokensPerTurnPermille   int `json:"tool_output_tokens_per_turn_permille"`
	UncachedPlusOutputPerTurnPermille int `json:"uncached_plus_output_per_turn_permille"`
	CacheCreationPerTurnPermille      int `json:"cache_creation_per_turn_permille"`
	FailedCommandsPerTurnPermille     int `json:"failed_commands_per_turn_permille"`
	RereadsPerTurnPermille            int `json:"rereads_per_turn_permille"`
	ContextGrowthPerTurnPermille      int `json:"context_growth_per_turn_permille"`
}

type BoundedToolState struct {
	KnownReducerIDs              []ToolID `json:"known_reducer_ids"`
	KnownRecommendationTargetIDs []ToolID `json:"known_recommendation_target_ids"`
	KnownPluginIDs               []string `json:"known_plugin_ids"`
	KnownMCPServerIDs            []string `json:"known_mcp_server_ids"`
	KnownSkillIDs                []string `json:"known_skill_ids"`
	UnknownMCPServerCount        int      `json:"unknown_mcp_server_count"`
	UnknownSkillCount            int      `json:"unknown_skill_count"`
	UnknownPluginCount           int      `json:"unknown_plugin_count"`
	UnknownToolIDCount           int      `json:"unknown_tool_id_count"`
}

type ReducerReceipt struct {
	ToolID         ToolID `json:"tool_id"`
	ReceiptSource  string `json:"receipt_source"`
	Calls          int    `json:"calls"`
	TokensSaved    int    `json:"tokens_saved"`
	ProcessedBytes int    `json:"processed_bytes"`
	ReturnedBytes  int    `json:"returned_bytes"`
	ReductionPct   int    `json:"reduction_pct"`
}

type ControlledBenchmarkProof struct {
	RepeatedPairs    int    `json:"repeated_pairs"`
	SamePrompt       bool   `json:"same_prompt"`
	SameCommit       bool   `json:"same_commit"`
	SameQualityGate  bool   `json:"same_quality_gate"`
	BothSidesPassing bool   `json:"both_sides_passing"`
	BenchmarkSuiteID string `json:"benchmark_suite_id,omitempty"`
}

type SavingsValidation struct {
	SchemaVersion          string               `json:"schema_version"`
	EvidenceTier           string               `json:"evidence_tier"`
	Summary                string               `json:"summary"`
	MetricDeltas           []SavingsMetricDelta `json:"metric_deltas"`
	Warnings               []string             `json:"warnings"`
	DirectReceiptAgreement bool                 `json:"direct_receipt_agreement"`
	Baseline               BaselineReceipt      `json:"baseline"`
	Current                BaselineReceipt      `json:"current"`
}

type SavingsMetricDelta struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	BaselinePermille int    `json:"baseline_permille"`
	CurrentPermille  int    `json:"current_permille"`
	DeltaPermille    int    `json:"delta_permille"`
	DeltaPct         int    `json:"delta_pct"`
	Improved         bool   `json:"improved"`
}

type AnalyzedLogRef struct {
	Label             string `json:"label"`
	LocalRef          string `json:"local_ref"`
	SizeBucket        string `json:"size_bucket,omitempty"`
	ContentHashSHA256 string `json:"content_hash_sha256,omitempty"`
}

type AggregateSafeEvent struct {
	Event           string            `json:"event"`
	ParserType      string            `json:"parser_type"`
	InputSizeBucket string            `json:"input_size_bucket"`
	TurnBucket      string            `json:"turn_bucket"`
	ScoreBucket     string            `json:"score_bucket"`
	WasteBucket     string            `json:"waste_bucket"`
	Findings        map[string]string `json:"findings"`
	Redactions      map[string]int    `json:"redactions"`
	Ecosystem       Ecosystem         `json:"ecosystem"`
}
