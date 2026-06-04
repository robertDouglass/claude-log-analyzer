package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/priivacy-ai/agent-log-analyzer/internal/analyzer"
	"github.com/priivacy-ai/agent-log-analyzer/internal/remediation"
)

const defaultBaseURL = "https://claude-code.spec-kitty.ai"

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
	case "analyze":
		return runAnalyze(args[1:])
	case "plugin":
		return runPlugin(args[1:])
	case "upload":
		return runUpload(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runAnalyze(args []string) error {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	out := fs.String("out", "claude-analyzer-report.json", "path to write sanitized report JSON")
	logPath := fs.String("log", "", "explicit Claude Code JSONL log path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path := *logPath
	if path == "" {
		latest, err := latestClaudeLog()
		if err != nil {
			return err
		}
		path = latest
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	report, err := analyzer.Analyze("local", data)
	if err != nil {
		return err
	}
	report.SecurityReceipt.RawLogTTL = "not uploaded"
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, append(encoded, '\n'), 0o600); err != nil {
		return err
	}

	fmt.Printf("Analyzed locally: %s\n", path)
	fmt.Printf("Raw bytes read locally: %d\n", len(data))
	fmt.Printf("Secrets redacted before report write: %d\n", report.SecurityReceipt.SecretsRedacted)
	fmt.Printf("Sanitized report: %s (%d bytes)\n", *out, len(encoded)+1)
	fmt.Println()
	fmt.Printf("Review before upload: jq . %s\n", shellQuote(*out))
	fmt.Printf("Upload sanitized report: claude-analyzer upload %s\n", shellQuote(*out))
	return nil
}

func runPlugin(args []string) error {
	fs := flag.NewFlagSet("plugin", flag.ContinueOnError)
	reportPath := fs.String("report", "claude-analyzer-report.json", "sanitized analyzer report JSON")
	out := fs.String("out", "claude-analyzer-optimization.zip", "path to write Claude Code plugin zip")
	artifactURL := fs.String("artifact-url", "", "optional short-lived artifact URL to embed in install instructions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: claude-analyzer plugin [--report sanitized-report.json] [--out plugin.zip]")
	}
	data, err := os.ReadFile(*reportPath)
	if err != nil {
		return err
	}
	var report analyzer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("report is not valid analyzer JSON: %w", err)
	}
	if report.SecurityReceipt.RawTranscriptSentToLLM {
		return errors.New("refusing to generate plugin from a report that claims raw transcript was sent to an LLM")
	}
	artifact := remediation.Generate(report, remediation.Options{ArtifactURL: *artifactURL})
	var buffer bytes.Buffer
	if err := remediation.WriteZip(&buffer, artifact); err != nil {
		return err
	}
	if err := os.WriteFile(*out, buffer.Bytes(), 0o600); err != nil {
		return err
	}
	fmt.Printf("Generated plugin: %s (%d bytes)\n", *out, buffer.Len())
	fmt.Printf("Load for one Claude Code session: claude --plugin-dir %s\n", shellQuote(*out))
	fmt.Println("Review WAIVER.md and /agent-analyzer-tooling before installing optional tools.")
	return nil
}

func runUpload(args []string) error {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	baseURL := fs.String("base-url", defaultBaseURL, "Claude Analyzer base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: claude-analyzer upload <sanitized-report.json>")
	}
	reportPath := fs.Arg(0)
	data, err := os.ReadFile(reportPath)
	if err != nil {
		return err
	}
	var report analyzer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("report is not valid analyzer JSON: %w", err)
	}
	if report.SecurityReceipt.RawTranscriptSentToLLM {
		return errors.New("refusing to upload report that claims raw transcript was sent to an LLM")
	}

	endpoint := strings.TrimRight(*baseURL, "/") + "/api/client-reports"
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("upload failed: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var result struct {
		ReportURL string    `json:"report_url"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	fmt.Printf("Uploaded sanitized report only: %s\n", reportPath)
	fmt.Printf("Report: %s\n", result.ReportURL)
	if !result.ExpiresAt.IsZero() {
		fmt.Printf("Expires: %s\n", result.ExpiresAt.Local().Format(time.RFC1123))
	}
	return nil
}

func latestClaudeLog() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, ".claude", "projects")
	var matches []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		matches = append(matches, path)
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if len(matches) == 0 {
		return "", errors.New("no Claude Code JSONL logs found under ~/.claude/projects")
	}
	sort.Slice(matches, func(i, j int) bool {
		left, leftErr := os.Stat(matches[i])
		right, rightErr := os.Stat(matches[j])
		if leftErr != nil || rightErr != nil {
			return matches[i] > matches[j]
		}
		return left.ModTime().After(right.ModTime())
	})
	return matches[0], nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  claude-analyzer analyze [--log path] [--out claude-analyzer-report.json]")
	fmt.Fprintln(os.Stderr, "  claude-analyzer plugin [--report claude-analyzer-report.json] [--out claude-analyzer-optimization.zip]")
	fmt.Fprintln(os.Stderr, "  claude-analyzer upload <sanitized-report.json> [--base-url https://claude-code.spec-kitty.ai]")
}
