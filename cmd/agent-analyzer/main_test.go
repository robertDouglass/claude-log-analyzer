package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/priivacy-ai/agent-log-analyzer/internal/analyzer"
)

// sampleJSONL is a minimal Claude Code JSONL log fixture used by the CLI
// argument-resolution tests. The content does not need to exercise the
// analyzer deeply; it only needs to parse cleanly.
const sampleJSONL = `{"type":"user","message":"hello"}
{"type":"assistant","message":"world"}
`

// writeSampleLog drops a small JSONL fixture into the given dir and returns
// the absolute path.
func writeSampleLog(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "sample.jsonl")
	writeLogContent(t, path, sampleJSONL)
	return path
}

func writeMeaningfulLog(t *testing.T, path string) {
	t.Helper()
	var builder strings.Builder
	for builder.Len() < freeAutoMinLogBytes+1024 {
		builder.WriteString(sampleJSONL)
	}
	writeLogContent(t, path, builder.String())
}

func writeCopilotChatSession(t *testing.T, path string, minBytes int) {
	t.Helper()
	padding := ""
	for len(padding) < minBytes {
		padding += " context"
	}
	content := `{
  "sessionId": "session-secret",
  "customTitle": "GitHub Copilot chat",
  "requests": [
    {
      "message": {"text": "fix private@example.com ` + padding + `"},
      "response": [{"value": "I will inspect the failing test."}],
      "usage": {"input_tokens": 1200, "cached_input_tokens": 200, "output_tokens": 80}
    },
    {
      "message": "run tests",
      "toolInvocation": {
        "kind": "toolInvocation",
        "toolName": "terminal",
        "callId": "call-secret",
        "input": {"command": "cat /Users/private/repo/.env"}
      },
      "response": [
        {
          "kind": "toolInvocationResult",
          "toolName": "terminal",
          "callId": "call-secret",
          "output": "oauth-refresh-token"
        }
      ]
    }
  ]
}`
	writeLogContent(t, path, content)
}

func writeLogContent(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir sample log parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write sample log: %v", err)
	}
}

func snapshotDirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir entries: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func snapshotFileModTimes(t *testing.T, paths ...string) map[string]int64 {
	t.Helper()
	modTimes := map[string]int64{}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		modTimes[filepath.Base(path)] = info.ModTime().UnixNano()
	}
	return modTimes
}

func existingPaths(paths ...string) []string {
	var out []string
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			out = append(out, path)
		}
	}
	return out
}

func isolatedDiscoveryHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))
	t.Setenv("TMPDIR", filepath.Join(home, "tmp"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("COPILOT_HOME", filepath.Join(home, ".copilot"))
	t.Setenv("KIRO_HOME", filepath.Join(home, ".kiro"))
	t.Setenv("KIRO_CHAT_LOG_FILE", "")
	for _, dir := range []string{os.Getenv("TMPDIR"), os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("XDG_CONFIG_HOME"), os.Getenv("APPDATA")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir isolated discovery dir %s: %v", dir, err)
		}
	}
	return home
}

func withDefaultDiscoveryShim(t *testing.T, path string) {
	t.Helper()
	original := defaultSupportedLogsFn
	defaultSupportedLogsFn = func() ([]logCandidate, error) {
		return []logCandidate{fileCandidate("claude_code", "Claude Code", path)}, nil
	}
	t.Cleanup(func() { defaultSupportedLogsFn = original })
}

func withRecentShim(t *testing.T, candidates []logCandidate) {
	t.Helper()
	original := recentSupportedLogsFn
	recentSupportedLogsFn = func(limit int) ([]logCandidate, error) {
		if limit > 0 && len(candidates) > limit {
			return candidates[:limit], nil
		}
		return candidates, nil
	}
	t.Cleanup(func() { recentSupportedLogsFn = original })
}

func fileCandidate(sourceID, sourceLabel, path string) logCandidate {
	return logCandidate{
		SourceID:    sourceID,
		SourceLabel: sourceLabel,
		Display:     path,
	}
}

func assertReportDoesNotContain(t *testing.T, report analyzer.Report, forbidden ...string) {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	serialized := string(data)
	for _, value := range forbidden {
		if strings.Contains(serialized, value) {
			t.Fatalf("report leaked %q in %s", value, serialized)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestAnalyze_NoArgs_UsesDefaultDiscovery(t *testing.T) {
	dir := t.TempDir()
	logPath := writeSampleLog(t, dir)
	outPath := filepath.Join(dir, "report.json")
	withDefaultDiscoveryShim(t, logPath)

	err := runAnalyze([]string{"--out", outPath})
	if err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("expected report at %s: %v", outPath, err)
	}
}

func TestAnalyze_ExplicitSourceUsesSourceSpecificNormalizer(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "codex.jsonl")
	logContent := `{"type":"event_msg","payload":{"msg":{"type":"token_count","last_token_usage":{"input_tokens":4321,"cached_input_tokens":123,"output_tokens":55}}}}` + "\n"
	writeLogContent(t, logPath, logContent)
	outPath := filepath.Join(dir, "report.json")

	if err := runAnalyze([]string{"--source", "codex", "--log", logPath, "--out", outPath}); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var report analyzer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if report.AnalysisSignals.InputTokens != 4321 || report.AnalysisSignals.CacheReadTokens != 123 || report.AnalysisSignals.OutputTokens != 55 {
		t.Fatalf("expected Codex source-specific token signals, got %#v", report.AnalysisSignals)
	}
	if len(report.SourceReports) != 1 || len(report.SourceReports[0].LogRefs) != 1 {
		t.Fatalf("expected single source log ref, got %#v", report.SourceReports)
	}
	wantHash := contentHashSHA256([]byte(logContent))
	if got := report.SourceReports[0].LogRefs[0].ContentHashSHA256; got != wantHash {
		t.Fatalf("expected content hash %s, got %s", wantHash, got)
	}
	assertReportDoesNotContain(t, report, logPath, filepath.Base(logPath))
}

func TestAnalyze_ExplicitCopilotPathUsesSessionReader(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "workspaceStorage", "workspace-a", "chatSessions", "copilot-session.json")
	writeCopilotChatSession(t, logPath, 0)
	outPath := filepath.Join(dir, "report.json")

	if err := runAnalyze([]string{logPath, "--out", outPath}); err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var report analyzer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if report.AnalysisSignals.ToolCallCount != 1 || report.AnalysisSignals.ToolResultCount != 1 {
		t.Fatalf("expected exact Copilot tool signals, got %#v", report.AnalysisSignals)
	}
	if report.AnalysisSignals.InputTokens != 1200 || report.AnalysisSignals.CacheReadTokens != 200 || report.AnalysisSignals.OutputTokens != 80 {
		t.Fatalf("expected Copilot token signals, got %#v", report.AnalysisSignals)
	}
	assertReportDoesNotContain(t, report, "private@example.com", "/Users/private/repo", "oauth-refresh-token", "session-secret")
}

func TestAnalyze_RejectsUnknownExplicitSource(t *testing.T) {
	dir := t.TempDir()
	logPath := writeSampleLog(t, dir)
	outPath := filepath.Join(dir, "report.json")
	err := runAnalyze([]string{"--source", "not_a_source", "--log", logPath, "--out", outPath})
	if err == nil || !strings.Contains(err.Error(), "unknown --source") {
		t.Fatalf("expected unknown source error, got %v", err)
	}
}

func TestAnalyze_NoArgs_UsesMultiplePerSupportedSource(t *testing.T) {
	dir := t.TempDir()
	claude := writeSampleLog(t, dir)
	claude2 := filepath.Join(dir, "claude-2.jsonl")
	codex := filepath.Join(dir, "codex.jsonl")
	codex2 := filepath.Join(dir, "codex-2.jsonl")
	opencode := filepath.Join(dir, "opencode.json")
	opencode2 := filepath.Join(dir, "opencode-2.json")
	for _, path := range []string{claude2, codex, codex2, opencode, opencode2} {
		if err := os.WriteFile(path, []byte(sampleJSONL), 0o600); err != nil {
			t.Fatalf("write source log: %v", err)
		}
	}
	outPath := filepath.Join(dir, "report.json")
	original := defaultSupportedLogsFn
	defaultSupportedLogsFn = func() ([]logCandidate, error) {
		return []logCandidate{
			fileCandidate("claude_code", "Claude Code", claude),
			fileCandidate("claude_code", "Claude Code", claude2),
			fileCandidate("codex", "Codex", codex),
			fileCandidate("codex", "Codex", codex2),
			fileCandidate("opencode", "OpenCode", opencode),
			fileCandidate("opencode", "OpenCode", opencode2),
		}, nil
	}
	t.Cleanup(func() { defaultSupportedLogsFn = original })

	err := runAnalyze([]string{"--out", outPath})
	if err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var report analyzer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("report is not JSON: %v", err)
	}
	if report.AggregateEvent.ParserType != "multi_source" {
		t.Fatalf("expected multi_source parser type, got %#v", report.AggregateEvent)
	}
	if report.Metrics.SessionCount != 6 {
		t.Fatalf("expected multiple sessions per source, got %#v", report.Metrics)
	}
	if len(report.SourceReports) != 3 {
		t.Fatalf("expected source reports for three sources, got %#v", report.SourceReports)
	}
	if report.SourceReports[0].SourceID != "claude_code" || report.SourceReports[1].SourceID != "codex" || report.SourceReports[2].SourceID != "opencode" {
		t.Fatalf("expected source reports to preserve discovery order, got %#v", report.SourceReports)
	}
}

func TestSafeAnalyzedLogRefDoesNotHashLocalPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace-secret", "rollout-thread-secret.jsonl")
	ref := safeAnalyzedLogRef(logCandidate{SourceID: "codex", SourceLabel: "Codex", Display: path}, 7, 4096, contentHashSHA256([]byte("stable log bytes")))
	sum := sha256.Sum256([]byte("codex" + "\x00" + path))
	forbiddenPrefix := hex.EncodeToString(sum[:])[:10]
	serialized, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("marshal ref: %v", err)
	}
	if strings.Contains(string(serialized), forbiddenPrefix) || strings.Contains(string(serialized), "workspace-secret") || strings.Contains(string(serialized), "thread-secret") {
		t.Fatalf("local ref leaked path-derived material: %s", serialized)
	}
	if ref.LocalRef != "codex-log-7" {
		t.Fatalf("expected ordinal-only local ref, got %#v", ref)
	}
	if ref.ContentHashSHA256 != contentHashSHA256([]byte("stable log bytes")) {
		t.Fatalf("expected content hash to be preserved, got %#v", ref)
	}
}

func TestBuildSourceReports_CombinesSameSourceTimelinesCumulatively(t *testing.T) {
	reports := buildSourceReports([]sourceAnalysisResult{
		{
			Candidate: fileCandidate("opencode", "OpenCode", "/tmp/opencode-1.jsonl"),
			Report: analyzer.Report{
				Metrics: analyzer.Metrics{Turns: 20, EstimatedTokens: 1000, ToolOutputTokens: 100, Rereads: 1, FailedCommands: 2},
				Timeline: []analyzer.TimelinePoint{
					{Turn: 10, EstimatedTokens: 400, ToolTokens: 40, Rereads: 1, Retries: 1},
					{Turn: 20, EstimatedTokens: 1000, ToolTokens: 100, Rereads: 1, Retries: 2},
				},
			},
			Bytes: 100,
		},
		{
			Candidate: fileCandidate("opencode", "OpenCode", "/tmp/opencode-2.jsonl"),
			Report: analyzer.Report{
				Metrics: analyzer.Metrics{Turns: 15, EstimatedTokens: 700, ToolOutputTokens: 80, Rereads: 2, FailedCommands: 1},
				Timeline: []analyzer.TimelinePoint{
					{Turn: 10, EstimatedTokens: 500, ToolTokens: 50, Rereads: 1, Retries: 1},
					{Turn: 15, EstimatedTokens: 700, ToolTokens: 80, Rereads: 2, Retries: 1},
				},
			},
			Bytes: 100,
		},
	})

	if len(reports) != 1 {
		t.Fatalf("expected one source report, got %#v", reports)
	}
	got := reports[0].Timeline
	want := []analyzer.TimelinePoint{
		{Turn: 10, EstimatedTokens: 400, ToolTokens: 40, Rereads: 1, Retries: 1},
		{Turn: 20, EstimatedTokens: 1000, ToolTokens: 100, Rereads: 1, Retries: 2},
		{Turn: 30, EstimatedTokens: 1500, ToolTokens: 150, Rereads: 2, Retries: 3},
		{Turn: 35, EstimatedTokens: 1700, ToolTokens: 180, Rereads: 3, Retries: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected timeline length: got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("timeline[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestRecentSupportedLogs_LimitsPerSource(t *testing.T) {
	home := isolatedDiscoveryHome(t)
	claudeRoot := filepath.Join(home, ".claude", "projects", "repo")
	codexRoot := filepath.Join(home, ".codex", "sessions", "2026")
	if err := os.MkdirAll(claudeRoot, 0o700); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	if err := os.MkdirAll(codexRoot, 0o700); err != nil {
		t.Fatalf("mkdir codex: %v", err)
	}
	paths := []string{
		filepath.Join(claudeRoot, "old.jsonl"),
		filepath.Join(claudeRoot, "new.jsonl"),
		filepath.Join(codexRoot, "old.jsonl"),
		filepath.Join(codexRoot, "new.jsonl"),
	}
	for index, path := range paths {
		writeMeaningfulLog(t, path)
		mtime := time.Unix(int64(100+index), 0)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	candidates, err := recentSupportedLogs(1)
	if err != nil {
		t.Fatalf("recentSupportedLogs: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected one candidate per file-backed source, got %#v", candidates)
	}
	if candidates[0].SourceID != "claude_code" || filepath.Base(candidates[0].Display) != "new.jsonl" {
		t.Fatalf("expected newest Claude log first, got %#v", candidates[0])
	}
	if candidates[1].SourceID != "codex" || filepath.Base(candidates[1].Display) != "new.jsonl" {
		t.Fatalf("expected newest Codex log second, got %#v", candidates[1])
	}
}

func TestRecentSupportedLogs_AppliesFinalLimitPerSource(t *testing.T) {
	isolatedDiscoveryHome(t)
	writeLogContent(t, filepath.Join(appSupportDir("Kiro"), "logs", "main.log"), strings.Repeat(`{"tool_name":"Bash","tool_input":{"command":"npm test"}}`+"\n", 80))
	writeLogContent(t, filepath.Join(appSupportDir("Kiro"), "User", "globalStorage", "kiro.kiroagent", "workspace-sessions", "workspace-a", "session-1.json"), strings.Repeat(`{"history":[{"hook_event_name":"PreToolUse","tool_name":"Bash"}]}`+"\n", 80))

	candidates, err := recentSupportedLogs(1)
	if err != nil {
		t.Fatalf("recentSupportedLogs: %v", err)
	}
	counts := map[string]int{}
	for _, candidate := range candidates {
		counts[candidate.SourceID]++
	}
	if counts["kiro_ide"] != 1 {
		t.Fatalf("expected final one-per-source cap for Kiro IDE, got %#v", candidates)
	}
}

func TestRecentSupportedLogs_DiscoversDesktopAndAgentSources(t *testing.T) {
	home := isolatedDiscoveryHome(t)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))

	claudeConfig := filepath.Join(home, "claude-config")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeConfig)
	writeMeaningfulLog(t, filepath.Join(claudeConfig, "projects", "repo", "claude.jsonl"))

	codexHome := filepath.Join(home, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	writeMeaningfulLog(t, filepath.Join(codexHome, "sessions", "2026", "05", "21", "rollout-2026-05-21T19-00-00-thread.jsonl"))

	writeLogContent(t, filepath.Join(appSupportDir("Claude"), "local-agent-mode-sessions", "org", "workspace", "local_session.json"), strings.Repeat(`{"sessionId":"session-secret","initialMessage":"fix private@example.com","enabledMcpTools":{"local:Claude Code:Bash":true},"history":[{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"go test ./..."}}]}`+"\n", 80))
	writeMeaningfulLog(t, filepath.Join(home, ".cursor", "projects", "repo", "agent-transcripts", "session", "transcript.jsonl"))
	writeMeaningfulLog(t, filepath.Join(home, ".gemini", "antigravity-ide", "brain", "task", ".system_generated", "logs", "transcript.jsonl"))
	writeCopilotChatSession(t, filepath.Join(appSupportDir("Code"), "User", "workspaceStorage", "workspace-a", "chatSessions", "copilot-session.json"), freeAutoMinLogBytes+1024)

	kiroCLI := filepath.Join(home, "kiro-log", "kiro-chat.log")
	t.Setenv("KIRO_CHAT_LOG_FILE", kiroCLI)
	writeLogContent(t, kiroCLI, strings.Repeat(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"go test ./..."}}`+"\n", 80))

	switch runtime.GOOS {
	case "darwin":
		writeLogContent(t, filepath.Join(home, "Library", "Logs", "Claude", "mcp.log"), strings.Repeat(`2026-05-21T19:00:00 {"jsonrpc":"2.0","method":"tools/call","params":{"name":"filesystem"}}`+"\n", 80))
		writeLogContent(t, filepath.Join(home, "Library", "Application Support", "Kiro", "logs", "20260521", "window1", "KiroLLMLogs.log"), strings.Repeat(`2026-05-21 19:00:00.000 [info] {"tool_name":"Bash","tool_input":{"command":"npm test"}}`+"\n", 80))
	case "windows":
		writeLogContent(t, filepath.Join(home, "AppData", "Roaming", "Claude", "logs", "mcp.log"), strings.Repeat(`2026-05-21T19:00:00 {"jsonrpc":"2.0","method":"tools/call","params":{"name":"filesystem"}}`+"\n", 80))
		writeLogContent(t, filepath.Join(home, "AppData", "Roaming", "Kiro", "logs", "20260521", "window1", "KiroLLMLogs.log"), strings.Repeat(`2026-05-21 19:00:00.000 [info] {"tool_name":"Bash","tool_input":{"command":"npm test"}}`+"\n", 80))
	default:
		writeLogContent(t, filepath.Join(home, ".config", "Kiro", "logs", "20260521", "window1", "KiroLLMLogs.log"), strings.Repeat(`2026-05-21 19:00:00.000 [info] {"tool_name":"Bash","tool_input":{"command":"npm test"}}`+"\n", 80))
	}

	candidates, err := recentSupportedLogs(1)
	if err != nil {
		t.Fatalf("recentSupportedLogs: %v", err)
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		seen[candidate.SourceID] = true
	}
	for _, want := range []string{"claude_code", "claude_desktop", "codex", "copilot", "cursor", "kiro_cli", "kiro_ide", "antigravity"} {
		if !seen[want] {
			t.Fatalf("missing source %s from candidates %#v", want, candidates)
		}
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if !seen["claude_desktop_mcp"] {
			t.Fatalf("missing Claude Desktop MCP candidate from %#v", candidates)
		}
	}
}

func TestRecentCopilotLogs_DiscoversCLIAndVSCodeSessions(t *testing.T) {
	home := isolatedDiscoveryHome(t)
	cliEvents := filepath.Join(home, ".copilot", "session-state", "session-1", "events.jsonl")
	writeLogContent(t, cliEvents, `{"type":"message","content":"GitHub Copilot CLI session"}`+"\n"+strings.Repeat(`{"type":"tool_call","tool":"terminal","arguments":{"command":"go test ./..."}}`+"\n", 80))
	vscodeSession := filepath.Join(appSupportDir("Code"), "User", "workspaceStorage", "workspace-a", "chatSessions", "copilot-session.json")
	writeCopilotChatSession(t, vscodeSession, freeAutoMinLogBytes+1024)

	candidates, err := recentSupportedLogs(2)
	if err != nil {
		t.Fatalf("recentSupportedLogs: %v", err)
	}
	var copilotCandidates []logCandidate
	for _, candidate := range candidates {
		if candidate.SourceID == "copilot" {
			copilotCandidates = append(copilotCandidates, candidate)
		}
	}
	if len(copilotCandidates) != 2 {
		t.Fatalf("expected Copilot CLI and VS Code candidates, got %#v", candidates)
	}
	for _, candidate := range copilotCandidates {
		data, err := candidate.readBytes()
		if err != nil {
			t.Fatalf("read Copilot candidate %s: %v", candidate.Display, err)
		}
		report, err := analyzer.AnalyzeForSource("copilot-test", "copilot", data)
		if err != nil {
			t.Fatalf("AnalyzeForSource: %v", err)
		}
		if !containsString(report.Ecosystem.CodingAgents, "copilot") {
			t.Fatalf("expected Copilot ecosystem detection, got %#v", report.Ecosystem.CodingAgents)
		}
	}
}

func TestCopilotVSCodeSessionReader_NormalizesToolsAndDoesNotLeakReportData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "copilot-session.json")
	writeCopilotChatSession(t, path, 0)
	data, err := readCopilotJSONSession(path)
	if err != nil {
		t.Fatalf("readCopilotJSONSession: %v", err)
	}
	if !strings.Contains(string(data), `"type":"tool_call"`) || !strings.Contains(string(data), `"type":"tool_result"`) {
		t.Fatalf("expected Copilot tool rows, got %s", data)
	}
	report, err := analyzer.AnalyzeForSource("copilot-session-test", "copilot", data)
	if err != nil {
		t.Fatalf("AnalyzeForSource: %v", err)
	}
	if report.AnalysisSignals.ToolCallCount != 1 || report.AnalysisSignals.ToolResultCount != 1 {
		t.Fatalf("expected Copilot tool signals, got %#v", report.AnalysisSignals)
	}
	assertReportDoesNotContain(t, report, "private@example.com", "/Users/private/repo", "oauth-refresh-token", "session-secret")
}

func TestCrossPlatformSourceRootHelpers(t *testing.T) {
	home := filepath.Join("Users", "agent")
	appData := filepath.Join("Users", "agent", "AppData", "Roaming")
	xdgConfig := filepath.Join("home", "agent", ".config")
	tempDir := filepath.Join("tmp", "agent")
	runtimeDir := filepath.Join("run", "user", "1000")
	kiroHome := filepath.Join("home", "agent", ".kiro")

	if got := appSupportDirFor("darwin", home, appData, xdgConfig, "Cursor"); got != filepath.Join(home, "Library", "Application Support", "Cursor") {
		t.Fatalf("darwin app support = %s", got)
	}
	if got := appSupportDirFor("windows", home, appData, xdgConfig, "Cursor"); got != filepath.Join(appData, "Cursor") {
		t.Fatalf("windows app support = %s", got)
	}
	if got := appSupportDirFor("linux", home, appData, xdgConfig, "Cursor"); got != filepath.Join(xdgConfig, "Cursor") {
		t.Fatalf("linux app support = %s", got)
	}
	if got := claudeDesktopLogRootsFor("windows", home, appData); len(got) != 1 || got[0] != filepath.Join(appData, "Claude", "logs") {
		t.Fatalf("windows Claude roots = %#v", got)
	}
	if got := kiroCLILogRootsFor("windows", tempDir, runtimeDir, kiroHome); len(got) != 2 || got[0] != filepath.Join(kiroHome, "logs") || got[1] != filepath.Join(tempDir, "kiro-log", "logs") {
		t.Fatalf("windows Kiro CLI roots = %#v", got)
	}
	if got := kiroCLILogRootsFor("linux", tempDir, runtimeDir, kiroHome); len(got) != 2 || got[1] != filepath.Join(runtimeDir, "kiro-log") {
		t.Fatalf("linux Kiro CLI roots = %#v", got)
	}
	if got := kiroCLILogRootsFor("linux", tempDir, "", kiroHome); len(got) != 2 || got[1] != filepath.Join(tempDir, "kiro-log") {
		t.Fatalf("linux Kiro CLI empty runtime fallback = %#v", got)
	}
}

func TestRecentSupportedLogs_DiscoversExactKiroChatLogFile(t *testing.T) {
	home := isolatedDiscoveryHome(t)
	customLog := filepath.Join(home, "custom-kiro-chat.jsonl")
	t.Setenv("KIRO_CHAT_LOG_FILE", customLog)
	writeLogContent(t, customLog, strings.Repeat(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"go test ./..."}}`+"\n", 80))

	candidates, err := recentSupportedLogs(1)
	if err != nil {
		t.Fatalf("recentSupportedLogs: %v", err)
	}
	if len(candidates) != 1 || candidates[0].SourceID != "kiro_cli" || candidates[0].Display != customLog {
		t.Fatalf("expected exact Kiro chat log candidate, got %#v", candidates)
	}
}

func TestRecentSupportedLogs_DiscoversAppSupportTranscripts(t *testing.T) {
	isolatedDiscoveryHome(t)
	cursorTranscript := filepath.Join(appSupportDir("Cursor"), "User", "workspaceStorage", "repo", "agent-transcripts", "session.jsonl")
	antigravityTranscript := filepath.Join(appSupportDir("Antigravity"), "User", "workspaceStorage", "repo", "transcript.jsonl")
	writeLogContent(t, cursorTranscript, strings.Repeat(`{"tool":"terminal","arguments":{"command":"go test ./..."}}`+"\n", 80))
	writeLogContent(t, antigravityTranscript, strings.Repeat(`{"type":"terminal_command","command":"go test ./..."}`+"\n", 80))

	candidates, err := recentSupportedLogs(1)
	if err != nil {
		t.Fatalf("recentSupportedLogs: %v", err)
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		seen[candidate.SourceID] = true
	}
	for _, want := range []string{"cursor", "antigravity"} {
		if !seen[want] {
			t.Fatalf("missing %s app support transcript from %#v", want, candidates)
		}
	}
}

func TestRecentCodexLogs_PrefersSessionIndex(t *testing.T) {
	home := isolatedDiscoveryHome(t)
	codexHome := filepath.Join(home, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	indexed := filepath.Join(codexHome, "sessions", "2026", "05", "25", "rollout-indexed.jsonl")
	fallback := filepath.Join(codexHome, "sessions", "2026", "05", "25", "rollout-fallback.jsonl")
	writeMeaningfulLog(t, indexed)
	writeMeaningfulLog(t, fallback)
	writeLogContent(t, filepath.Join(codexHome, "session_index.jsonl"), `{"session_path":"sessions/2026/05/25/rollout-indexed.jsonl","cwd":"/Users/private/repo"}`+"\n")

	candidates, err := recentCodexLogs(1, 0, 1)
	if err != nil {
		t.Fatalf("recentCodexLogs: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Display != indexed {
		t.Fatalf("expected indexed Codex candidate, got %#v", candidates)
	}
}

func TestRecentClaudeDesktopLogs_DiscoversSessionAndAudit(t *testing.T) {
	isolatedDiscoveryHome(t)
	sessionPath := filepath.Join(appSupportDir("Claude"), "local-agent-mode-sessions", "org", "workspace", "local_session.json")
	auditPath := filepath.Join(appSupportDir("Claude"), "audit.jsonl")
	writeLogContent(t, sessionPath, strings.Repeat(`{"initialMessage":"fix private@example.com","history":[{"hook_event_name":"PreToolUse","tool_name":"Bash"}]}`+"\n", 80))
	writeLogContent(t, auditPath, strings.Repeat(`{"method":"tools/call","params":{"name":"filesystem","arguments":{"path":"/Users/private/repo"}}}`+"\n", 80))

	candidates, err := recentPathLogs("claude_desktop", "Claude Desktop", claudeDesktopSessionRoots(), acceptClaudeDesktopSession, 10, 0, 1)
	if err != nil {
		t.Fatalf("recentPathLogs: %v", err)
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		seen[filepath.Base(candidate.Display)] = true
	}
	if !seen["local_session.json"] || !seen["audit.jsonl"] {
		t.Fatalf("expected session and audit candidates, got %#v", candidates)
	}
	if source := inferSourceFromPath(auditPath); source != "claude_desktop" {
		t.Fatalf("expected audit path to infer claude_desktop, got %q", source)
	}
}

func TestRecentCodexSQLiteLogs_ReadsDiagnosticsWithoutSourceWrites(t *testing.T) {
	home := isolatedDiscoveryHome(t)
	codexHome := filepath.Join(home, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	dbPath := filepath.Join(codexHome, "logs_2.sqlite")
	writeCodexLogsDB(t, dbPath)
	beforeEntries := snapshotDirEntries(t, filepath.Dir(dbPath))
	beforeModTimes := snapshotFileModTimes(t, dbPath)

	candidates, err := recentCodexSQLiteLogs(1, 0, 1)
	if err != nil {
		t.Fatalf("recentCodexSQLiteLogs: %v", err)
	}
	if len(candidates) != 1 || candidates[0].SourceID != "codex" {
		t.Fatalf("expected Codex SQLite candidate, got %#v", candidates)
	}
	data, err := candidates[0].readBytes()
	if err != nil {
		t.Fatalf("read Codex SQLite: %v", err)
	}
	afterEntries := snapshotDirEntries(t, filepath.Dir(dbPath))
	if !reflect.DeepEqual(beforeEntries, afterEntries) {
		t.Fatalf("Codex SQLite read changed source directory entries: before=%#v after=%#v", beforeEntries, afterEntries)
	}
	afterModTimes := snapshotFileModTimes(t, dbPath)
	if !reflect.DeepEqual(beforeModTimes, afterModTimes) {
		t.Fatalf("Codex SQLite read changed source file mtimes: before=%#v after=%#v", beforeModTimes, afterModTimes)
	}
	report, err := analyzer.AnalyzeForSource("codex-sqlite-test", "codex", data)
	if err != nil {
		t.Fatalf("AnalyzeForSource: %v", err)
	}
	if report.Metrics.FailedCommands == 0 {
		t.Fatalf("expected diagnostic error signal, got %#v", report.Metrics)
	}
	assertReportDoesNotContain(t, report, "/Users/private/repo", "sk-test-secret")
}

func TestCodexSQLiteLogs_ToleratesMinimalSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE logs (level TEXT, message TEXT)`); err != nil {
		t.Fatalf("create logs: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO logs(level, message) VALUES ('ERROR', 'failed with /Users/private/repo and sk-test-secret')`); err != nil {
		t.Fatalf("insert log: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	data, err := readCodexSQLiteLogsAsJSONL(dbPath, 0)
	if err != nil {
		t.Fatalf("read Codex SQLite: %v", err)
	}
	report, err := analyzer.AnalyzeForSource("codex-sqlite-minimal-test", "codex", data)
	if err != nil {
		t.Fatalf("AnalyzeForSource: %v", err)
	}
	if report.Metrics.FailedCommands == 0 {
		t.Fatalf("expected diagnostic error signal, got %#v", report.Metrics)
	}
	assertReportDoesNotContain(t, report, "/Users/private/repo", "sk-test-secret")
}

func TestClaudeDesktopMCPServerLogAddsBoundedServerEvidence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp-server-github.log")
	writeLogContent(t, path, `2026-05-21T19:00:00 {"jsonrpc":"2.0","method":"tools/call","params":{"name":"create_issue"}}`)

	read := candidateReadFunc("claude_desktop_mcp", path)
	if read == nil {
		t.Fatal("expected custom Claude Desktop MCP reader")
	}
	data, err := read()
	if err != nil {
		t.Fatalf("read custom MCP log: %v", err)
	}
	if !strings.Contains(string(data), "Available MCP servers:") || !strings.Contains(string(data), "github") {
		t.Fatalf("expected bounded MCP server evidence header, got %s", data)
	}
}

func TestRecentSupportedLogs_SkipsPermissionDeniedDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod permission-denied semantics differ on Windows")
	}
	home := isolatedDiscoveryHome(t)
	denied := filepath.Join(home, ".cursor", "projects", "denied")
	if err := os.MkdirAll(denied, 0o700); err != nil {
		t.Fatalf("mkdir denied: %v", err)
	}
	if err := os.Chmod(denied, 0); err != nil {
		t.Fatalf("chmod denied: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(denied, 0o700) })
	if _, err := recentSupportedLogs(1); err == nil || !strings.Contains(err.Error(), "no supported agent logs") {
		t.Fatalf("expected no supported logs error without permission failure, got %v", err)
	}
}

func TestAnalyzeDiscovered_SkipsUnreadableCandidates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod permission-denied semantics differ on Windows")
	}
	dir := t.TempDir()
	unreadable := filepath.Join(dir, "unreadable.jsonl")
	readable := filepath.Join(dir, "readable.jsonl")
	writeLogContent(t, unreadable, sampleJSONL)
	writeLogContent(t, readable, sampleJSONL)
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatalf("chmod unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })

	out := filepath.Join(dir, "report.json")
	err := analyzeDiscovered([]logCandidate{
		fileCandidate("codex", "Codex", unreadable),
		fileCandidate("codex", "Codex", readable),
	}, out, "", false)
	if err != nil {
		t.Fatalf("analyzeDiscovered should skip unreadable candidate: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var report analyzer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if strings.Contains(string(data), "unreadable") {
		t.Fatalf("report should not contain unreadable candidate details: %s", data)
	}
}

func TestRecentKiroWorkspaceSessions_ReadsSessionJSON(t *testing.T) {
	isolatedDiscoveryHome(t)
	sessionPath := filepath.Join(appSupportDir("Kiro"), "User", "globalStorage", "kiro.kiroagent", "workspace-sessions", "workspace-a", "session-1.json")
	writeLogContent(t, sessionPath, `{"sessionId":"session-secret","history":[{"role":"user","content":"run tests for private@example.com"},{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"aws sts get-caller-identity"}},{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_response":"arn:aws:iam::123456789012:user/private"}]}`)

	candidates, err := recentKiroWorkspaceSessions(1, 0, 1)
	if err != nil {
		t.Fatalf("recentKiroWorkspaceSessions: %v", err)
	}
	if len(candidates) != 1 || candidates[0].SourceID != "kiro_ide" {
		t.Fatalf("expected Kiro session candidate, got %#v", candidates)
	}
	data, err := candidates[0].readBytes()
	if err != nil {
		t.Fatalf("read Kiro session: %v", err)
	}
	report, err := analyzer.AnalyzeForSource("kiro-session-test", "kiro_ide", data)
	if err != nil {
		t.Fatalf("AnalyzeForSource: %v", err)
	}
	if report.AnalysisSignals.ToolCallCount != 1 || report.AnalysisSignals.ToolResultCount != 1 {
		t.Fatalf("expected Kiro nested session tool signals, got %#v", report.AnalysisSignals)
	}
	assertReportDoesNotContain(t, report, "private@example.com", "aws sts get-caller-identity", "session-secret", "arn:aws:iam::123456789012:user/private")
}

func TestSQLiteSourceExtraction_ReadsStateDBByDefaultWithoutSourceWrites(t *testing.T) {
	isolatedDiscoveryHome(t)
	dbPath := filepath.Join(appSupportDir("Cursor"), "User", "workspaceStorage", "abc123", "state.vscdb")
	writeCursorStateDB(t, dbPath)
	beforeEntries := snapshotDirEntries(t, filepath.Dir(dbPath))
	beforeModTimes := snapshotFileModTimes(t, dbPath)

	candidates, err := recentSupportedLogs(1)
	if err != nil {
		t.Fatalf("recentSupportedLogs: %v", err)
	}
	var cursorCandidate *logCandidate
	for i := range candidates {
		if candidates[i].SourceID == "cursor" && strings.Contains(candidates[i].SourceLabel, "SQLite") {
			cursorCandidate = &candidates[i]
			break
		}
	}
	if cursorCandidate == nil {
		t.Fatalf("expected Cursor SQLite candidate, got %#v", candidates)
	}
	data, err := cursorCandidate.readBytes()
	if err != nil {
		t.Fatalf("read SQLite candidate: %v", err)
	}
	if !strings.Contains(string(data), `"key_type":"bubbleid"`) || strings.Contains(string(data), "bubbleId:composer-secret") {
		t.Fatalf("expected bounded key type without raw DB key, got %s", data)
	}
	afterEntries := snapshotDirEntries(t, filepath.Dir(dbPath))
	if !reflect.DeepEqual(beforeEntries, afterEntries) {
		t.Fatalf("SQLite read changed source directory entries: before=%#v after=%#v", beforeEntries, afterEntries)
	}
	afterModTimes := snapshotFileModTimes(t, dbPath)
	if !reflect.DeepEqual(beforeModTimes, afterModTimes) {
		t.Fatalf("SQLite read changed source file mtimes: before=%#v after=%#v", beforeModTimes, afterModTimes)
	}
	report, err := analyzer.AnalyzeForSource("cursor-sqlite-test", "cursor", data)
	if err != nil {
		t.Fatalf("AnalyzeForSource: %v", err)
	}
	if report.AnalysisSignals.ToolCallCount != 1 {
		t.Fatalf("expected Cursor SQLite stringified JSON tool call, got %#v", report.AnalysisSignals)
	}
	assertReportDoesNotContain(t, report, "private@example.com", "arn:aws:iam::123456789012:user/private", "oauth-refresh-token", "composer-secret")
}

func TestSQLiteSourceExtraction_ReadsLegacyCursorKeys(t *testing.T) {
	isolatedDiscoveryHome(t)
	dbPath := filepath.Join(appSupportDir("Cursor"), "User", "workspaceStorage", "abc123", "state.vscdb")
	writeStateDBRows(t, dbPath, map[string]any{
		"aiService.prompts":                           `[{"role":"user","content":"private@example.com"}]`,
		"aiService.generations":                       `[{"tool":"terminal","arguments":{"command":"cat /Users/private/repo/.env"}}]`,
		"workbench.panel.aichat.view.aichat.chatdata": `{"messages":[{"role":"assistant","content":"oauth-refresh-token"}]}`,
		"workbench.panel.chat.view.chat.chatdata":     `{"messages":[{"role":"user","content":"sk-test-secret"}]}`,
		"agentKv:blob:composer-secret":                `{"tool":"apply_patch","arguments":{"patch":"secret patch"}}`,
		"unrelatedPrivateKey":                         "should not be extracted",
	})

	data, err := readSQLiteStateAsJSONL(dbPath, sqliteSourceDefinitions()[0].prefixes, 0)
	if err != nil {
		t.Fatalf("read SQLite state: %v", err)
	}
	for _, want := range []string{`"key_type":"aiservice_prompts"`, `"key_type":"aiservice_generations"`, `"key_type":"workbench_panel_aichat_view_aichat_chatdata"`, `"key_type":"workbench_panel_chat_view_chat_chatdata"`, `"key_type":"agentkvblob"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("expected %s in extracted Cursor rows, got %s", want, data)
		}
	}
	report, err := analyzer.AnalyzeForSource("cursor-legacy-sqlite-test", "cursor", data)
	if err != nil {
		t.Fatalf("AnalyzeForSource: %v", err)
	}
	if report.AnalysisSignals.ToolCallCount == 0 {
		t.Fatalf("expected Cursor legacy SQLite tool signal, got %#v", report.AnalysisSignals)
	}
	assertReportDoesNotContain(t, report, "private@example.com", "/Users/private/repo", "oauth-refresh-token", "sk-test-secret", "composer-secret")
}

func TestSQLiteReadOnlyDSNRejectsWrites(t *testing.T) {
	isolatedDiscoveryHome(t)
	dbPath := filepath.Join(appSupportDir("Cursor"), "User", "workspaceStorage", "abc123", "state.vscdb")
	writeCursorStateDB(t, dbPath)

	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(dbPath))
	if err != nil {
		t.Fatalf("open read-only sqlite: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO ItemTable(key, value) VALUES ('bubbleId:write-attempt', 'should fail')`); err == nil {
		t.Fatal("expected read-only SQLite DSN to reject writes")
	}
}

func TestSQLiteSourceExtraction_ReadsWALStateDBWithoutSourceWrites(t *testing.T) {
	isolatedDiscoveryHome(t)
	dbPath := filepath.Join(appSupportDir("Cursor"), "User", "workspaceStorage", "abc123", "state.vscdb")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatalf("mkdir sqlite parent: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA wal_autocheckpoint=0`,
		`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`,
		`INSERT INTO ItemTable(key, value) VALUES ('bubbleId:wal-message', '{"role":"user","content":"wal content"}')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec sqlite stmt %q: %v", stmt, err)
		}
	}
	sidecars := existingPaths(dbPath, dbPath+"-wal", dbPath+"-shm")
	if len(sidecars) < 2 {
		t.Fatalf("expected SQLite WAL sidecars, got %#v", sidecars)
	}
	beforeEntries := snapshotDirEntries(t, filepath.Dir(dbPath))
	beforeModTimes := snapshotFileModTimes(t, sidecars...)

	data, err := readSQLiteStateAsJSONL(dbPath, []string{"bubbleId:"}, 0)
	if err != nil {
		t.Fatalf("read WAL SQLite state: %v", err)
	}
	if !strings.Contains(string(data), "wal content") {
		t.Fatalf("expected WAL-backed content, got %s", data)
	}
	afterEntries := snapshotDirEntries(t, filepath.Dir(dbPath))
	if !reflect.DeepEqual(beforeEntries, afterEntries) {
		t.Fatalf("SQLite WAL read changed source directory entries: before=%#v after=%#v", beforeEntries, afterEntries)
	}
	afterModTimes := snapshotFileModTimes(t, sidecars...)
	if !reflect.DeepEqual(beforeModTimes, afterModTimes) {
		t.Fatalf("SQLite WAL read changed source file mtimes: before=%#v after=%#v", beforeModTimes, afterModTimes)
	}
}

func TestSQLiteSourceExtraction_KiroAndAntigravityFixtures(t *testing.T) {
	isolatedDiscoveryHome(t)
	kiroDB := filepath.Join(appSupportDir("Kiro"), "User", "workspaceStorage", "kiro-workspace", "state.vscdb")
	antigravityDB := filepath.Join(appSupportDir("Antigravity"), "User", "workspaceStorage", "ag-workspace", "state.vscdb")
	writeStateDBRows(t, kiroDB, map[string]any{
		"kiro.kiroAgent:session-secret": `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"cat /Users/private/repo/.env"}}`,
		"kiro:protobuf":                 []byte{0xff, 0xfe, 0xfd},
		"unrelatedPrivateKey":           "private@example.com",
	})
	writeStateDBRows(t, antigravityDB, map[string]any{
		"transcript:session-secret": `{"type":"terminal_command","command":"cat /Users/private/repo/.env"}`,
		"agent:result":              `{"type":"tool_result","output":"oauth-refresh-token"}`,
		"unrelatedPrivateKey":       "private@example.com",
	})

	candidates, err := recentSQLiteSourceLogs(10, 0, 1)
	if err != nil {
		t.Fatalf("recentSQLiteSourceLogs: %v", err)
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		seen[candidate.SourceID] = true
		data, err := candidate.readBytes()
		if err != nil {
			t.Fatalf("read %s sqlite candidate: %v", candidate.SourceID, err)
		}
		report, err := analyzer.AnalyzeForSource(candidate.SourceID+"-sqlite-test", candidate.SourceID, data)
		if err != nil {
			t.Fatalf("AnalyzeForSource %s: %v", candidate.SourceID, err)
		}
		assertReportDoesNotContain(t, report, "private@example.com", "/Users/private/repo", "oauth-refresh-token", "session-secret")
	}
	for _, want := range []string{"kiro_ide", "antigravity"} {
		if !seen[want] {
			t.Fatalf("missing SQLite source %s from %#v", want, candidates)
		}
	}
}

func TestSQLiteSourceExtraction_FiltersBeforeLimitAndBoundsOutput(t *testing.T) {
	isolatedDiscoveryHome(t)
	dbPath := filepath.Join(appSupportDir("Cursor"), "User", "workspaceStorage", "abc123", "state.vscdb")
	writeLargeCursorStateDB(t, dbPath)

	data, err := readSQLiteStateAsJSONL(dbPath, []string{"bubbleId:"}, 1024)
	if err != nil {
		t.Fatalf("read SQLite state: %v", err)
	}
	if strings.Contains(string(data), "oversized-secret") {
		t.Fatalf("bounded SQLite output leaked oversized value: %s", data)
	}
	data, err = readSQLiteStateAsJSONL(dbPath, []string{"bubbleId:"}, 0)
	if err != nil {
		t.Fatalf("read unbounded SQLite state: %v", err)
	}
	if !strings.Contains(string(data), `"key_type":"bubbleid"`) {
		t.Fatalf("expected filtered supported row after unrelated rows, got %s", data)
	}
}

func TestSQLiteSourceExtraction_EmptySupportedRowsDoesNotFailAnalysis(t *testing.T) {
	isolatedDiscoveryHome(t)
	dbPath := filepath.Join(appSupportDir("Cursor"), "User", "workspaceStorage", "abc123", "state.vscdb")
	writeEmptyStateDB(t, dbPath)

	candidates, err := recentSupportedLogs(1)
	if err != nil {
		t.Fatalf("recentSupportedLogs: %v", err)
	}
	var cursorCandidate *logCandidate
	for i := range candidates {
		if candidates[i].SourceID == "cursor" && strings.Contains(candidates[i].SourceLabel, "SQLite") {
			cursorCandidate = &candidates[i]
			break
		}
	}
	if cursorCandidate == nil {
		t.Fatalf("expected Cursor SQLite candidate, got %#v", candidates)
	}
	data, err := cursorCandidate.readBytes()
	if err != nil {
		t.Fatalf("read empty SQLite candidate: %v", err)
	}
	if _, err := analyzer.AnalyzeForSource("cursor-sqlite-empty-test", "cursor", data); err != nil {
		t.Fatalf("AnalyzeForSource empty SQLite candidate: %v", err)
	}
}

func writeCursorStateDB(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir sqlite parent: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`,
		`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`,
		`INSERT INTO ItemTable(key, value) VALUES ('bubbleId:composer-secret:message', '{"role":"user","content":"private@example.com arn:aws:iam::123456789012:user/private oauth-refresh-token"}')`,
		`INSERT INTO cursorDiskKV(key, value) VALUES ('composerData:composer-secret', '{"tool":"terminal","arguments":{"command":"cat .env"}}')`,
		`INSERT INTO ItemTable(key, value) VALUES ('unrelatedPrivateKey', 'should not be extracted')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec sqlite stmt %q: %v", stmt, err)
		}
	}
}

func writeEmptyStateDB(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir sqlite parent: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		t.Fatalf("create empty sqlite table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable(key, value) VALUES ('unrelatedPrivateKey', 'should not be extracted')`); err != nil {
		t.Fatalf("insert unrelated sqlite row: %v", err)
	}
}

func writeStateDBRows(t *testing.T, path string, rows map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir sqlite parent: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		t.Fatalf("create sqlite table: %v", err)
	}
	for key, value := range rows {
		if _, err := db.Exec(`INSERT INTO ItemTable(key, value) VALUES (?, ?)`, key, value); err != nil {
			t.Fatalf("insert sqlite row %q: %v", key, err)
		}
	}
}

func writeCodexLogsDB(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir codex sqlite parent: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open codex sqlite: %v", err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts INTEGER NOT NULL,
			ts_nanos INTEGER NOT NULL,
			level TEXT NOT NULL,
			target TEXT NOT NULL,
			feedback_log_body TEXT,
			module_path TEXT,
			file TEXT,
			line INTEGER,
			thread_id TEXT,
			process_uuid TEXT,
			estimated_bytes INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT INTO logs(ts, ts_nanos, level, target, feedback_log_body, module_path, file, line, estimated_bytes) VALUES (1, 1, 'ERROR', 'codex_core::runner', 'failed in /Users/private/repo with sk-test-secret', 'codex_core::runner', '/Users/private/repo/main.rs', 42, 128)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec codex sqlite stmt: %v", err)
		}
	}
}

func writeLargeCursorStateDB(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir sqlite parent: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		t.Fatalf("create sqlite table: %v", err)
	}
	for i := 0; i < 600; i++ {
		if _, err := db.Exec(`INSERT INTO ItemTable(key, value) VALUES (?, ?)`, fmt.Sprintf("unrelated-%03d", i), "ignored"); err != nil {
			t.Fatalf("insert unrelated row: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO ItemTable(key, value) VALUES ('bubbleId:supported:message', ?)`, strings.Repeat("oversized-secret", 300)); err != nil {
		t.Fatalf("insert supported row: %v", err)
	}
}

func TestRecentSupportedLogs_SelectsClosestTargetSizeWhenLimitIsOne(t *testing.T) {
	home := isolatedDiscoveryHome(t)
	claudeRoot := filepath.Join(home, ".claude", "projects", "repo")
	if err := os.MkdirAll(claudeRoot, 0o700); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	newestSmall := filepath.Join(claudeRoot, "newest-small.jsonl")
	recentLarge := filepath.Join(claudeRoot, "recent-large.jsonl")
	staleHuge := filepath.Join(claudeRoot, "stale-huge.jsonl")
	for _, path := range []string{newestSmall, recentLarge, staleHuge} {
		writeMeaningfulLog(t, path)
	}
	if err := os.Truncate(newestSmall, 16*1024); err != nil {
		t.Fatalf("truncate newest: %v", err)
	}
	if err := os.Truncate(recentLarge, 128*1024); err != nil {
		t.Fatalf("truncate recent: %v", err)
	}
	if err := os.Truncate(staleHuge, 512*1024); err != nil {
		t.Fatalf("truncate stale: %v", err)
	}
	now := time.Unix(200000, 0)
	for path, modTime := range map[string]time.Time{
		newestSmall: now,
		recentLarge: now.Add(-24 * time.Hour),
		staleHuge:   now.Add(-90 * 24 * time.Hour),
	} {
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}

	candidates, err := recentSupportedLogs(1)
	if err != nil {
		t.Fatalf("recentSupportedLogs: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one Claude candidate, got %#v", candidates)
	}
	if got := filepath.Base(candidates[0].Display); got != "stale-huge.jsonl" {
		t.Fatalf("expected closest-to-target log, got %s", got)
	}
}

func TestDefaultSupportedLogs_UsesSmallFilesToApproachTargetSize(t *testing.T) {
	home := isolatedDiscoveryHome(t)
	codexRoot := filepath.Join(home, ".codex", "sessions", "2026")
	if err := os.MkdirAll(codexRoot, 0o700); err != nil {
		t.Fatalf("mkdir codex: %v", err)
	}
	var paths []string
	for index := 0; index < 5; index++ {
		path := filepath.Join(codexRoot, fmt.Sprintf("log-%d.jsonl", index))
		writeMeaningfulLog(t, path)
		if err := os.Truncate(path, 512*1024); err != nil {
			t.Fatalf("truncate log %d: %v", index, err)
		}
		mtime := time.Unix(int64(100+index), 0)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes log %d: %v", index, err)
		}
		paths = append(paths, path)
	}

	candidates, err := defaultSupportedLogs()
	if err != nil {
		t.Fatalf("defaultSupportedLogs: %v", err)
	}
	if len(candidates) != defaultAutoLogLimit {
		t.Fatalf("expected five small logs when target size cannot be reached, got %#v", candidates)
	}
	for index, candidate := range candidates {
		want := filepath.Base(paths[4-index])
		if got := filepath.Base(candidate.Display); got != want {
			t.Fatalf("candidate %d = %s, want %s; candidates=%#v", index, got, want, candidates)
		}
	}
}

func TestDefaultSupportedLogs_UsesOneHugeFileWhenOnlyHugeFilesExist(t *testing.T) {
	home := isolatedDiscoveryHome(t)
	root := filepath.Join(home, ".claude", "projects", "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	olderHuge := filepath.Join(root, "older-huge.jsonl")
	newerHuge := filepath.Join(root, "newer-huge.jsonl")
	for _, path := range []string{olderHuge, newerHuge} {
		writeMeaningfulLog(t, path)
		if err := os.Truncate(path, 50*1024*1024); err != nil {
			t.Fatalf("truncate huge: %v", err)
		}
	}
	for path, modTime := range map[string]time.Time{
		olderHuge: time.Unix(100, 0),
		newerHuge: time.Unix(200, 0),
	} {
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}

	candidates, err := defaultSupportedLogs()
	if err != nil {
		t.Fatalf("defaultSupportedLogs: %v", err)
	}
	if len(candidates) != 1 || filepath.Base(candidates[0].Display) != "newer-huge.jsonl" {
		t.Fatalf("expected only the best huge file, got %#v", candidates)
	}
}

func TestDefaultSupportedLogs_UsesOneNearTargetFileInsteadOfTwo(t *testing.T) {
	home := isolatedDiscoveryHome(t)
	root := filepath.Join(home, ".claude", "projects", "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	first := filepath.Join(root, "first-near-target.jsonl")
	second := filepath.Join(root, "second-near-target.jsonl")
	for _, path := range []string{first, second} {
		writeMeaningfulLog(t, path)
		if err := os.Truncate(path, targetAutoLogBytes-128*1024); err != nil {
			t.Fatalf("truncate near-target: %v", err)
		}
	}
	for path, modTime := range map[string]time.Time{
		first:  time.Unix(200, 0),
		second: time.Unix(100, 0),
	} {
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}

	candidates, err := defaultSupportedLogs()
	if err != nil {
		t.Fatalf("defaultSupportedLogs: %v", err)
	}
	if len(candidates) != 1 || filepath.Base(candidates[0].Display) != "first-near-target.jsonl" {
		t.Fatalf("expected one near-target file, got %#v", candidates)
	}
}

func TestDefaultSupportedLogs_DoesNotOvershootTargetWithHugeSecondFile(t *testing.T) {
	home := isolatedDiscoveryHome(t)
	root := filepath.Join(home, ".claude", "projects", "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	nearTarget := filepath.Join(root, "near-target.jsonl")
	huge := filepath.Join(root, "huge.jsonl")
	writeMeaningfulLog(t, nearTarget)
	writeMeaningfulLog(t, huge)
	if err := os.Truncate(nearTarget, targetAutoLogBytes-128*1024); err != nil {
		t.Fatalf("truncate near target: %v", err)
	}
	if err := os.Truncate(huge, 50*1024*1024); err != nil {
		t.Fatalf("truncate huge: %v", err)
	}
	for path, modTime := range map[string]time.Time{
		nearTarget: time.Unix(200, 0),
		huge:       time.Unix(100, 0),
	} {
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}

	candidates, err := defaultSupportedLogs()
	if err != nil {
		t.Fatalf("defaultSupportedLogs: %v", err)
	}
	if len(candidates) != 1 || filepath.Base(candidates[0].Display) != "near-target.jsonl" {
		t.Fatalf("expected near-target undershoot instead of a huge overshoot, got %#v", candidates)
	}
}

func TestDefaultSupportedLogs_PrefersLargestRecentMeaningfulLogs(t *testing.T) {
	home := isolatedDiscoveryHome(t)
	root := filepath.Join(home, ".claude", "projects", "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	tinyNewest := filepath.Join(root, "tiny-newest.jsonl")
	smaller := filepath.Join(root, "smaller.jsonl")
	larger := filepath.Join(root, "larger.jsonl")
	writeLogContent(t, tinyNewest, "{}\n")
	writeMeaningfulLog(t, smaller)
	writeMeaningfulLog(t, larger)
	if err := os.Truncate(smaller, freeAutoMinLogBytes+512); err != nil {
		t.Fatalf("truncate smaller: %v", err)
	}
	if err := os.Truncate(larger, freeAutoMinLogBytes+4096); err != nil {
		t.Fatalf("truncate larger: %v", err)
	}
	for path, modTime := range map[string]time.Time{
		smaller:    time.Unix(100, 0),
		larger:     time.Unix(200, 0),
		tinyNewest: time.Unix(300, 0),
	} {
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}

	candidates, err := defaultSupportedLogs()
	if err != nil {
		t.Fatalf("defaultSupportedLogs: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected two meaningful Claude logs, got %#v", candidates)
	}
	if filepath.Base(candidates[0].Display) != "larger.jsonl" || filepath.Base(candidates[1].Display) != "smaller.jsonl" {
		t.Fatalf("expected largest meaningful recent Claude logs, got %#v", candidates)
	}
}

func TestRecentOpenCodeSessions_ReadsMessageDirectoriesAndSkipsTinySessions(t *testing.T) {
	home := isolatedDiscoveryHome(t)
	root := filepath.Join(home, ".local", "share", "opencode", "storage", "message")
	partRoot := filepath.Join(home, ".local", "share", "opencode", "storage", "part")
	tiny := filepath.Join(root, "ses_tiny")
	big := filepath.Join(root, "ses_big")
	for _, dir := range []string{tiny, big} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir opencode session: %v", err)
		}
	}
	writeLogContent(t, filepath.Join(tiny, "msg_1.json"), "{}")
	writeLogContent(t, filepath.Join(big, "msg_big.json"), `{"id":"msg_big","sessionID":"ses_big","role":"assistant","text":"`+strings.Repeat("x", freeAutoMinLogBytes+1024)+`"}`)
	if err := os.MkdirAll(filepath.Join(partRoot, "msg_big"), 0o700); err != nil {
		t.Fatalf("mkdir opencode parts: %v", err)
	}
	writeLogContent(t, filepath.Join(partRoot, "msg_big", "part_1.json"), `{"id":"part_1","messageID":"msg_big","type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"npm test"},"output":"ok"}}`)

	candidates, err := recentOpenCodeSessions(10, 0, freeAutoMinLogBytes)
	if err != nil {
		t.Fatalf("recentOpenCodeSessions: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Display != "opencode session ses_big" {
		t.Fatalf("expected only meaningful OpenCode message session, got %#v", candidates)
	}
	data, err := candidates[0].readBytes()
	if err != nil {
		t.Fatalf("read opencode session: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") || !strings.Contains(string(data), `"id":"msg_big"`) {
		t.Fatalf("expected JSONL message output, got %q", string(data[:min(len(data), 80)]))
	}
	if !strings.Contains(string(data), `"type":"tool"`) || !strings.Contains(string(data), `"tool":"bash"`) {
		t.Fatalf("expected OpenCode part JSONL to be joined, got %q", string(data[:min(len(data), 200)]))
	}
}

func TestAnalyze_PositionalOnly_UsesPositional(t *testing.T) {
	dir := t.TempDir()
	logPath := writeSampleLog(t, dir)
	outPath := filepath.Join(dir, "report.json")
	// Shim latest to a non-existent path to prove we did NOT fall through to
	// it; if the positional resolution were skipped, runAnalyze would try
	// to read the shim path and fail.
	withDefaultDiscoveryShim(t, filepath.Join(dir, "does-not-exist.jsonl"))

	err := runAnalyze([]string{"--out", outPath, logPath})
	if err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("expected report at %s: %v", outPath, err)
	}
}

func TestAnalyze_PositionalBeforeOutFlag_UsesPositionalAndOut(t *testing.T) {
	dir := t.TempDir()
	logPath := writeSampleLog(t, dir)
	outPath := filepath.Join(dir, "report.json")
	withDefaultDiscoveryShim(t, filepath.Join(dir, "does-not-exist.jsonl"))

	err := runAnalyze([]string{logPath, "--out", outPath})
	if err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("expected report at %s: %v", outPath, err)
	}
}

func TestAnalyze_LogFlagOnly_UsesLogFlag(t *testing.T) {
	dir := t.TempDir()
	logPath := writeSampleLog(t, dir)
	outPath := filepath.Join(dir, "report.json")
	withDefaultDiscoveryShim(t, filepath.Join(dir, "does-not-exist.jsonl"))

	err := runAnalyze([]string{"--log", logPath, "--out", outPath})
	if err != nil {
		t.Fatalf("runAnalyze: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("expected report at %s: %v", outPath, err)
	}
}

func TestAnalyze_PositionalPlusLog_Refuses(t *testing.T) {
	dir := t.TempDir()
	logPath := writeSampleLog(t, dir)
	outPath := filepath.Join(dir, "report.json")

	err := runAnalyze([]string{"--log", logPath, "--out", outPath, logPath})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot combine positional log path with --log") {
		t.Fatalf("unexpected error message: %v", err)
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected no report at %s, stat err=%v", outPath, statErr)
	}
}

func TestAnalyze_TwoPositionals_Refuses(t *testing.T) {
	dir := t.TempDir()
	logPath := writeSampleLog(t, dir)
	secondPath := filepath.Join(dir, "second.jsonl")
	if err := os.WriteFile(secondPath, []byte(sampleJSONL), 0o600); err != nil {
		t.Fatalf("write second log: %v", err)
	}
	outPath := filepath.Join(dir, "report.json")

	err := runAnalyze([]string{"--out", outPath, logPath, secondPath})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected extra argument") {
		t.Fatalf("unexpected error message: %v", err)
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected no report at %s, stat err=%v", outPath, statErr)
	}
}

func TestAnalyze_PositionalNonExistent_Refuses(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.jsonl")
	outPath := filepath.Join(dir, "report.json")

	err := runAnalyze([]string{"--out", outPath, missing})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestAnalyzePaid_WritesSanitizedAggregate(t *testing.T) {
	dir := t.TempDir()
	first := writeSampleLog(t, dir)
	second := filepath.Join(dir, "second.jsonl")
	if err := os.WriteFile(second, []byte(sampleJSONL), 0o600); err != nil {
		t.Fatalf("write second log: %v", err)
	}
	outPath := filepath.Join(dir, "paid-report.json")
	withRecentShim(t, []logCandidate{
		fileCandidate("claude_code", "Claude Code", first),
		fileCandidate("codex", "Codex", second),
	})

	err := runAnalyze([]string{"--paid", "--limit", "3", "--out", outPath})
	if err != nil {
		t.Fatalf("runAnalyze paid: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read paid report: %v", err)
	}
	var report analyzer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("paid report is not JSON: %v", err)
	}
	if report.AggregateEvent.ParserType != "paid_bundle" {
		t.Fatalf("expected paid_bundle parser type, got %#v", report.AggregateEvent)
	}
	if report.Metrics.SessionCount != 2 {
		t.Fatalf("expected two paid sessions, got %#v", report.Metrics)
	}
	if len(report.SourceReports) != 2 {
		t.Fatalf("expected per-source paid report sections, got %#v", report.SourceReports)
	}
	if report.SecurityReceipt.RawLogTTL != "not uploaded" || report.SecurityReceipt.RawTranscriptSentToLLM {
		t.Fatalf("expected local-only security receipt, got %#v", report.SecurityReceipt)
	}
}

func TestAnalyzePaid_RejectsUnsafeArguments(t *testing.T) {
	dir := t.TempDir()
	logPath := writeSampleLog(t, dir)
	outPath := filepath.Join(dir, "paid-report.json")

	err := runAnalyze([]string{"--paid", "--out", outPath, logPath})
	if err == nil || !strings.Contains(err.Error(), "--paid cannot be combined") {
		t.Fatalf("expected paid positional rejection, got %v", err)
	}
	err = runAnalyze([]string{"--paid", "--limit", "6", "--out", outPath})
	if err == nil || !strings.Contains(err.Error(), "--limit cannot exceed 5") {
		t.Fatalf("expected paid limit rejection, got %v", err)
	}
}

func TestProveSavings_WritesValidationReport(t *testing.T) {
	isolatedDiscoveryHome(t)
	dir := t.TempDir()
	baselineLog := filepath.Join(dir, "baseline.jsonl")
	currentLog := filepath.Join(dir, "current.jsonl")
	baselineContent := `{"type":"assistant","message":{"usage":{"input_tokens":1000,"cache_creation_input_tokens":200,"output_tokens":100},"content":[{"type":"tool_use","name":"Bash","input":{"command":"cat src/a.go"}},{"type":"tool_result","content":"` + strings.Repeat("x", 400) + `"}]}}` + "\n"
	currentContent := `{"type":"assistant","message":{"usage":{"input_tokens":500,"cache_creation_input_tokens":100,"output_tokens":80},"content":[{"type":"tool_use","name":"Bash","input":{"command":"cat src/a.go"}},{"type":"tool_result","content":"` + strings.Repeat("x", 120) + `"}]}}` + "\n"
	writeLogContent(t, baselineLog, baselineContent)
	writeLogContent(t, currentLog, currentContent)

	baselineReportPath := filepath.Join(dir, "baseline-report.json")
	if err := runAnalyze([]string{"--source", "claude_code", "--log", baselineLog, "--out", baselineReportPath}); err != nil {
		t.Fatalf("baseline analyze: %v", err)
	}
	outPath := filepath.Join(dir, "followup-report.json")
	if err := runProveSavings([]string{"--baseline", baselineReportPath, "--source", "claude_code", "--log", currentLog, "--out", outPath}); err != nil {
		t.Fatalf("prove-savings: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read follow-up report: %v", err)
	}
	var report analyzer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("follow-up report is not JSON: %v", err)
	}
	if report.SavingsValidation == nil {
		t.Fatalf("expected savings validation in follow-up report")
	}
	if report.SavingsValidation.EvidenceTier == "" {
		t.Fatalf("expected evidence tier, got %#v", report.SavingsValidation)
	}
	assertReportDoesNotContain(t, report, baselineLog, currentLog)
}

func TestRunOneShot_AnalyzesAndUploadsSanitizedReport(t *testing.T) {
	dir := t.TempDir()
	logPath := writeSampleLog(t, dir)
	outPath := filepath.Join(dir, "agent-analyzer-report.json")
	var received analyzer.Report
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/client-reports" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode uploaded report: %v", err)
		}
		if received.SecurityReceipt.RawLogTTL != "not uploaded" || received.SecurityReceipt.RawTranscriptSentToLLM {
			t.Fatalf("uploaded report violated local-first receipt: %#v", received.SecurityReceipt)
		}
		expiresAt := time.Now().Add(15 * time.Minute)
		_ = json.NewEncoder(w).Encode(uploadResult{
			ReportURL: serverURL(r) + "/r/job-token/report-token",
			ExpiresAt: &expiresAt,
		})
	}))
	defer server.Close()

	err := runOneShot([]string{
		"--log", logPath,
		"--out", outPath,
		"--base-url", server.URL,
		"--yes",
		"--no-open",
	})
	if err != nil {
		t.Fatalf("runOneShot: %v", err)
	}
	if received.Version == "" {
		t.Fatalf("expected uploaded report, got %#v", received)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("expected local report file at %s: %v", outPath, err)
	}
}

func TestRunFullScan_AnalyzesPaidAggregateAndUploadsWithEntitlementToken(t *testing.T) {
	dir := t.TempDir()
	first := writeSampleLog(t, dir)
	second := filepath.Join(dir, "second.jsonl")
	if err := os.WriteFile(second, []byte(sampleJSONL), 0o600); err != nil {
		t.Fatalf("write second log: %v", err)
	}
	var discoveredLimit int
	originalRecent := recentSupportedLogsFn
	recentSupportedLogsFn = func(limit int) ([]logCandidate, error) {
		discoveredLimit = limit
		return []logCandidate{
			fileCandidate("claude_code", "Claude Code", first),
			fileCandidate("codex", "Codex", second),
		}, nil
	}
	t.Cleanup(func() { recentSupportedLogsFn = originalRecent })
	outPath := filepath.Join(dir, "full-scan.json")
	var authHeader string
	var received analyzer.Report
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/full-scan-client-reports" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		authHeader = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode uploaded report: %v", err)
		}
		expiresAt := time.Now().Add(15 * time.Minute)
		_ = json.NewEncoder(w).Encode(uploadResult{
			ReportURL: serverURL(r) + "/r/job-token/report-token",
			ExpiresAt: &expiresAt,
		})
	}))
	defer server.Close()

	err := runFullScan([]string{
		"--token", "email-token",
		"--out", outPath,
		"--base-url", server.URL,
		"--no-open",
	})
	if err != nil {
		t.Fatalf("runFullScan: %v", err)
	}
	if authHeader != "Bearer email-token" {
		t.Fatalf("expected bearer entitlement token, got %q", authHeader)
	}
	if discoveredLimit != defaultAutoLogLimit {
		t.Fatalf("expected full-scan default limit %d, got %d", defaultAutoLogLimit, discoveredLimit)
	}
	if received.AggregateEvent.ParserType != "full_scan_bundle" || received.SecurityReceipt.RawLogTTL != "not uploaded" {
		t.Fatalf("expected sanitized full-scan aggregate upload, got %#v", received)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}

func TestVersion_PrintsProvenance(t *testing.T) {
	var buf bytes.Buffer
	original := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = write
	t.Cleanup(func() { os.Stdout = original })

	err = run([]string{"version"})
	if err != nil {
		t.Fatalf("run version: %v", err)
	}
	if err := write.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if _, err := buf.ReadFrom(read); err != nil {
		t.Fatalf("read stdout: %v", err)
	}

	output := buf.String()
	for _, want := range []string{
		"agent-analyzer ",
		"commit:",
		"built:",
		"source: https://github.com/Priivacy-ai/agent-log-analyzer",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("version output missing %q:\n%s", want, output)
		}
	}
}

func TestProgressLinesAvoidsCarriageReturnRepaints(t *testing.T) {
	t.Setenv("AGENT_ANALYZER_PROGRESS", "lines")
	output := captureStdout(t, func() {
		progress := newProgressBar(3)
		progress.Update(0, "reading")
		progress.Update(1, "analyzing")
		progress.Update(2, "writing")
		progress.Finish("complete")
	})

	if strings.Contains(output, "\r") {
		t.Fatalf("line progress should not repaint with carriage returns: %q", output)
	}
	for _, want := range []string{"[0/3] reading", "[1/3] analyzing", "[2/3] writing", "[3/3] complete"} {
		if !strings.Contains(output, want) {
			t.Fatalf("line progress missing %q:\n%s", want, output)
		}
	}
}

func TestProgressBarOverrideUsesSingleLineRepaints(t *testing.T) {
	t.Setenv("AGENT_ANALYZER_PROGRESS", "bar")
	output := captureStdout(t, func() {
		progress := newProgressBar(2)
		progress.Update(0, "reading")
		progress.Finish("complete")
	})

	if !strings.Contains(output, "\r") {
		t.Fatalf("bar override should repaint with carriage returns: %q", output)
	}
	if strings.Count(output, "\n") != 1 {
		t.Fatalf("bar progress should only end with one newline: %q", output)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	original := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = write
	defer func() { os.Stdout = original }()

	fn()

	if err := write.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if _, err := buf.ReadFrom(read); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return buf.String()
}
