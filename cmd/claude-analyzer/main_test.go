package main

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/priivacy-ai/agent-log-analyzer/internal/analyzer"
)

func TestRunPluginGeneratesZipFromSanitizedReport(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	zipPath := filepath.Join(dir, "plugin.zip")
	report := analyzer.Report{
		Version: "0.1.0",
		Score:   72,
		Metrics: analyzer.Metrics{Turns: 12},
		EstimatedWaste: analyzer.WasteRange{
			Low:  12,
			High: 20,
		},
		AggregateEvent: analyzer.AggregateSafeEvent{
			ScoreBucket: "60_80",
			WasteBucket: "0_20",
		},
		SecurityReceipt: analyzer.SecurityReceipt{
			RawTranscriptSentToLLM: false,
			OutboundDuringAnalysis: false,
		},
	}
	writeReport(t, reportPath, report)

	if err := runPlugin([]string{"--report", reportPath, "--out", zipPath}); err != nil {
		t.Fatalf("runPlugin failed: %v", err)
	}

	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("generated plugin is not a zip: %v", err)
	}
	defer reader.Close()

	if !zipHasFile(reader.File, ".claude-plugin/plugin.json") {
		t.Fatal("generated plugin missing manifest")
	}
	if !zipHasFile(reader.File, "commands/agent-analyzer-review.md") {
		t.Fatal("generated plugin missing review command")
	}
	if !zipHasFile(reader.File, "agents/token-hygiene-reviewer.md") {
		t.Fatal("generated plugin missing reviewer agent")
	}
}

func TestRunPluginRejectsUnsafeReportReceipt(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	report := analyzer.Report{
		Version: "0.1.0",
		Metrics: analyzer.Metrics{Turns: 1},
		SecurityReceipt: analyzer.SecurityReceipt{
			RawTranscriptSentToLLM: true,
		},
	}
	writeReport(t, reportPath, report)

	if err := runPlugin([]string{"--report", reportPath, "--out", filepath.Join(dir, "plugin.zip")}); err == nil {
		t.Fatal("expected unsafe report receipt to be rejected")
	}
}

func writeReport(t *testing.T, path string, report analyzer.Report) {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func zipHasFile(files []*zip.File, path string) bool {
	for _, file := range files {
		if file.Name == path {
			return true
		}
	}
	return false
}
