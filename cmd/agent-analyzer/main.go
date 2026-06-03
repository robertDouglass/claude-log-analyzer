package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/priivacy-ai/agent-log-analyzer/internal/analyzer"
	_ "modernc.org/sqlite"
)

const defaultBaseURL = "https://analyzer.spec-kitty.ai"
const freeAutoMinLogBytes = 4 * 1024
const defaultAutoLogLimit = 5
const maxAutoLogLimit = 5
const targetAutoLogBytes = 5 * 1024 * 1024
const targetSelectionPoolLimit = 40
const largestRecentHalfLife = 14 * 24 * time.Hour

var representativeSourceOrder = []string{
	"claude_code",
	"claude_desktop",
	"codex",
	"copilot",
	"opencode",
	"claude_desktop_mcp",
	"cursor",
	"kiro_cli",
	"kiro_ide",
	"antigravity",
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("missing command")
	}
	switch args[0] {
	case "run":
		return runOneShot(args[1:])
	case "analyze":
		return runAnalyze(args[1:])
	case "prove-savings", "follow-up":
		return runProveSavings(args[1:])
	case "full-scan":
		return runFullScan(args[1:])
	case "upload":
		return runUpload(args[1:])
	case "version", "--version", "-v":
		printVersion()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// defaultSupportedLogsFn and recentSupportedLogsFn are package-level
// indirections so tests can shim source discovery without touching the user's
// real home directory or installed agent CLIs.
var defaultSupportedLogsFn = defaultSupportedLogs
var recentSupportedLogsFn = recentSupportedLogs

func runAnalyze(args []string) error {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	out := fs.String("out", "agent-analyzer-report.json", "path to write sanitized report JSON")
	logPath := fs.String("log", "", "explicit supported JSON/JSONL log path")
	sourceID := fs.String("source", "", "source ID for an explicit log path")
	paid := fs.Bool("paid", false, "analyze recent supported agent logs locally and write a sanitized paid aggregate report")
	limit := fs.Int("limit", defaultAutoLogLimit, "maximum target-sized recent logs per supported source to analyze with --paid")
	orderedArgs := reorderAnalyzeArgs(args)
	if err := fs.Parse(orderedArgs); err != nil {
		return err
	}

	positional := fs.Args()
	if *paid {
		if *logPath != "" || *sourceID != "" || len(positional) > 0 {
			return errors.New("agent-analyzer analyze: --paid cannot be combined with --log, --source, or positional log paths")
		}
		return runAnalyzePaid(*out, *limit)
	}
	// FR-002 takes precedence over FR-003 when both a positional and --log
	// are supplied alongside extra positional arguments.
	if len(positional) >= 1 && *logPath != "" {
		return errors.New("agent-analyzer analyze: cannot combine positional log path with --log flag")
	}
	if len(positional) >= 2 {
		return fmt.Errorf("agent-analyzer analyze: unexpected extra argument %q", positional[1])
	}

	path := *logPath
	if path == "" && len(positional) == 1 {
		path = positional[0]
	}
	if path == "" {
		if *sourceID != "" {
			return errors.New("agent-analyzer analyze: --source requires --log or a positional log path")
		}
		candidates, err := defaultSupportedLogsFn()
		if err != nil {
			return err
		}
		return analyzeDiscovered(candidates, *out, "", true)
	}
	source, err := explicitOrInferredSource(*sourceID, path)
	if err != nil {
		return err
	}
	return analyzeSingle(path, *out, true, source)
}

func analyzeSingle(path, out string, printNextSteps bool, sourceID string) error {
	progress := newProgressBar(3)
	progress.Update(0, "reading "+shortDisplay(path))
	candidate := explicitLogCandidate(sourceID, path)
	data, err := candidate.readBytes()
	if err != nil {
		progress.Fail()
		return err
	}
	progress.Update(1, "analyzing "+shortDisplay(path))
	report, err := analyzeBytesForSource("local", sourceID, data)
	if err != nil {
		progress.Fail()
		return err
	}
	report.SourceReports = buildSourceReports([]sourceAnalysisResult{
		{
			Candidate:         candidate,
			Report:            report,
			Bytes:             len(data),
			ContentHashSHA256: contentHashSHA256(data),
		},
	})
	progress.Update(2, "writing sanitized report")
	analyzer.AttachBaselineReceipt(&report, baselineOptionsFromResults(report.SourceReports, []sourceAnalysisResult{{
		Candidate: candidate,
		Report:    report,
		Bytes:     len(data),
	}}, len(data), nil))
	if err := writeReport(out, report); err != nil {
		progress.Fail()
		return err
	}
	progress.Finish("complete")

	fmt.Printf("Analyzed locally: %s\n", path)
	fmt.Printf("Raw bytes read locally: %d\n", len(data))
	fmt.Printf("Secrets redacted before report write: %d\n", report.SecurityReceipt.SecretsRedacted)
	printReportWrite(out, report)
	if printNextSteps {
		printNextStepsFor(out)
	}
	return nil
}

func explicitOrInferredSource(explicit, path string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		if !analyzer.ValidEcosystemID("coding_agent", explicit) {
			return "", fmt.Errorf("agent-analyzer analyze: unknown --source %q", explicit)
		}
		return explicit, nil
	}
	if inferred := inferSourceFromPath(path); inferred != "" {
		return inferred, nil
	}
	return "unknown", nil
}

func inferSourceFromPath(path string) string {
	normalized := strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(normalized, "/local-agent-mode-sessions/") || strings.Contains(normalized, "/claude-code-sessions/"):
		return "claude_desktop"
	case base == "audit.jsonl" && strings.Contains(normalized, "/claude/"):
		return "claude_desktop"
	case strings.Contains(normalized, "/logs/claude/") && acceptClaudeDesktopMCPLog(path, nil):
		return "claude_desktop_mcp"
	case strings.Contains(normalized, "/.codex/") || strings.Contains(base, "codex") || strings.HasPrefix(base, "rollout-"):
		return "codex"
	case strings.Contains(normalized, "/.copilot/") || strings.Contains(normalized, "/github.copilot") || strings.Contains(normalized, "/github/copilot") || strings.Contains(normalized, "/chatsessions/"):
		return "copilot"
	case strings.Contains(normalized, "/.cursor/") || strings.Contains(normalized, "/application support/cursor/") || strings.Contains(normalized, "/cursor/user/"):
		return "cursor"
	case strings.Contains(normalized, "/opencode/"):
		return "opencode"
	case strings.Contains(normalized, "/kiro-log/") || strings.Contains(normalized, "/.kiro/"):
		return "kiro_cli"
	case strings.Contains(normalized, "/application support/kiro/") || strings.Contains(normalized, "/kiro/user/"):
		return "kiro_ide"
	case strings.Contains(normalized, "/antigravity"):
		return "antigravity"
	case strings.Contains(normalized, "/.claude/"):
		return "claude_code"
	default:
		return ""
	}
}

func analyzeBytesForSource(jobID string, sourceID string, data []byte) (analyzer.Report, error) {
	report, err := analyzer.AnalyzeForSource(jobID, sourceID, data)
	if err != nil {
		return analyzer.Report{}, err
	}
	report.SecurityReceipt.RawLogTTL = "not uploaded"
	return report, nil
}

func writeReport(out string, report analyzer.Report) error {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	return nil
}

func reportFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func printReportWrite(out string, report analyzer.Report) {
	label := "Sanitized report"
	if report.AggregateEvent.ParserType == "paid_bundle" {
		label = "Sanitized paid aggregate report"
	} else if report.AggregateEvent.ParserType == "full_scan_bundle" {
		label = "Sanitized full-scan aggregate report"
	}
	fmt.Printf("%s: %s (%d bytes)\n", label, out, reportFileSize(out))
}

func printNextStepsFor(out string) {
	fmt.Println()
	fmt.Printf("Review before upload: jq . %s\n", shellQuote(out))
	fmt.Printf("Upload sanitized report: agent-analyzer upload %s\n", shellQuote(out))
}

type progressBar struct {
	total       int
	width       int
	lastLen     int
	lastDone    int
	lastMessage string
	mode        progressMode
}

type progressMode string

const (
	progressModeBar   progressMode = "bar"
	progressModeLines progressMode = "lines"
	progressModeNone  progressMode = "none"
)

func newProgressBar(total int) *progressBar {
	if total < 1 {
		total = 1
	}
	return &progressBar{total: total, width: 24, lastDone: -1, mode: detectProgressMode()}
}

func detectProgressMode() progressMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AGENT_ANALYZER_PROGRESS"))) {
	case "bar":
		return progressModeBar
	case "line", "lines":
		return progressModeLines
	case "none", "off", "false", "0":
		return progressModeNone
	}

	// Claude Code, Codex, CI logs, and dumb terminals often render carriage
	// return updates as stacked lines. Prefer boring milestone output there.
	if os.Getenv("CODEX_SHELL") != "" ||
		os.Getenv("CODEX_CI") != "" ||
		os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" ||
		os.Getenv("CI") != "" ||
		os.Getenv("TERM") == "dumb" {
		return progressModeLines
	}
	if info, err := os.Stdout.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		return progressModeBar
	}
	return progressModeLines
}

func (bar *progressBar) Update(done int, message string) {
	if bar.mode == progressModeNone {
		return
	}
	if done < 0 {
		done = 0
	}
	if done > bar.total {
		done = bar.total
	}
	if bar.mode == progressModeLines {
		if done == bar.lastDone && message == bar.lastMessage {
			return
		}
		fmt.Printf("[%d/%d] %s\n", done, bar.total, message)
		bar.lastDone = done
		bar.lastMessage = message
		return
	}
	filled := done * bar.width / bar.total
	empty := bar.width - filled
	head := ""
	if done < bar.total && empty > 0 {
		head = ">"
		empty--
	}
	line := fmt.Sprintf("\r[%s%s%s] %d/%d %s",
		strings.Repeat("=", filled),
		head,
		strings.Repeat(" ", empty),
		done,
		bar.total,
		message,
	)
	if bar.lastLen > len(line) {
		line += strings.Repeat(" ", bar.lastLen-len(line))
	}
	fmt.Print(line)
	bar.lastLen = len(line)
}

func (bar *progressBar) Finish(message string) {
	bar.Update(bar.total, message)
	if bar.mode == progressModeBar {
		fmt.Println()
	}
}

func (bar *progressBar) Fail() {
	if bar.mode == progressModeBar && bar.lastLen > 0 {
		fmt.Println()
	}
}

type sourceAnalysisResult struct {
	Candidate         logCandidate
	Report            analyzer.Report
	Bytes             int
	ContentHashSHA256 string
}

type candidateAnalysisResult struct {
	Index     int
	Result    sourceAnalysisResult
	Skipped   bool
	SkipLabel string
	Err       error
}

func reportsFromResults(results []sourceAnalysisResult) []analyzer.Report {
	reports := make([]analyzer.Report, 0, len(results))
	for _, result := range results {
		reports = append(reports, result.Report)
	}
	return reports
}

func buildSourceReports(results []sourceAnalysisResult) []analyzer.SourceReport {
	if len(results) == 0 {
		return nil
	}
	type group struct {
		sourceID    string
		sourceLabel string
		reports     []analyzer.Report
		bytes       int
		logRefs     []analyzer.AnalyzedLogRef
	}
	order := []string{}
	groups := map[string]*group{}
	for _, result := range results {
		key := result.Candidate.SourceID
		if _, ok := groups[key]; !ok {
			order = append(order, key)
			groups[key] = &group{
				sourceID:    result.Candidate.SourceID,
				sourceLabel: result.Candidate.SourceLabel,
			}
		}
		groups[key].reports = append(groups[key].reports, result.Report)
		groups[key].bytes += result.Bytes
		groups[key].logRefs = append(groups[key].logRefs, safeAnalyzedLogRef(result.Candidate, len(groups[key].logRefs)+1, result.Bytes, result.ContentHashSHA256))
	}
	sourceReports := make([]analyzer.SourceReport, 0, len(order))
	for _, key := range order {
		group := groups[key]
		report := group.reports[0]
		if len(group.reports) > 1 {
			merged, err := analyzer.AggregateReportsWithParserType("local-"+group.sourceID, group.reports, group.bytes, "multi_source")
			if err == nil {
				report = merged
			}
		}
		timeline := report.Timeline
		if len(timeline) == 0 {
			timeline = combinedSourceTimeline(group.reports)
		}
		sourceReports = append(sourceReports, analyzer.SourceReport{
			SourceID:        group.sourceID,
			SourceLabel:     group.sourceLabel,
			LogCount:        len(group.reports),
			LogRefs:         group.logRefs,
			Score:           report.Score,
			EstimatedWaste:  report.EstimatedWaste,
			Metrics:         report.Metrics,
			Findings:        report.Findings,
			Timeline:        timeline,
			AnalysisSignals: report.AnalysisSignals,
			PluginSavings:   report.PluginSavings,
			ImmediateFixes:  report.ImmediateFixes,
		})
	}
	return sourceReports
}

func combinedSourceTimeline(reports []analyzer.Report) []analyzer.TimelinePoint {
	var timeline []analyzer.TimelinePoint
	var turnOffset, tokenOffset, toolOffset, rereadOffset, retryOffset int
	for _, report := range reports {
		for _, point := range report.Timeline {
			timeline = append(timeline, analyzer.TimelinePoint{
				Turn:            turnOffset + point.Turn,
				EstimatedTokens: tokenOffset + point.EstimatedTokens,
				ToolTokens:      toolOffset + point.ToolTokens,
				Rereads:         rereadOffset + point.Rereads,
				Retries:         retryOffset + point.Retries,
			})
		}
		turnOffset += report.Metrics.Turns
		tokenOffset += report.Metrics.EstimatedTokens
		toolOffset += report.Metrics.ToolOutputTokens
		rereadOffset += report.Metrics.Rereads
		retryOffset += report.Metrics.FailedCommands
	}
	return timeline
}

func safeAnalyzedLogRef(candidate logCandidate, ordinal int, bytesRead int, contentHashSHA256 string) analyzer.AnalyzedLogRef {
	prefix := candidate.SourceID
	if prefix == "" {
		prefix = "log"
	}
	return analyzer.AnalyzedLogRef{
		Label:             fmt.Sprintf("%s log %d", candidate.SourceLabel, ordinal),
		LocalRef:          fmt.Sprintf("%s-log-%d", prefix, ordinal),
		SizeBucket:        byteSizeBucket(bytesRead),
		ContentHashSHA256: contentHashSHA256,
	}
}

func contentHashSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sourceLabelForID(sourceID string) string {
	for _, source := range logSourceDefinitions() {
		if source.id == sourceID {
			return source.label
		}
	}
	switch sourceID {
	case "codex":
		return "Codex"
	case "copilot":
		return "GitHub Copilot"
	case "cursor":
		return "Cursor"
	case "kiro_ide":
		return "Kiro IDE"
	case "antigravity":
		return "Google Antigravity"
	case "opencode":
		return "OpenCode"
	case "unknown", "":
		return "Unknown"
	default:
		return sourceID
	}
}

func byteSizeBucket(bytesRead int) string {
	switch {
	case bytesRead <= 0:
		return "unknown"
	case bytesRead < 10*1024:
		return "<10 KB"
	case bytesRead < 100*1024:
		return "10-100 KB"
	case bytesRead < 1024*1024:
		return "100 KB-1 MB"
	case bytesRead < 5*1024*1024:
		return "1-5 MB"
	default:
		return ">5 MB"
	}
}

func analyzeDiscovered(candidates []logCandidate, out string, mode string, printNextSteps bool) error {
	if len(candidates) == 0 {
		return noSupportedLogsError()
	}
	progress := newProgressBar(len(candidates)*2 + 1)

	results, completedSteps, err := analyzeCandidates(candidates, progress)
	if err != nil {
		progress.Fail()
		return err
	}

	totalBytes := 0
	totalRedacted := 0
	analyzedCandidates := make([]logCandidate, 0, len(results))
	for _, result := range results {
		totalBytes += result.Bytes
		totalRedacted += result.Report.SecurityReceipt.SecretsRedacted
		analyzedCandidates = append(analyzedCandidates, result.Candidate)
	}
	if len(results) == 0 {
		progress.Fail()
		return errors.New("no readable supported agent logs found")
	}

	var report analyzer.Report
	reports := reportsFromResults(results)
	if mode == "" && len(reports) == 1 {
		report = reports[0]
		report.SecurityReceipt.RawLogTTL = "not uploaded"
	} else {
		parserType := "multi_source"
		jobID := "local-multi"
		if mode == "paid" {
			parserType = "paid_bundle"
			jobID = "local-paid"
		} else if mode == "full_scan" {
			parserType = "full_scan_bundle"
			jobID = "local-full-scan"
		}
		report, err = analyzer.AggregateReportsWithParserType(jobID, reports, totalBytes, parserType)
		if err != nil {
			progress.Fail()
			return err
		}
		report.SecurityReceipt.RawLogTTL = "not uploaded"
	}
	report.SourceReports = buildSourceReports(results)
	analyzer.AttachBaselineReceipt(&report, baselineOptionsFromResults(report.SourceReports, results, totalBytes, nil))
	progress.Update(completedSteps, "writing sanitized report")
	if err := writeReport(out, report); err != nil {
		progress.Fail()
		return err
	}
	progress.Finish("complete")

	if mode == "paid" || mode == "full_scan" {
		fmt.Printf("Analyzed locally: %d supported agent logs across %d sources (%s)\n", len(analyzedCandidates), sourceCount(analyzedCandidates), sourceSummary(analyzedCandidates))
	} else {
		fmt.Printf("Analyzed locally: %d target-sized recent supported agent log(s) across %d sources (%s)\n", len(analyzedCandidates), sourceCount(analyzedCandidates), sourceSummary(analyzedCandidates))
	}
	fmt.Printf("Raw bytes read locally: %d\n", totalBytes)
	fmt.Printf("Secrets redacted before report write: %d\n", totalRedacted)
	fmt.Println("Model tokens spent generating this report: 0")
	printReportWrite(out, report)
	if printNextSteps {
		if mode == "paid" {
			fmt.Println()
			fmt.Printf("Review before upload: jq . %s\n", shellQuote(out))
			fmt.Printf("Upload sanitized paid aggregate with the command from your paid unlock page.\n")
		} else if mode == "full_scan" {
			fmt.Println()
			fmt.Printf("Review before upload: jq . %s\n", shellQuote(out))
			fmt.Printf("Upload sanitized full-scan aggregate with the legacy full-scan command.\n")
		} else {
			printNextStepsFor(out)
		}
	}
	return nil
}

func analyzeCandidates(candidates []logCandidate, progress *progressBar) ([]sourceAnalysisResult, int, error) {
	workerCount := analysisWorkerCount(len(candidates))
	if workerCount > 1 {
		progress.Update(0, fmt.Sprintf("analyzing %d logs with %d workers", len(candidates), workerCount))
	}

	jobs := make(chan int)
	outcomes := make(chan candidateAnalysisResult, len(candidates))
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer wg.Done()
			for index := range jobs {
				candidate := candidates[index]
				data, err := candidate.readBytes()
				if err != nil {
					if isPermissionError(err) {
						outcomes <- candidateAnalysisResult{
							Index:     index,
							Skipped:   true,
							SkipLabel: candidate.SourceLabel,
						}
						continue
					}
					outcomes <- candidateAnalysisResult{
						Index: index,
						Err:   fmt.Errorf("read %s log %q: %w", candidate.SourceLabel, candidate.Display, err),
					}
					continue
				}
				report, err := analyzeBytesForSource(fmt.Sprintf("local-%s-%03d", candidate.SourceID, index+1), candidate.SourceID, data)
				if err != nil {
					outcomes <- candidateAnalysisResult{
						Index: index,
						Err:   fmt.Errorf("analyze %s log %d: %w", candidate.SourceLabel, index+1, err),
					}
					continue
				}
				outcomes <- candidateAnalysisResult{
					Index: index,
					Result: sourceAnalysisResult{
						Candidate:         candidate,
						Report:            report,
						Bytes:             len(data),
						ContentHashSHA256: contentHashSHA256(data),
					},
				}
			}
		}()
	}

	go func() {
		for index := range candidates {
			jobs <- index
		}
		close(jobs)
		wg.Wait()
		close(outcomes)
	}()

	results := make([]sourceAnalysisResult, len(candidates))
	completedSteps := 0
	readableCount := 0
	for outcome := range outcomes {
		if outcome.Err != nil {
			return nil, completedSteps, outcome.Err
		}
		completedSteps += 2
		if outcome.Skipped {
			progress.Update(completedSteps, fmt.Sprintf("skipped unreadable %s", outcome.SkipLabel))
			continue
		}
		results[outcome.Index] = outcome.Result
		readableCount++
		progress.Update(completedSteps, fmt.Sprintf("complete %s %s", outcome.Result.Candidate.SourceLabel, outcome.Result.Candidate.shortDisplay()))
	}
	readableResults := make([]sourceAnalysisResult, 0, readableCount)
	for _, result := range results {
		if result.Candidate.SourceID == "" {
			continue
		}
		readableResults = append(readableResults, result)
	}
	return readableResults, completedSteps, nil
}

func analysisWorkerCount(candidateCount int) int {
	if candidateCount <= 1 {
		return 1
	}
	workers := runtime.NumCPU()
	if workers > 4 {
		workers = 4
	}
	if workers > candidateCount {
		workers = candidateCount
	}
	if workers < 1 {
		workers = 1
	}
	return workers
}

func runOneShot(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	out := fs.String("out", "agent-analyzer-report.json", "path to write sanitized report JSON")
	logPath := fs.String("log", "", "explicit supported JSON/JSONL log path")
	baseURL := fs.String("base-url", defaultBaseURL, "Agent Analyzer base URL")
	yes := fs.Bool("yes", false, "upload the sanitized report without an interactive confirmation")
	noOpen := fs.Bool("no-open", false, "do not open the generated report URL in a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("agent-analyzer run: unexpected extra argument %q", fs.Arg(0))
	}
	path := *logPath
	if path == "" {
		candidates, err := defaultSupportedLogsFn()
		if err != nil {
			return err
		}
		if err := analyzeDiscovered(candidates, *out, "", false); err != nil {
			return err
		}
	} else {
		source, err := explicitOrInferredSource("", path)
		if err != nil {
			return err
		}
		if err := analyzeSingle(path, *out, false, source); err != nil {
			return err
		}
	}
	fmt.Println()
	fmt.Println("Are you ready to get your report?")
	fmt.Println("- raw agent logs stayed on this machine")
	fmt.Println("- only the sanitized report JSON will be uploaded")
	fmt.Printf("- report file: %s\n", *out)
	if !*yes && !confirmUpload(os.Stdin, os.Stdout) {
		fmt.Println("Upload cancelled.")
		return nil
	}
	result, err := uploadReport(*out, *baseURL)
	if err != nil {
		return err
	}
	fmt.Printf("Uploaded sanitized report only: %s\n", *out)
	fmt.Printf("Report: %s\n", result.ReportURL)
	if result.ExpiresAt != nil && !result.ExpiresAt.IsZero() {
		fmt.Printf("Expires: %s\n", result.ExpiresAt.Local().Format(time.RFC1123))
	}
	if !*noOpen {
		_ = openBrowser(result.ReportURL)
	}
	return nil
}

func reorderAnalyzeArgs(args []string) []string {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--out" || arg == "-out" || arg == "--log" || arg == "-log" || arg == "--source" || arg == "-source" || arg == "--limit" || arg == "-limit" || arg == "--baseline" || arg == "-baseline":
			flags = append(flags, arg)
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		case strings.HasPrefix(arg, "--out=") || strings.HasPrefix(arg, "-out=") ||
			strings.HasPrefix(arg, "--log=") || strings.HasPrefix(arg, "-log=") ||
			strings.HasPrefix(arg, "--source=") || strings.HasPrefix(arg, "-source=") ||
			strings.HasPrefix(arg, "--limit=") || strings.HasPrefix(arg, "-limit=") ||
			strings.HasPrefix(arg, "--baseline=") || strings.HasPrefix(arg, "-baseline="):
			flags = append(flags, arg)
		case strings.HasPrefix(arg, "-"):
			flags = append(flags, arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	return append(flags, positionals...)
}

func runAnalyzePaid(out string, limit int) error {
	if limit <= 0 {
		return errors.New("agent-analyzer analyze: --limit must be greater than zero")
	}
	if limit > maxAutoLogLimit {
		return fmt.Errorf("agent-analyzer analyze: --limit cannot exceed %d", maxAutoLogLimit)
	}
	candidates, err := recentSupportedLogsFn(limit)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return noSupportedLogsError()
	}
	return analyzeDiscovered(candidates, out, "paid", true)
}

func runProveSavings(args []string) error {
	fs := flag.NewFlagSet("prove-savings", flag.ContinueOnError)
	out := fs.String("out", "agent-analyzer-followup-report.json", "path to write sanitized follow-up report JSON")
	baselinePath := fs.String("baseline", "", "prior sanitized Agent Analyzer report JSON")
	logPath := fs.String("log", "", "explicit supported JSON/JSONL log path for the follow-up run")
	sourceID := fs.String("source", "", "source ID for an explicit follow-up log path")
	limit := fs.Int("limit", defaultAutoLogLimit, "maximum target-sized recent logs per supported source to analyze")
	orderedArgs := reorderAnalyzeArgs(args)
	if err := fs.Parse(orderedArgs); err != nil {
		return err
	}
	if strings.TrimSpace(*baselinePath) == "" {
		return errors.New("agent-analyzer prove-savings: --baseline is required")
	}
	if *limit <= 0 {
		return errors.New("agent-analyzer prove-savings: --limit must be greater than zero")
	}
	if *limit > maxAutoLogLimit {
		return fmt.Errorf("agent-analyzer prove-savings: --limit cannot exceed %d", maxAutoLogLimit)
	}
	positional := fs.Args()
	if len(positional) >= 1 && *logPath != "" {
		return errors.New("agent-analyzer prove-savings: cannot combine positional log path with --log flag")
	}
	if len(positional) >= 2 {
		return fmt.Errorf("agent-analyzer prove-savings: unexpected extra argument %q", positional[1])
	}
	if *logPath == "" && len(positional) == 1 {
		*logPath = positional[0]
	}

	baselineReport, err := readSanitizedReport(*baselinePath)
	if err != nil {
		return err
	}
	baseline := baselineReport.BaselineReceipt
	if baseline.SchemaVersion == "" {
		baseline = analyzer.BuildBaselineReceipt(baselineReport, analyzer.BaselineOptions{})
	}

	var candidates []logCandidate
	if strings.TrimSpace(*logPath) != "" {
		source, err := explicitOrInferredSource(*sourceID, *logPath)
		if err != nil {
			return err
		}
		candidates = []logCandidate{explicitLogCandidate(source, *logPath)}
	} else {
		candidates, err = recentSupportedLogsFn(*limit)
		if err != nil {
			return err
		}
	}
	if len(candidates) == 0 {
		return noSupportedLogsError()
	}

	progress := newProgressBar(len(candidates)*2 + 1)
	results, completedSteps, err := analyzeCandidates(candidates, progress)
	if err != nil {
		progress.Fail()
		return err
	}
	report, totalBytes, err := reportFromAnalysisResults(results, "follow_up")
	if err != nil {
		progress.Fail()
		return err
	}
	receipts := localReducerReceipts(baseline.Window.End)
	analyzer.AttachSavingsValidation(&report, baseline, baselineOptionsFromResults(report.SourceReports, results, totalBytes, receipts))
	progress.Update(completedSteps, "writing sanitized follow-up report")
	if err := writeReport(*out, report); err != nil {
		progress.Fail()
		return err
	}
	progress.Finish("complete")

	fmt.Printf("Analyzed follow-up locally: %d supported agent log(s) across %d sources (%s)\n", len(results), sourceCount(candidates), sourceSummary(candidates))
	fmt.Printf("Raw bytes read locally: %d\n", totalBytes)
	fmt.Printf("Evidence tier: %s\n", report.SavingsValidation.EvidenceTier)
	fmt.Printf("Savings validation: %s\n", report.SavingsValidation.Summary)
	printReportWrite(*out, report)
	return nil
}

func readSanitizedReport(path string) (analyzer.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return analyzer.Report{}, err
	}
	var report analyzer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return analyzer.Report{}, fmt.Errorf("report is not valid analyzer JSON: %w", err)
	}
	if report.SecurityReceipt.RawTranscriptSentToLLM {
		return analyzer.Report{}, errors.New("refusing baseline report that claims raw transcript was sent to an LLM")
	}
	return report, nil
}

func explicitLogCandidate(sourceID, path string) logCandidate {
	candidate := logCandidate{
		SourceID:    sourceID,
		SourceLabel: sourceLabelForID(sourceID),
		Display:     path,
		Read:        candidateReadFunc(sourceID, path),
	}
	if info, err := os.Stat(path); err == nil {
		candidate.ModTime = info.ModTime()
		candidate.Size = info.Size()
	}
	return candidate
}

func baselineOptionsFromResults(sourceReports []analyzer.SourceReport, results []sourceAnalysisResult, totalBytes int, receipts []analyzer.ReducerReceipt) analyzer.BaselineOptions {
	var start time.Time
	var end time.Time
	for _, result := range results {
		mod := result.Candidate.ModTime
		if mod.IsZero() {
			continue
		}
		if start.IsZero() || mod.Before(start) {
			start = mod
		}
		if end.IsZero() || mod.After(end) {
			end = mod
		}
	}
	_ = sourceReports
	return analyzer.BaselineOptions{
		WindowStart:     start,
		WindowEnd:       end,
		InputSizeBytes:  totalBytes,
		ReducerReceipts: receipts,
	}
}

func localReducerReceipts(baselineEnd string) []analyzer.ReducerReceipt {
	var since time.Time
	if baselineEnd != "" {
		if parsed, err := time.Parse(time.RFC3339, baselineEnd); err == nil {
			since = parsed
		}
	}
	receipt := contextModeReducerReceipt(since)
	if receipt.ToolID == "" {
		return nil
	}
	return []analyzer.ReducerReceipt{receipt}
}

func contextModeReducerReceipt(since time.Time) analyzer.ReducerReceipt {
	root := filepath.Join(claudeConfigDir(), "context-mode", "sessions")
	matches, err := filepath.Glob(filepath.Join(root, "stats-pid-*.json"))
	if err != nil || len(matches) == 0 {
		return analyzer.ReducerReceipt{}
	}
	type stats struct {
		UpdatedAt      int64 `json:"updated_at"`
		TotalCalls     int   `json:"total_calls"`
		TokensSaved    int   `json:"tokens_saved"`
		TotalProcessed int   `json:"total_processed"`
		BytesReturned  int   `json:"bytes_returned"`
		ReductionPct   int   `json:"reduction_pct"`
	}
	var receipt analyzer.ReducerReceipt
	receipt.ToolID = "context_mode"
	receipt.ReceiptSource = "context_mode_session_stats"
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var current stats
		if err := json.Unmarshal(data, &current); err != nil {
			continue
		}
		if !since.IsZero() && current.UpdatedAt > 0 {
			updated := time.UnixMilli(current.UpdatedAt)
			if updated.Before(since) || updated.Equal(since) {
				continue
			}
		}
		if current.TotalCalls <= 0 && current.TokensSaved <= 0 {
			continue
		}
		receipt.Calls += max(0, current.TotalCalls)
		receipt.TokensSaved += max(0, current.TokensSaved)
		receipt.ProcessedBytes += max(0, current.TotalProcessed)
		receipt.ReturnedBytes += max(0, current.BytesReturned)
	}
	if receipt.Calls == 0 && receipt.TokensSaved == 0 {
		return analyzer.ReducerReceipt{}
	}
	if receipt.ProcessedBytes > 0 {
		receipt.ReductionPct = int(float64(receipt.ProcessedBytes-receipt.ReturnedBytes) / float64(receipt.ProcessedBytes) * 100)
		if receipt.ReductionPct < 0 {
			receipt.ReductionPct = 0
		}
		if receipt.ReductionPct > 100 {
			receipt.ReductionPct = 100
		}
	}
	return receipt
}

func reportFromAnalysisResults(results []sourceAnalysisResult, mode string) (analyzer.Report, int, error) {
	if len(results) == 0 {
		return analyzer.Report{}, 0, errors.New("no readable supported agent logs found")
	}
	totalBytes := 0
	for _, result := range results {
		totalBytes += result.Bytes
	}
	reports := reportsFromResults(results)
	if mode == "" && len(reports) == 1 {
		report := reports[0]
		report.SecurityReceipt.RawLogTTL = "not uploaded"
		report.SourceReports = buildSourceReports(results)
		analyzer.AttachBaselineReceipt(&report, baselineOptionsFromResults(report.SourceReports, results, totalBytes, nil))
		return report, totalBytes, nil
	}
	parserType := "multi_source"
	jobID := "local-multi"
	if mode == "paid" {
		parserType = "paid_bundle"
		jobID = "local-paid"
	} else if mode == "full_scan" {
		parserType = "full_scan_bundle"
		jobID = "local-full-scan"
	} else if mode == "follow_up" {
		parserType = "follow_up_bundle"
		jobID = "local-follow-up"
	}
	report, err := analyzer.AggregateReportsWithParserType(jobID, reports, totalBytes, parserType)
	if err != nil {
		return analyzer.Report{}, 0, err
	}
	report.SecurityReceipt.RawLogTTL = "not uploaded"
	report.SourceReports = buildSourceReports(results)
	analyzer.AttachBaselineReceipt(&report, baselineOptionsFromResults(report.SourceReports, results, totalBytes, nil))
	return report, totalBytes, nil
}

func runAnalyzeFullScan(out string, limit int) error {
	if limit <= 0 {
		return errors.New("agent-analyzer full-scan: --limit must be greater than zero")
	}
	if limit > maxAutoLogLimit {
		return fmt.Errorf("agent-analyzer full-scan: --limit cannot exceed %d", maxAutoLogLimit)
	}
	candidates, err := recentSupportedLogsFn(limit)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return noSupportedLogsError()
	}
	return analyzeDiscovered(candidates, out, "full_scan", true)
}

func runFullScan(args []string) error {
	fs := flag.NewFlagSet("full-scan", flag.ContinueOnError)
	out := fs.String("out", "agent-analyzer-full-scan-report.json", "path to write sanitized full-scan report JSON")
	baseURL := fs.String("base-url", defaultBaseURL, "Agent Analyzer base URL")
	token := fs.String("token", "", "legacy full-scan entitlement token")
	limit := fs.Int("limit", defaultAutoLogLimit, "maximum target-sized recent logs per supported source to analyze")
	noOpen := fs.Bool("no-open", false, "do not open the generated report URL in a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("agent-analyzer full-scan: unexpected extra argument %q", fs.Arg(0))
	}
	if strings.TrimSpace(*token) == "" {
		return errors.New("agent-analyzer full-scan: --token is required")
	}
	if err := runAnalyzeFullScan(*out, *limit); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("Uploading sanitized full-scan aggregate.")
	fmt.Println("- raw agent logs stayed on this machine")
	fmt.Println("- only the sanitized aggregate JSON will be uploaded")
	fmt.Printf("- report file: %s\n", *out)
	result, err := uploadFullScanReport(*out, *baseURL, *token)
	if err != nil {
		return err
	}
	fmt.Printf("Uploaded sanitized full-scan report only: %s\n", *out)
	fmt.Printf("Report: %s\n", result.ReportURL)
	if result.ExpiresAt != nil && !result.ExpiresAt.IsZero() {
		fmt.Printf("Expires: %s\n", result.ExpiresAt.Local().Format(time.RFC1123))
	}
	if !*noOpen {
		_ = openBrowser(result.ReportURL)
	}
	return nil
}

func runUpload(args []string) error {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	baseURL := fs.String("base-url", defaultBaseURL, "Agent Analyzer base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: agent-analyzer upload <sanitized-report.json>")
	}
	reportPath := fs.Arg(0)
	result, err := uploadReport(reportPath, *baseURL)
	if err != nil {
		return err
	}
	fmt.Printf("Uploaded sanitized report only: %s\n", reportPath)
	fmt.Printf("Report: %s\n", result.ReportURL)
	if result.ExpiresAt != nil && !result.ExpiresAt.IsZero() {
		fmt.Printf("Expires: %s\n", result.ExpiresAt.Local().Format(time.RFC1123))
	}
	return nil
}

type uploadResult struct {
	ReportURL string     `json:"report_url"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func uploadReport(reportPath, baseURL string) (uploadResult, error) {
	return uploadReportToEndpoint(reportPath, strings.TrimRight(baseURL, "/")+"/api/client-reports", "")
}

func uploadFullScanReport(reportPath, baseURL, token string) (uploadResult, error) {
	return uploadReportToEndpoint(reportPath, strings.TrimRight(baseURL, "/")+"/api/full-scan-client-reports", token)
}

func uploadReportToEndpoint(reportPath, endpoint, bearer string) (uploadResult, error) {
	data, err := os.ReadFile(reportPath)
	if err != nil {
		return uploadResult{}, err
	}
	var report analyzer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return uploadResult{}, fmt.Errorf("report is not valid analyzer JSON: %w", err)
	}
	if report.SecurityReceipt.RawTranscriptSentToLLM {
		return uploadResult{}, errors.New("refusing to upload report that claims raw transcript was sent to an LLM")
	}

	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return uploadResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return uploadResult{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if err != nil {
		return uploadResult{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return uploadResult{}, fmt.Errorf("upload failed: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var result uploadResult
	if err := json.Unmarshal(body, &result); err != nil {
		return uploadResult{}, err
	}
	return result, nil
}

type logCandidate struct {
	SourceID    string
	SourceLabel string
	Display     string
	ModTime     time.Time
	Size        int64
	Read        func() ([]byte, error)
}

func (candidate logCandidate) readBytes() ([]byte, error) {
	if candidate.Read != nil {
		return candidate.Read()
	}
	return os.ReadFile(candidate.Display)
}

func (candidate logCandidate) shortDisplay() string {
	return shortDisplay(candidate.Display)
}

func shortDisplay(value string) string {
	if value == "" {
		return "log"
	}
	if strings.Contains(value, string(os.PathSeparator)) {
		if base := filepath.Base(value); base != "." && base != string(os.PathSeparator) {
			return base
		}
	}
	if len(value) > 48 {
		return value[:45] + "..."
	}
	return value
}

func defaultSupportedLogs() ([]logCandidate, error) {
	return recentSupportedLogs(defaultAutoLogLimit)
}

func recentSupportedLogs(limit int) ([]logCandidate, error) {
	return recentSupportedLogsWithBounds(limit, 0, freeAutoMinLogBytes)
}

func recentSupportedLogsWithBounds(limit int, maxBytes int64, minBytes int64) ([]logCandidate, error) {
	if limit <= 0 {
		return nil, errors.New("log discovery limit must be greater than zero")
	}
	var candidates []logCandidate
	for _, source := range logSourceDefinitions() {
		var found []logCandidate
		var err error
		if source.id == "codex" {
			found, err = recentCodexLogs(limit, maxBytes, minBytes)
		} else {
			found, err = recentPathLogs(source.id, source.label, source.roots, source.accept, limit, maxBytes, minBytes)
		}
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, found...)
	}
	codexSQLite, err := recentCodexSQLiteLogs(limit, maxBytes, minBytes)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, codexSQLite...)
	openCode, err := recentOpenCodeSessions(limit, maxBytes, minBytes)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, openCode...)
	kiroSessions, err := recentKiroWorkspaceSessions(limit, maxBytes, minBytes)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, kiroSessions...)
	sqliteCandidates, err := recentSQLiteSourceLogs(limit, maxBytes, minBytes)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, sqliteCandidates...)
	candidates = selectLargestRecentCandidatesPerSource(candidates, limit)
	if len(candidates) == 0 {
		return nil, noSupportedLogsError()
	}
	return candidates, nil
}

type logSourceDefinition struct {
	id     string
	label  string
	roots  []string
	accept func(path string, info os.FileInfo) bool
}

func logSourceDefinitions() []logSourceDefinition {
	return []logSourceDefinition{
		{
			id:     "claude_code",
			label:  "Claude Code",
			roots:  []string{filepath.Join(claudeConfigDir(), "projects")},
			accept: acceptExtension(".jsonl"),
		},
		{
			id:     "codex",
			label:  "Codex",
			roots:  codexSessionRoots(),
			accept: acceptCodexRollout,
		},
		{
			id:     "copilot",
			label:  "GitHub Copilot",
			roots:  copilotLogRoots(),
			accept: acceptCopilotLog,
		},
		{
			id:     "claude_desktop",
			label:  "Claude Desktop",
			roots:  claudeDesktopSessionRoots(),
			accept: acceptClaudeDesktopSession,
		},
		{
			id:     "claude_desktop_mcp",
			label:  "Claude Desktop MCP",
			roots:  claudeDesktopLogRoots(),
			accept: acceptClaudeDesktopMCPLog,
		},
		{
			id:     "cursor",
			label:  "Cursor",
			roots:  cursorTranscriptRoots(),
			accept: acceptCursorTranscript,
		},
		{
			id:     "kiro_cli",
			label:  "Kiro CLI",
			roots:  kiroCLILogRoots(),
			accept: acceptKiroCLILog,
		},
		{
			id:     "kiro_ide",
			label:  "Kiro IDE",
			roots:  kiroIDELogRoots(),
			accept: acceptKiroIDELog,
		},
		{
			id:     "antigravity",
			label:  "Google Antigravity",
			roots:  antigravityTranscriptRoots(),
			accept: acceptAntigravityTranscript,
		},
	}
}

func recentPathLogs(sourceID, sourceLabel string, roots []string, accept func(string, os.FileInfo) bool, limit int, maxBytes int64, minBytes int64) ([]logCandidate, error) {
	var matches []logMatch
	seenRoots := map[string]bool{}
	for _, root := range roots {
		if root == "" || seenRoots[root] {
			continue
		}
		seenRoots[root] = true
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				if isSkippableDiscoveryError(err) {
					return nil
				}
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				if isSkippableDiscoveryError(err) {
					return nil
				}
				return err
			}
			if accept != nil && !accept(path, info) {
				return nil
			}
			if maxBytes > 0 && info.Size() > maxBytes {
				return nil
			}
			if minBytes > 0 && info.Size() < minBytes {
				return nil
			}
			matches = append(matches, logMatch{path: path, modTime: info.ModTime(), size: info.Size()})
			return nil
		})
		if err != nil {
			if isSkippableDiscoveryError(err) {
				continue
			}
			return nil, fmt.Errorf("discover %s root %q: %w", sourceLabel, root, err)
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	matches = selectTargetSizedRecentMatches(matches, limit)
	candidates := make([]logCandidate, 0, len(matches))
	for _, match := range matches {
		candidates = append(candidates, logCandidate{
			SourceID:    sourceID,
			SourceLabel: sourceLabel,
			Display:     match.path,
			ModTime:     match.modTime,
			Size:        match.size,
			Read:        candidateReadFunc(sourceID, match.path),
		})
	}
	return candidates, nil
}

func candidateReadFunc(sourceID, path string) func() ([]byte, error) {
	if sourceID == "claude_desktop" {
		return func() ([]byte, error) {
			return readClaudeDesktopSession(path)
		}
	}
	if sourceID == "claude_desktop_mcp" {
		if server := claudeDesktopMCPServerName(path); server != "" {
			return func() ([]byte, error) {
				data, err := os.ReadFile(path)
				if err != nil {
					return nil, err
				}
				header := []byte("Available MCP servers:\n- " + server + "\n")
				return append(header, data...), nil
			}
		}
	}
	if sourceID == "copilot" && strings.EqualFold(filepath.Ext(path), ".json") {
		return func() ([]byte, error) {
			return readCopilotJSONSession(path)
		}
	}
	return nil
}

func readClaudeDesktopSession(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		return data, nil
	}
	servers := claudeDesktopSessionMCPServers(data)
	if len(servers) == 0 {
		return data, nil
	}
	var builder strings.Builder
	builder.WriteString("Available MCP servers:\n")
	for _, server := range servers {
		builder.WriteString("- ")
		builder.WriteString(server)
		builder.WriteByte('\n')
	}
	builder.Write(data)
	return []byte(builder.String()), nil
}

func claudeDesktopSessionMCPServers(data []byte) []string {
	var obj map[string]any
	if json.Unmarshal(data, &obj) != nil {
		return nil
	}
	tools, ok := obj["enabledMcpTools"].(map[string]any)
	if !ok || len(tools) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var servers []string
	for raw := range tools {
		server := sanitizeMCPServerFromClaudeToolKey(raw)
		if server == "" || seen[server] {
			continue
		}
		seen[server] = true
		servers = append(servers, server)
	}
	sort.Strings(servers)
	return servers
}

func sanitizeMCPServerFromClaudeToolKey(key string) string {
	parts := strings.Split(key, ":")
	if len(parts) >= 3 {
		key = parts[len(parts)-2]
	}
	key = strings.TrimSpace(key)
	var out []rune
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			out = append(out, r)
		case r == ' ' || r == '.':
			out = append(out, '_')
		}
		if len(out) >= 64 {
			break
		}
	}
	return string(out)
}

func noSupportedLogsError() error {
	return errors.New("no supported agent logs found; checked Claude Code, Claude Desktop, Codex, GitHub Copilot, OpenCode, Claude Desktop MCP, Cursor, Kiro, and Google Antigravity")
}

func acceptExtension(ext string) func(string, os.FileInfo) bool {
	return func(path string, _ os.FileInfo) bool {
		return strings.EqualFold(filepath.Ext(path), ext)
	}
}

func acceptCodexRollout(path string, _ os.FileInfo) bool {
	return strings.EqualFold(filepath.Ext(path), ".jsonl")
}

func acceptCopilotLog(path string, _ os.FileInfo) bool {
	normalized := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.EqualFold(base, "events.jsonl") && strings.Contains(normalized, "/session-state/"):
		return true
	case strings.EqualFold(filepath.Ext(base), ".log") && strings.Contains(normalized, "/logs/"):
		return true
	case strings.EqualFold(filepath.Ext(base), ".json") && strings.Contains(normalized, "/chatsessions/"):
		return true
	case strings.EqualFold(filepath.Ext(base), ".json") && strings.Contains(normalized, "/emptywindowchatsessions/"):
		return true
	default:
		return false
	}
}

func acceptClaudeDesktopSession(path string, _ os.FileInfo) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == "audit.jsonl" {
		return true
	}
	return strings.HasPrefix(base, "local_") && strings.EqualFold(filepath.Ext(base), ".json")
}

func acceptClaudeDesktopMCPLog(path string, _ os.FileInfo) bool {
	base := strings.ToLower(filepath.Base(path))
	return filepath.Ext(base) == ".log" && (base == "mcp.log" || strings.HasPrefix(base, "mcp-server-"))
}

func acceptCursorTranscript(path string, _ os.FileInfo) bool {
	normalized := filepath.ToSlash(path)
	return strings.EqualFold(filepath.Ext(path), ".jsonl") && strings.Contains(normalized, "/agent-transcripts/")
}

func acceptKiroCLILog(path string, _ os.FileInfo) bool {
	if configured := os.Getenv("KIRO_CHAT_LOG_FILE"); configured != "" && filepath.Clean(path) == filepath.Clean(configured) {
		return true
	}
	return strings.EqualFold(filepath.Base(path), "kiro-chat.log")
}

func acceptKiroIDELog(path string, _ os.FileInfo) bool {
	if !strings.EqualFold(filepath.Ext(path), ".log") {
		return false
	}
	base := strings.ToLower(filepath.Base(path))
	return strings.Contains(base, "kiro") ||
		base == "main.log" ||
		base == "renderer.log" ||
		base == "terminal.log" ||
		base == "telemetry.log"
}

func acceptAntigravityTranscript(path string, _ os.FileInfo) bool {
	return strings.EqualFold(filepath.Base(path), "transcript.jsonl")
}

func claudeConfigDir() string {
	if configured := os.Getenv("CLAUDE_CONFIG_DIR"); configured != "" {
		return configured
	}
	return filepath.Join(homeDir(), ".claude")
}

func codexSessionRoots() []string {
	root := os.Getenv("CODEX_HOME")
	if root == "" {
		root = filepath.Join(homeDir(), ".codex")
	}
	return []string{
		filepath.Join(root, "sessions"),
		filepath.Join(root, "archived_sessions"),
	}
}

func codexHomeDir() string {
	if root := os.Getenv("CODEX_HOME"); root != "" {
		return root
	}
	return filepath.Join(homeDir(), ".codex")
}

func copilotHomeDir() string {
	if root := os.Getenv("COPILOT_HOME"); root != "" {
		return root
	}
	return filepath.Join(homeDir(), ".copilot")
}

func copilotLogRoots() []string {
	home := copilotHomeDir()
	roots := []string{
		filepath.Join(home, "session-state"),
		filepath.Join(home, "logs"),
	}
	for _, app := range []string{"Code", "Code - Insiders"} {
		root := appSupportDir(app)
		roots = append(roots,
			filepath.Join(root, "User", "workspaceStorage"),
			filepath.Join(root, "User", "globalStorage", "emptyWindowChatSessions"),
		)
	}
	return roots
}

func readCopilotJSONSession(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return copilotJSONSessionAsJSONL(data)
}

func copilotJSONSessionAsJSONL(data []byte) ([]byte, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return data, nil
	}
	var output bytes.Buffer
	appendCopilotRows(&output, root)
	if output.Len() == 0 {
		compact, err := json.Marshal(root)
		if err != nil {
			return data, nil
		}
		output.Write(compact)
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}

func appendCopilotRows(output *bytes.Buffer, root any) {
	if root == nil {
		return
	}
	if obj, ok := root.(map[string]any); ok {
		if title := firstPresentString(obj, "customTitle", "title"); title != "" {
			writeCopilotRow(output, map[string]any{"type": "message", "role": "system", "content": title})
		}
		if requests, ok := firstPresentValue(obj, "requests").([]any); ok {
			for _, request := range requests {
				appendCopilotRequestRows(output, request)
			}
			return
		}
	}
	appendCopilotRequestRows(output, root)
}

func appendCopilotRequestRows(output *bytes.Buffer, request any) {
	if obj, ok := request.(map[string]any); ok {
		if content := copilotText(firstPresentValue(obj, "message", "prompt", "request", "input")); content != "" {
			writeCopilotRow(output, map[string]any{"type": "message", "role": "user", "content": content})
		}
		if content := copilotText(firstPresentValue(obj, "response", "responses", "answer")); content != "" {
			writeCopilotRow(output, map[string]any{"type": "message", "role": "assistant", "content": content})
		}
		if usage := firstJSONMapByKey(obj, "usage"); len(usage) > 0 {
			row := map[string]any{"type": "token_count"}
			for _, key := range []string{"input_tokens", "prompt_tokens", "cached_input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "output_tokens", "completion_tokens"} {
				if value := firstJSONIntByKey(usage, key); value != 0 {
					row[key] = value
				}
			}
			if len(row) > 1 {
				writeCopilotRow(output, row)
			}
		}
	}
	appendCopilotToolRows(output, request)
}

func appendCopilotToolRows(output *bytes.Buffer, value any) {
	var walk func(any)
	walk = func(item any) {
		switch typed := item.(type) {
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case map[string]any:
			if row, ok := copilotToolRow(typed); ok {
				writeCopilotRow(output, row)
			}
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
}

func copilotToolRow(obj map[string]any) (map[string]any, bool) {
	tool := firstPresentString(obj, "toolName", "tool_name", "tool", "toolId", "tool_id")
	if tool == "" {
		return nil, false
	}
	eventText := strings.ToLower(strings.Join([]string{
		firstPresentString(obj, "type", "kind", "event", "phase", "status"),
		tool,
	}, " "))
	if !strings.Contains(eventText, "tool") &&
		!strings.Contains(eventText, "function") &&
		!strings.Contains(eventText, "terminal") &&
		firstPresentValue(obj, "arguments", "args", "input", "command", "output", "result") == nil {
		return nil, false
	}
	row := map[string]any{
		"type": "tool_call",
		"tool": tool,
	}
	if id := firstPresentString(obj, "callId", "call_id", "id", "toolCallId", "tool_call_id"); id != "" {
		row["call_id"] = id
	}
	if output := firstPresentValue(obj, "output", "result", "tool_output"); output != nil || strings.Contains(eventText, "result") || strings.Contains(eventText, "output") {
		row["type"] = "tool_result"
		if output != nil {
			row["output"] = output
		}
		return row, true
	}
	if args := firstPresentValue(obj, "arguments", "args", "input", "command"); args != nil {
		row["arguments"] = args
	}
	return row, true
}

func copilotText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case []any:
		var parts []string
		for _, item := range typed {
			if text := copilotText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		for _, key := range []string{"text", "content", "value", "message"} {
			if text := copilotText(typed[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

func writeCopilotRow(output *bytes.Buffer, row map[string]any) {
	encoded, err := json.Marshal(row)
	if err != nil || len(encoded) == 0 {
		return
	}
	output.Write(encoded)
	output.WriteByte('\n')
}

func firstPresentValue(value map[string]any, keys ...string) any {
	for _, key := range keys {
		if item, ok := value[key]; ok {
			return item
		}
	}
	return nil
}

func firstPresentString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func firstJSONMapByKey(value any, target string) map[string]any {
	target = strings.ToLower(target)
	var found map[string]any
	var walk func(any)
	walk = func(v any) {
		if found != nil {
			return
		}
		switch typed := v.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				item := typed[key]
				if strings.ToLower(key) == target {
					if mapped, ok := item.(map[string]any); ok {
						found = mapped
						return
					}
				}
				walk(item)
			}
		}
	}
	walk(value)
	if found == nil {
		return map[string]any{}
	}
	return found
}

func firstJSONIntByKey(value any, target string) int {
	target = strings.ToLower(target)
	var found int
	var walk func(any)
	walk = func(v any) {
		if found != 0 {
			return
		}
		switch typed := v.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				item := typed[key]
				if strings.ToLower(key) == target {
					found = intFromAny(item)
					if found != 0 {
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

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func recentCodexLogs(limit int, maxBytes int64, minBytes int64) ([]logCandidate, error) {
	indexed, err := recentCodexIndexedSessions(limit, maxBytes, minBytes)
	if err != nil {
		return nil, err
	}
	if len(indexed) > 0 {
		return indexed, nil
	}
	return recentPathLogs("codex", "Codex", codexSessionRoots(), acceptCodexRollout, limit, maxBytes, minBytes)
}

func recentCodexIndexedSessions(limit int, maxBytes int64, minBytes int64) ([]logCandidate, error) {
	indexPath := filepath.Join(codexHomeDir(), "session_index.jsonl")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isPermissionError(err) {
			return nil, nil
		}
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	var matches []logMatch
	seen := map[string]bool{}
	for scanner.Scan() {
		var obj map[string]any
		if json.Unmarshal(bytes.TrimSpace(scanner.Bytes()), &obj) != nil {
			continue
		}
		path := codexSessionPathFromIndex(obj)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if maxBytes > 0 && info.Size() > maxBytes {
			continue
		}
		if minBytes > 0 && info.Size() < minBytes {
			continue
		}
		matches = append(matches, logMatch{path: path, modTime: info.ModTime(), size: info.Size()})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	matches = selectTargetSizedRecentMatches(matches, limit)
	candidates := make([]logCandidate, 0, len(matches))
	for _, match := range matches {
		candidates = append(candidates, logCandidate{
			SourceID:    "codex",
			SourceLabel: "Codex",
			Display:     match.path,
			ModTime:     match.modTime,
			Size:        match.size,
		})
	}
	return candidates, nil
}

func codexSessionPathFromIndex(obj map[string]any) string {
	for _, key := range []string{"path", "session_path", "log_path", "file", "filepath", "filename"} {
		if value, ok := obj[key].(string); ok {
			if path := resolveCodexJSONLPath(value); path != "" {
				return path
			}
		}
	}
	var found string
	var walk func(any)
	walk = func(value any) {
		if found != "" {
			return
		}
		switch typed := value.(type) {
		case string:
			found = resolveCodexJSONLPath(typed)
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
	return found
}

func resolveCodexJSONLPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasSuffix(strings.ToLower(value), ".jsonl") {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(codexHomeDir(), value)
}

func claudeDesktopLogRoots() []string {
	return claudeDesktopLogRootsFor(runtime.GOOS, homeDir(), appDataDir())
}

func claudeDesktopSessionRoots() []string {
	root := appSupportDir("Claude")
	return []string{
		filepath.Join(root, "local-agent-mode-sessions"),
		filepath.Join(root, "claude-code-sessions"),
		filepath.Join(root, "audit.jsonl"),
	}
}

func claudeDesktopLogRootsFor(goos, home, appData string) []string {
	switch goos {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Logs", "Claude")}
	case "windows":
		return []string{filepath.Join(appData, "Claude", "logs")}
	default:
		return nil
	}
}

func cursorTranscriptRoots() []string {
	return []string{
		filepath.Join(homeDir(), ".cursor", "projects"),
		filepath.Join(appSupportDir("Cursor"), "User", "workspaceStorage"),
		filepath.Join(appSupportDir("Cursor"), "User", "globalStorage"),
	}
}

func kiroCLILogRoots() []string {
	roots := kiroCLILogRootsFor(runtime.GOOS, os.TempDir(), os.Getenv("XDG_RUNTIME_DIR"), kiroHomeDir())
	if configured := os.Getenv("KIRO_CHAT_LOG_FILE"); configured != "" {
		return append([]string{filepath.Dir(configured)}, roots...)
	}
	return roots
}

func kiroCLILogRootsFor(goos, tempDir, runtimeDir, kiroHome string) []string {
	roots := []string{filepath.Join(kiroHome, "logs")}
	switch goos {
	case "windows":
		return append(roots, filepath.Join(tempDir, "kiro-log", "logs"))
	case "linux":
		if runtimeDir != "" {
			return append(roots, filepath.Join(runtimeDir, "kiro-log"))
		}
		return append(roots, filepath.Join(tempDir, "kiro-log"))
	default:
		return append(roots, filepath.Join(tempDir, "kiro-log"))
	}
}

func kiroHomeDir() string {
	if configured := os.Getenv("KIRO_HOME"); configured != "" {
		return configured
	}
	return filepath.Join(homeDir(), ".kiro")
}

func kiroIDELogRoots() []string {
	return []string{filepath.Join(appSupportDir("Kiro"), "logs")}
}

func kiroWorkspaceSessionRoots() []string {
	return []string{filepath.Join(appSupportDir("Kiro"), "User", "globalStorage", "kiro.kiroagent", "workspace-sessions")}
}

func recentKiroWorkspaceSessions(limit int, maxBytes int64, minBytes int64) ([]logCandidate, error) {
	return recentPathLogs("kiro_ide", "Kiro IDE", kiroWorkspaceSessionRoots(), acceptKiroWorkspaceSession, limit, maxBytes, minBytes)
}

func acceptKiroWorkspaceSession(path string, _ os.FileInfo) bool {
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		return false
	}
	base := strings.ToLower(filepath.Base(path))
	return base != "sessions.json"
}

type sqliteSourceDefinition struct {
	id       string
	label    string
	roots    []string
	prefixes []string
}

func recentSQLiteSourceLogs(limit int, maxBytes int64, minBytes int64) ([]logCandidate, error) {
	var candidates []logCandidate
	for _, source := range sqliteSourceDefinitions() {
		found, err := recentSQLiteStores(source, limit, maxBytes, minBytes)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, found...)
	}
	return candidates, nil
}

func recentCodexSQLiteLogs(limit int, maxBytes int64, minBytes int64) ([]logCandidate, error) {
	root := codexHomeDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isPermissionError(err) {
			return nil, nil
		}
		return nil, err
	}
	var matches []logMatch
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if !strings.HasPrefix(name, "logs") || !strings.HasSuffix(name, ".sqlite") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			if isSkippableDiscoveryError(err) {
				continue
			}
			return nil, err
		}
		storeSize, err := sqliteStoreSize(path)
		if err != nil {
			continue
		}
		if maxBytes > 0 && storeSize > maxBytes {
			continue
		}
		if minBytes > 0 && storeSize < minBytes {
			continue
		}
		matches = append(matches, logMatch{path: path, modTime: info.ModTime(), size: storeSize})
	}
	if len(matches) == 0 {
		return nil, nil
	}
	matches = selectTargetSizedRecentMatches(matches, limit)
	candidates := make([]logCandidate, 0, len(matches))
	for _, match := range matches {
		dbPath := match.path
		candidates = append(candidates, logCandidate{
			SourceID:    "codex",
			SourceLabel: "Codex SQLite",
			Display:     dbPath,
			ModTime:     match.modTime,
			Size:        match.size,
			Read: func() ([]byte, error) {
				return readCodexSQLiteLogsAsJSONL(dbPath, maxBytes)
			},
		})
	}
	return candidates, nil
}

func readCodexSQLiteLogsAsJSONL(path string, maxOutputBytes int64) ([]byte, error) {
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(path))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if !sqliteTableExists(db, "logs") {
		return []byte("{\"type\":\"message\",\"kind\":\"codex_sqlite_empty\"}\n"), nil
	}
	columnSet, err := sqliteTableColumnSet(db, "logs")
	if err != nil {
		return nil, err
	}
	orderBy := "rowid DESC"
	switch {
	case columnSet["ts"] && columnSet["ts_nanos"]:
		orderBy = "ts DESC, ts_nanos DESC"
	case columnSet["ts"]:
		orderBy = "ts DESC"
	case columnSet["timestamp"]:
		orderBy = "timestamp DESC"
	case columnSet["created_at"]:
		orderBy = "created_at DESC"
	}
	rows, err := db.Query("SELECT * FROM logs ORDER BY " + orderBy + " LIMIT 500")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	columnIndex := map[string]int{}
	for i, column := range columns {
		columnIndex[strings.ToLower(column)] = i
	}
	var output bytes.Buffer
	for rows.Next() {
		values := make([]any, len(columns))
		scanTargets := make([]any, len(columns))
		for i := range values {
			scanTargets[i] = &values[i]
		}
		if err := rows.Scan(scanTargets...); err != nil {
			return nil, err
		}
		cell := func(names ...string) string {
			for _, name := range names {
				if idx, ok := columnIndex[strings.ToLower(name)]; ok {
					if text := strings.TrimSpace(sqliteCellString(values[idx])); text != "" {
						return text
					}
				}
			}
			return ""
		}
		level := cell("level", "severity")
		target := cell("target", "logger", "module")
		estimatedBytes := 0
		if raw := cell("estimated_bytes", "bytes"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil {
				estimatedBytes = parsed
			}
		}
		row := map[string]any{
			"type":            "message",
			"kind":            "codex_diagnostic",
			"level":           strings.ToLower(level),
			"target":          boundedDiagnosticField(target),
			"estimated_bytes": estimatedBytes,
			"error":           strings.Contains(strings.ToLower(level), "error"),
		}
		if timestamp := cell("ts", "timestamp", "created_at"); timestamp != "" {
			row["timestamp"] = boundedDiagnosticField(timestamp)
		}
		if feedback := cell("feedback_log_body", "message", "body", "text", "content", "error"); feedback != "" {
			row["content"] = truncateString(feedback, maxSQLiteValueTextBytes)
		}
		if modulePath := cell("module_path", "module", "target"); modulePath != "" {
			row["module"] = boundedDiagnosticField(modulePath)
		}
		if cell("file", "filename", "path") != "" {
			row["file_kind"] = "present"
		}
		if cell("line", "line_number") != "" {
			row["line_present"] = true
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		if maxOutputBytes > 0 && int64(output.Len()+len(encoded)+1) > maxOutputBytes {
			break
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if output.Len() == 0 {
		return []byte("{\"type\":\"message\",\"kind\":\"codex_sqlite_empty\"}\n"), nil
	}
	return output.Bytes(), nil
}

func sqliteTableColumnSet(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[strings.ToLower(name)] = true
	}
	return columns, rows.Err()
}

func sqliteCellString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case []byte:
		if !utf8.Valid(typed) {
			return ""
		}
		return string(typed)
	case string:
		return typed
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func boundedDiagnosticField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var out []rune
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' || r == ':' {
			out = append(out, r)
		}
		if len(out) >= 96 {
			break
		}
	}
	return string(out)
}

func truncateString(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes]
}

func sqliteSourceDefinitions() []sqliteSourceDefinition {
	return []sqliteSourceDefinition{
		{
			id:    "cursor",
			label: "Cursor SQLite",
			roots: []string{
				filepath.Join(appSupportDir("Cursor"), "User", "globalStorage"),
				filepath.Join(appSupportDir("Cursor"), "User", "workspaceStorage"),
			},
			prefixes: []string{"bubbleId:", "composerData:", "composer.composerData", "agentKv:blob:", "agentKv:", "messageRequestContext:", "aiService.prompts", "aiService.generations", "workbench.panel.aichat.view.aichat.chatdata", "workbench.panel.chat.view.chat.chatdata"},
		},
		{
			id:    "kiro_ide",
			label: "Kiro IDE SQLite",
			roots: []string{
				filepath.Join(appSupportDir("Kiro"), "User", "globalStorage"),
				filepath.Join(appSupportDir("Kiro"), "User", "workspaceStorage"),
			},
			prefixes: []string{"kiro.kiroAgent", "kiro:", "chat", "session"},
		},
		{
			id:    "antigravity",
			label: "Google Antigravity SQLite",
			roots: append(
				sqliteAppStorageRoots("Antigravity"),
				sqliteAppStorageRoots("Antigravity IDE")...,
			),
			prefixes: []string{"agent", "chat", "conversation", "task", "transcript"},
		},
	}
}

func sqliteAppStorageRoots(app string) []string {
	return []string{
		filepath.Join(appSupportDir(app), "User", "globalStorage"),
		filepath.Join(appSupportDir(app), "User", "workspaceStorage"),
	}
}

func recentSQLiteStores(source sqliteSourceDefinition, limit int, maxBytes int64, minBytes int64) ([]logCandidate, error) {
	var matches []logMatch
	seenRoots := map[string]bool{}
	for _, root := range source.roots {
		if root == "" || seenRoots[root] {
			continue
		}
		seenRoots[root] = true
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				if isSkippableDiscoveryError(err) {
					return nil
				}
				return err
			}
			if entry.IsDir() || filepath.Base(path) != "state.vscdb" {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				if isSkippableDiscoveryError(err) {
					return nil
				}
				return err
			}
			storeSize, err := sqliteStoreSize(path)
			if err != nil {
				return nil
			}
			if maxBytes > 0 && storeSize > maxBytes {
				return nil
			}
			if minBytes > 0 && storeSize < minBytes {
				return nil
			}
			matches = append(matches, logMatch{path: path, modTime: info.ModTime(), size: storeSize})
			return nil
		})
		if err != nil {
			if isSkippableDiscoveryError(err) {
				continue
			}
			return nil, fmt.Errorf("discover %s root %q: %w", source.label, root, err)
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	matches = selectTargetSizedRecentMatches(matches, limit)
	candidates := make([]logCandidate, 0, len(matches))
	for _, match := range matches {
		dbPath := match.path
		candidates = append(candidates, logCandidate{
			SourceID:    source.id,
			SourceLabel: source.label,
			Display:     dbPath,
			ModTime:     match.modTime,
			Size:        match.size,
			Read: func() ([]byte, error) {
				return readSQLiteStateAsJSONL(dbPath, source.prefixes, maxBytes)
			},
		})
	}
	return candidates, nil
}

func sqliteStoreSize(path string) (int64, error) {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			if suffix != "" && (errors.Is(err, os.ErrNotExist) || isPermissionError(err)) {
				continue
			}
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

func readSQLiteStateAsJSONL(path string, keyPrefixes []string, maxOutputBytes int64) ([]byte, error) {
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(path))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var output bytes.Buffer
	for _, table := range []string{"ItemTable", "cursorDiskKV"} {
		if err := appendSQLiteKVTable(&output, db, table, keyPrefixes, maxOutputBytes); err != nil {
			return nil, err
		}
	}
	if output.Len() == 0 {
		return []byte("{\"type\":\"message\",\"kind\":\"sqlite_state_empty\"}\n"), nil
	}
	return output.Bytes(), nil
}

func sqliteReadOnlyDSN(path string) string {
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := url.Values{}
	query.Set("mode", "ro")
	query.Set("_pragma", "query_only(1)")
	uri.RawQuery = query.Encode()
	return uri.String()
}

func appendSQLiteKVTable(output *bytes.Buffer, db *sql.DB, table string, keyPrefixes []string, maxOutputBytes int64) error {
	if !sqliteTableExists(db, table) {
		return nil
	}
	where, args := sqlitePrefixWhereClause(keyPrefixes)
	if where == "" {
		return nil
	}
	query := "SELECT key, value FROM " + table + " WHERE " + where + " ORDER BY key LIMIT 500"
	rows, err := db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		keyType := sqliteKeyType(key, keyPrefixes)
		if keyType == "" {
			continue
		}
		text := sqliteValueText(value)
		if strings.TrimSpace(text) == "" {
			continue
		}
		line, err := json.Marshal(map[string]any{
			"type":      "message",
			"kind":      "sqlite_state",
			"key_type":  keyType,
			"content":   text,
			"truncated": len(text) >= maxSQLiteValueTextBytes,
		})
		if err != nil {
			return err
		}
		if maxOutputBytes > 0 && int64(output.Len()+len(line)+1) > maxOutputBytes {
			return nil
		}
		output.Write(line)
		output.WriteByte('\n')
	}
	return rows.Err()
}

func sqlitePrefixWhereClause(prefixes []string) (string, []any) {
	clauses := make([]string, 0, len(prefixes))
	args := make([]any, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		clauses = append(clauses, "key LIKE ? ESCAPE '\\'")
		args = append(args, escapeSQLiteLike(prefix)+"%")
	}
	return strings.Join(clauses, " OR "), args
}

func escapeSQLiteLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}

func sqliteTableExists(db *sql.DB, table string) bool {
	var name string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=? LIMIT 1", table).Scan(&name)
	return err == nil && name == table
}

const maxSQLiteValueTextBytes = 256 * 1024

func sqliteValueText(value []byte) string {
	if len(value) > maxSQLiteValueTextBytes {
		value = value[:maxSQLiteValueTextBytes]
	}
	if !utf8.Valid(value) {
		return ""
	}
	text := strings.TrimSpace(string(value))
	if text == "" {
		return ""
	}
	if len(text)%2 == 0 && looksHex(text) {
		decoded, err := hex.DecodeString(text)
		if err == nil && utf8.Valid(decoded) {
			text = strings.TrimSpace(string(decoded))
		}
	}
	return text
}

func looksHex(value string) bool {
	if len(value) < 8 {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}

func sqliteKeyType(key string, prefixes []string) string {
	lower := strings.ToLower(key)
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			return sanitizeKeyType(prefix)
		}
	}
	return ""
}

func sanitizeKeyType(value string) string {
	value = strings.TrimSuffix(value, ":")
	value = strings.TrimSpace(strings.ToLower(value))
	var out []rune
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out = append(out, r)
			continue
		}
		if r == '_' || r == '-' || r == '.' {
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "state"
	}
	return string(out)
}

func antigravityTranscriptRoots() []string {
	geminiRoot := filepath.Join(homeDir(), ".gemini")
	roots := []string{
		filepath.Join(geminiRoot, "antigravity"),
		filepath.Join(geminiRoot, "antigravity-ide"),
		filepath.Join(appSupportDir("Antigravity"), "User", "workspaceStorage"),
		filepath.Join(appSupportDir("Antigravity"), "User", "globalStorage"),
		filepath.Join(appSupportDir("Antigravity IDE"), "User", "workspaceStorage"),
		filepath.Join(appSupportDir("Antigravity IDE"), "User", "globalStorage"),
	}
	entries, err := os.ReadDir(geminiRoot)
	if err != nil {
		return roots
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if strings.HasPrefix(name, "antigravity") {
			roots = append(roots, filepath.Join(geminiRoot, entry.Name()))
		}
	}
	return roots
}

func claudeDesktopMCPServerName(path string) string {
	base := strings.ToLower(filepath.Base(path))
	if !strings.HasPrefix(base, "mcp-server-") || !strings.HasSuffix(base, ".log") {
		return ""
	}
	name := strings.TrimSuffix(strings.TrimPrefix(base, "mcp-server-"), ".log")
	var out []rune
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			out = append(out, r)
		}
		if len(out) >= 64 {
			break
		}
	}
	return string(out)
}

func appSupportDir(app string) string {
	return appSupportDirFor(runtime.GOOS, homeDir(), appDataDir(), os.Getenv("XDG_CONFIG_HOME"), app)
}

func appSupportDirFor(goos, home, appData, xdgConfig, app string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", app)
	case "windows":
		return filepath.Join(appData, app)
	default:
		config := xdgConfig
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		return filepath.Join(config, app)
	}
}

func appDataDir() string {
	if value := os.Getenv("APPDATA"); value != "" {
		return value
	}
	return filepath.Join(homeDir(), "AppData", "Roaming")
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err == nil {
		return home
	}
	return ""
}

func isPermissionError(err error) bool {
	return errors.Is(err, os.ErrPermission)
}

func isSkippableDiscoveryError(err error) bool {
	return errors.Is(err, os.ErrNotExist) || isPermissionError(err)
}

type logMatch struct {
	path    string
	modTime time.Time
	size    int64
}

func selectTargetSizedRecentMatches(matches []logMatch, limit int) []logMatch {
	if len(matches) == 0 {
		return nil
	}
	bounded := make([]logMatch, 0, len(matches))
	for _, match := range matches {
		if match.size <= targetAutoLogBytes {
			bounded = append(bounded, match)
		}
	}
	if len(bounded) > 0 {
		matches = bounded
	}
	newest := matches[0].modTime
	for _, match := range matches[1:] {
		if match.modTime.After(newest) {
			newest = match.modTime
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		left := largestRecentScore(matches[i], newest)
		right := largestRecentScore(matches[j], newest)
		if left == right {
			if matches[i].modTime.Equal(matches[j].modTime) {
				if matches[i].size == matches[j].size {
					return matches[i].path > matches[j].path
				}
				return matches[i].size > matches[j].size
			}
			return matches[i].modTime.After(matches[j].modTime)
		}
		return left > right
	})

	if limit <= 0 {
		return matches
	}
	poolLimit := min(len(matches), targetSelectionPoolLimit)
	pool := matches[:poolLimit]
	limit = min(limit, len(pool))
	var best selectionCandidate
	var walk func(index int, selected []logMatch, total int64, score float64)
	walk = func(index int, selected []logMatch, total int64, score float64) {
		if len(selected) > 0 {
			candidate := selectionCandidate{matches: append([]logMatch(nil), selected...), total: total, score: score}
			if betterSelection(candidate, best) {
				best = candidate
			}
		}
		if index >= len(pool) || len(selected) >= limit {
			return
		}
		for next := index; next < len(pool); next++ {
			match := pool[next]
			walk(next+1, append(selected, match), total+match.size, score+largestRecentScore(match, newest))
		}
	}
	walk(0, nil, 0, 0)
	if len(best.matches) == 0 {
		return nil
	}
	sort.Slice(best.matches, func(i, j int) bool {
		left := largestRecentScore(best.matches[i], newest)
		right := largestRecentScore(best.matches[j], newest)
		if left == right {
			return best.matches[i].path < best.matches[j].path
		}
		return left > right
	})
	return best.matches
}

type selectionCandidate struct {
	matches []logMatch
	total   int64
	score   float64
}

func betterSelection(candidate selectionCandidate, best selectionCandidate) bool {
	if len(best.matches) == 0 {
		return true
	}
	candidateDistance := absInt64(candidate.total - targetAutoLogBytes)
	bestDistance := absInt64(best.total - targetAutoLogBytes)
	if candidateDistance != bestDistance {
		return candidateDistance < bestDistance
	}
	candidateOvershoots := candidate.total > targetAutoLogBytes
	bestOvershoots := best.total > targetAutoLogBytes
	if candidateOvershoots != bestOvershoots {
		return !candidateOvershoots
	}
	if candidate.score != best.score {
		return candidate.score > best.score
	}
	return len(candidate.matches) < len(best.matches)
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func selectLargestRecentCandidatesPerSource(candidates []logCandidate, limit int) []logCandidate {
	if limit <= 0 || len(candidates) == 0 {
		return candidates
	}
	groups := map[string][]logCandidate{}
	var order []string
	for _, candidate := range candidates {
		if _, ok := groups[candidate.SourceID]; !ok {
			order = append(order, candidate.SourceID)
		}
		groups[candidate.SourceID] = append(groups[candidate.SourceID], candidate)
	}
	var selected []logCandidate
	for _, sourceID := range order {
		group := groups[sourceID]
		newest := group[0].ModTime
		for _, candidate := range group[1:] {
			if candidate.ModTime.After(newest) {
				newest = candidate.ModTime
			}
		}
		sort.Slice(group, func(i, j int) bool {
			left := largestRecentScore(logMatch{path: group[i].Display, modTime: group[i].ModTime, size: group[i].Size}, newest)
			right := largestRecentScore(logMatch{path: group[j].Display, modTime: group[j].ModTime, size: group[j].Size}, newest)
			if left == right {
				if group[i].ModTime.Equal(group[j].ModTime) {
					if group[i].Size == group[j].Size {
						return group[i].Display > group[j].Display
					}
					return group[i].Size > group[j].Size
				}
				return group[i].ModTime.After(group[j].ModTime)
			}
			return left > right
		})
		if len(group) > limit {
			group = group[:limit]
		}
		selected = append(selected, group...)
	}
	return selected
}

func largestRecentScore(match logMatch, newest time.Time) float64 {
	age := newest.Sub(match.modTime)
	if age < 0 {
		age = 0
	}
	decay := math.Exp(-float64(age) / float64(largestRecentHalfLife))
	return float64(match.size) * decay
}

func recentOpenCodeSessions(limit int, maxBytes int64, minBytes int64) ([]logCandidate, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, ".local", "share", "opencode", "storage", "message")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isPermissionError(err) {
			return nil, nil
		}
		return nil, err
	}
	matches := make([]logMatch, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "ses_") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		size, modTime, err := openCodeSessionStats(path)
		if err != nil || size == 0 {
			continue
		}
		if maxBytes > 0 && size > maxBytes {
			continue
		}
		if minBytes > 0 && size < minBytes {
			continue
		}
		matches = append(matches, logMatch{path: path, modTime: modTime, size: size})
	}
	if len(matches) == 0 {
		return nil, nil
	}
	matches = selectTargetSizedRecentMatches(matches, limit)
	candidates := make([]logCandidate, 0, len(matches))
	for _, match := range matches {
		sessionPath := match.path
		sessionID := filepath.Base(sessionPath)
		candidates = append(candidates, logCandidate{
			SourceID:    "opencode",
			SourceLabel: "OpenCode",
			Display:     "opencode session " + sessionID,
			ModTime:     match.modTime,
			Size:        match.size,
			Read: func() ([]byte, error) {
				return readOpenCodeSessionMessages(sessionPath)
			},
		})
	}
	return candidates, nil
}

func openCodeSessionStats(path string) (int64, time.Time, error) {
	var total int64
	var latest time.Time
	var messageIDs []string
	err := filepath.WalkDir(path, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() || strings.ToLower(filepath.Ext(filePath)) != ".json" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		data, err := os.ReadFile(filePath)
		if err == nil {
			if messageID := openCodeMessageID(bytes.TrimSpace(data)); messageID != "" {
				messageIDs = append(messageIDs, messageID)
			}
		}
		return nil
	})
	if err != nil {
		return total, latest, err
	}
	partRoot := openCodePartRootForMessageSession(path)
	for _, messageID := range messageIDs {
		partFiles, err := openCodePartFiles(partRoot, messageID)
		if err != nil {
			return total, latest, err
		}
		for _, part := range partFiles {
			total += part.size
			if part.modTime.After(latest) {
				latest = part.modTime
			}
		}
	}
	return total, latest, err
}

func readOpenCodeSessionMessages(path string) ([]byte, error) {
	var files []logMatch
	err := filepath.WalkDir(path, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() || strings.ToLower(filepath.Ext(filePath)) != ".json" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		files = append(files, logMatch{path: filePath, modTime: info.ModTime(), size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path < files[j].path
		}
		return files[i].modTime.Before(files[j].modTime)
	})
	var output bytes.Buffer
	for _, file := range files {
		data, err := os.ReadFile(file.path)
		if err != nil {
			return nil, err
		}
		trimmed := bytes.TrimSpace(data)
		if len(trimmed) == 0 {
			continue
		}
		output.Write(trimmed)
		output.WriteByte('\n')
		messageID := openCodeMessageID(trimmed)
		if messageID == "" {
			continue
		}
		parts, err := readOpenCodeMessageParts(openCodePartRootForMessageSession(path), messageID)
		if err != nil {
			return nil, err
		}
		for _, part := range parts {
			output.Write(part)
			output.WriteByte('\n')
		}
	}
	return output.Bytes(), nil
}

func openCodePartRootForMessageSession(messageSessionPath string) string {
	storageRoot := filepath.Dir(filepath.Dir(messageSessionPath))
	partRoot := filepath.Join(storageRoot, "part")
	if _, err := os.Stat(partRoot); err != nil {
		return ""
	}
	return partRoot
}

func openCodeMessageID(data []byte) string {
	var decoded map[string]any
	if json.Unmarshal(data, &decoded) != nil {
		return ""
	}
	id, _ := decoded["id"].(string)
	return strings.TrimSpace(id)
}

func readOpenCodeMessageParts(partRoot string, messageID string) ([][]byte, error) {
	files, err := openCodePartFiles(partRoot, messageID)
	if err != nil {
		return nil, err
	}
	parts := make([][]byte, 0, len(files))
	for _, file := range files {
		data, err := os.ReadFile(file.path)
		if err != nil {
			return nil, err
		}
		trimmed := bytes.TrimSpace(data)
		if len(trimmed) > 0 {
			parts = append(parts, trimmed)
		}
	}
	return parts, nil
}

func openCodePartFiles(partRoot string, messageID string) ([]logMatch, error) {
	if partRoot == "" || messageID == "" {
		return nil, nil
	}
	root := filepath.Join(partRoot, messageID)
	var files []logMatch
	err := filepath.WalkDir(root, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() || strings.ToLower(filepath.Ext(filePath)) != ".json" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		files = append(files, logMatch{path: filePath, modTime: info.ModTime(), size: info.Size()})
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path < files[j].path
		}
		return files[i].modTime.Before(files[j].modTime)
	})
	return files, nil
}

func sourceCount(candidates []logCandidate) int {
	seen := map[string]bool{}
	for _, candidate := range candidates {
		seen[candidate.SourceID] = true
	}
	return len(seen)
}

func sourceSummary(candidates []logCandidate) string {
	counts := map[string]int{}
	labels := map[string]string{}
	for _, candidate := range candidates {
		counts[candidate.SourceID]++
		labels[candidate.SourceID] = candidate.SourceLabel
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", labels[key], counts[key]))
	}
	return strings.Join(parts, ", ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func confirmUpload(input io.Reader, output io.Writer) bool {
	fmt.Fprint(output, "Upload only this sanitized report? [y/N] ")
	var answer string
	_, _ = fmt.Fscanln(input, &answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func openBrowser(url string) error {
	if url == "" {
		return nil
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func formatBytesForHelp(bytes int64) string {
	const mb = 1024 * 1024
	if bytes%mb == 0 {
		return fmt.Sprintf("%d MB", bytes/mb)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: agent-analyzer run [--out <path>] [--base-url <url>] [--yes] [--no-open]")
	fmt.Fprintln(os.Stderr, "       agent-analyzer analyze [<log-path>] [--log <path>] [--out <path>] ...")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  run            analyze locally, ask for upload confirmation, upload sanitized JSON, and open the report.")
	fmt.Fprintln(os.Stderr, "  <log-path>     path to a supported JSON/JSONL log; mutually exclusive with --log.")
	fmt.Fprintf(os.Stderr, "                 if neither is supplied, recent logs per source are selected to target about %s total.\n", formatBytesForHelp(targetAutoLogBytes))
	fmt.Fprintln(os.Stderr, "                 currently auto-discovers Claude Code, Claude Desktop, Codex, GitHub Copilot, OpenCode, Claude Desktop MCP, Cursor, Kiro, and Google Antigravity.")
	fmt.Fprintln(os.Stderr, "  --log <path>   explicit log path; mutually exclusive with a positional <log-path>.")
	fmt.Fprintln(os.Stderr, "  --source <id>  source ID for explicit --log or positional analysis; inferred from path when omitted.")
	fmt.Fprintln(os.Stderr, "  --out <path>   output path for the sanitized report JSON (default: ./agent-analyzer-report.json).")
	fmt.Fprintln(os.Stderr, "  --paid         legacy alias: analyze target-sized recent supported logs locally and write a sanitized aggregate report.")
	fmt.Fprintf(os.Stderr, "  --limit <n>    maximum recent logs per source for aggregate modes, capped at %d (default: %d).\n", maxAutoLogLimit, defaultAutoLogLimit)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  agent-analyzer upload <sanitized-report.json> [--base-url https://analyzer.spec-kitty.ai]")
	fmt.Fprintln(os.Stderr, "  agent-analyzer version")
}
