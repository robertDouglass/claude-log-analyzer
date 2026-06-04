#!/usr/bin/env bash
set -euo pipefail

target="${OWNER_BREAKDOWN_BENCHMARK_TARGET:-${SOURCE_REPO:-/tmp/agent-analyzer-owner-breakdown-target-v1}}"
tag="${OWNER_BREAKDOWN_BENCHMARK_REF:-benchmark/owner-breakdown-v1-base}"

rm -rf "$target"
mkdir -p "$target/internal/events" "$target/internal/report" "$target/cmd/owner-report"

cat >"$target/go.mod" <<'EOF'
module example.com/ownerbreakdown

go 1.22
EOF

cat >"$target/internal/events/events.go" <<'EOF'
package events

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type Event struct {
	Service string `json:"service"`
	Owner   string `json:"owner"`
	Count   int    `json:"count"`
}

func NormalizeService(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.Join(strings.Fields(value), "-")
	if value == "" {
		return "unknown"
	}
	return value
}

func ParseCSV(r io.Reader) ([]Event, error) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	index := map[string]int{}
	for i, column := range header {
		index[strings.ToLower(strings.TrimSpace(column))] = i
	}
	for _, required := range []string{"service", "owner", "count"} {
		if _, ok := index[required]; !ok {
			return nil, fmt.Errorf("missing %s column", required)
		}
	}

	var events []Event
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		count, err := strconv.Atoi(strings.TrimSpace(row[index["count"]]))
		if err != nil {
			return nil, fmt.Errorf("parse count: %w", err)
		}
		events = append(events, Event{
			Service: NormalizeService(row[index["service"]]),
			Owner:   strings.TrimSpace(row[index["owner"]]),
			Count:   count,
		})
	}
	return events, nil
}
EOF

cat >"$target/internal/events/events_test.go" <<'EOF'
package events

import (
	"strings"
	"testing"
)

func TestParseCSVNormalizesService(t *testing.T) {
	rows, err := ParseCSV(strings.NewReader("service,owner,count\nAPI Gateway,Platform Team,3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0].Service; got != "api-gateway" {
		t.Fatalf("Service = %q, want api-gateway", got)
	}
}

func TestParseCSVNormalizesBlankOwner(t *testing.T) {
	rows, err := ParseCSV(strings.NewReader("service,owner,count\nAPI Gateway,   ,3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0].Owner; got != "unassigned" {
		t.Fatalf("Owner = %q, want unassigned", got)
	}
}
EOF

cat >"$target/internal/report/report.go" <<'EOF'
package report

import (
	"fmt"
	"sort"
	"strings"

	"example.com/ownerbreakdown/internal/events"
)

type Row struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Summary struct {
	Services []Row `json:"services"`
	Owners   []Row `json:"owners"`
}

func Aggregate(input []events.Event) Summary {
	services := map[string]int{}
	owners := map[string]int{}
	for _, event := range input {
		services[event.Service] += event.Count
		owners[event.Service] += event.Count
	}
	return Summary{
		Services: sortedRows(services),
		Owners:   sortedRows(owners),
	}
}

func RenderMarkdown(summary Summary) string {
	var b strings.Builder
	b.WriteString("# Owner Breakdown\n\n")
	b.WriteString("## Services\n\n")
	writeTable(&b, summary.Services)
	return b.String()
}

func writeTable(b *strings.Builder, rows []Row) {
	b.WriteString("| Name | Count |\n")
	b.WriteString("| --- | ---: |\n")
	for _, row := range rows {
		fmt.Fprintf(b, "| %s | %d |\n", row.Name, row.Count)
	}
}

func sortedRows(values map[string]int) []Row {
	rows := make([]Row, 0, len(values))
	for name, count := range values {
		rows = append(rows, Row{Name: name, Count: count})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count == rows[j].Count {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Count > rows[j].Count
	})
	return rows
}
EOF

cat >"$target/internal/report/report_test.go" <<'EOF'
package report

import (
	"strings"
	"testing"

	"example.com/ownerbreakdown/internal/events"
)

func TestAggregateGroupsOwnersByOwner(t *testing.T) {
	summary := Aggregate([]events.Event{
		{Service: "api", Owner: "platform", Count: 3},
		{Service: "worker", Owner: "platform", Count: 2},
		{Service: "api", Owner: "unassigned", Count: 1},
	})
	want := []Row{{Name: "platform", Count: 5}, {Name: "unassigned", Count: 1}}
	if len(summary.Owners) != len(want) {
		t.Fatalf("Owners length = %d, want %d: %#v", len(summary.Owners), len(want), summary.Owners)
	}
	for i := range want {
		if summary.Owners[i] != want[i] {
			t.Fatalf("Owners[%d] = %#v, want %#v", i, summary.Owners[i], want[i])
		}
	}
}

func TestRenderMarkdownIncludesOwnersTable(t *testing.T) {
	out := RenderMarkdown(Summary{
		Services: []Row{{Name: "api", Count: 4}},
		Owners:   []Row{{Name: "platform", Count: 4}},
	})
	for _, needle := range []string{"## Services", "| api | 4 |", "## Owners", "| platform | 4 |"} {
		if !strings.Contains(out, needle) {
			t.Fatalf("markdown missing %q:\n%s", needle, out)
		}
	}
}
EOF

cat >"$target/cmd/owner-report/main.go" <<'EOF'
package main

import (
	"fmt"
	"os"

	"example.com/ownerbreakdown/internal/events"
	"example.com/ownerbreakdown/internal/report"
)

func main() {
	rows, err := events.ParseCSV(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(report.RenderMarkdown(report.Aggregate(rows)))
}
EOF

git -C "$target" init -q -b main
git -C "$target" add .
GIT_AUTHOR_NAME="Agent Analyzer Benchmark" \
GIT_AUTHOR_EMAIL="benchmark@example.invalid" \
GIT_AUTHOR_DATE="2026-05-24T00:00:00Z" \
GIT_COMMITTER_NAME="Agent Analyzer Benchmark" \
GIT_COMMITTER_EMAIL="benchmark@example.invalid" \
GIT_COMMITTER_DATE="2026-05-24T00:00:00Z" \
git -C "$target" commit -q -m "Seed owner-breakdown benchmark target"
git -C "$target" tag -f "$tag" >/dev/null
commit="$(git -C "$target" rev-parse "$tag")"

cat >"$target/.benchmark-target.json" <<EOF
{
  "schema_version": "2026-06-04",
  "target": "owner-breakdown",
  "purpose": "Reproducible local Go benchmark target for Agent Analyzer token-saving A/B suites.",
  "stable_ref": "$tag",
  "commit": "$commit",
  "task_prompt": "docs/benchmarks/fixtures/tasks/owner-breakdown-v3-noisy.txt",
  "quality_command": "go test ./...",
  "expected_base_quality": "failing tests until the benchmark agent implements owner normalization, owner aggregation, and markdown owner table rendering"
}
EOF

printf '%s\n' "$target"
printf '%s\n' "$commit"
