package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavingsValidationCopyDoesNotOverclaimNaturalLogs(t *testing.T) {
	root := filepath.Join("..", "..")
	files := []string{
		filepath.Join(root, "web", "index.html"),
		filepath.Join(root, "web", "app.js"),
		filepath.Join(root, "cmd", "api", "report_html.go"),
	}
	banned := []string{
		"natural logs prove",
		"natural-log proof",
		"before/after proves token savings",
		"prove causal token savings",
		"proves causal savings",
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		lower := strings.ToLower(string(data))
		for _, phrase := range banned {
			if strings.Contains(lower, phrase) {
				t.Fatalf("%s contains banned overclaim phrase %q", file, phrase)
			}
		}
	}
}
