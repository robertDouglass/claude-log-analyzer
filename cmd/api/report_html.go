package main

import (
	"bytes"
	"errors"
	"fmt"
	htmlstd "html"
	"html/template"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/priivacy-ai/agent-log-analyzer/internal/analyzer"
	"github.com/priivacy-ai/agent-log-analyzer/internal/app"
)

type reportPageData struct {
	Report            analyzer.Report
	Job               app.Job
	ArtifactURL       string
	ExtendedReportURL string
	StatusText        string
	ReportToken       string
}

func reportPageHandler(store app.APIStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		job, err := store.GetJob(r.PathValue("id"))
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid job id")
			return
		}
		if !tokenMatches(job.ReportTokenHash, r.PathValue("token")) {
			writeError(w, http.StatusUnauthorized, "invalid report token")
			return
		}
		if job.Status != app.StatusCompleted {
			renderReportStatusPage(w, job)
			return
		}
		report, err := store.GetReport(job.ID)
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "report not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid report id")
			return
		}
		artifactURL := ""
		if jobAllowsPluginArtifact(job) {
			artifactURL = publicBaseURL(r) + "/api/public-artifacts/" + job.ID + "/" + r.PathValue("token") + "/plugin.zip"
		}
		extendedURL := publicBaseURL(r) + "/api/public-reports/" + job.ID + "/" + r.PathValue("token") + "/download.zip"
		renderReportHTML(w, reportPageData{
			Report:            report,
			Job:               job,
			ArtifactURL:       artifactURL,
			ExtendedReportURL: extendedURL,
			StatusText:        "Download your custom skills and save tokens now.",
			ReportToken:       r.PathValue("token"),
		})
	}
}

func renderReportStatusPage(w http.ResponseWriter, job app.Job) {
	renderReportHTML(w, reportPageData{
		Job:        job,
		StatusText: fmt.Sprintf("This private report link will remain available after completion. Current status: %s.", job.Status),
	})
}

func renderReportHTML(w http.ResponseWriter, data reportPageData) {
	var body bytes.Buffer
	if err := reportHTMLTemplate.Execute(&body, data); err != nil {
		writeError(w, http.StatusInternalServerError, "could not render report")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body.Bytes())
}

var reportHTMLTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"add":                       func(a, b int) int { return a + b },
	"actionPlan":                actionPlanHTML,
	"boolText":                  boolText,
	"bucketValue":               bucketValue,
	"ecosystemPanel":            ecosystemPanelHTML,
	"environmentSignals":        environmentSignalsHTML,
	"findingEvidence":           findingEvidence,
	"findingsBubbleChart":       findingsBubbleChartHTML,
	"formatInt":                 formatInt,
	"formatTokens":              formatTokens,
	"helpTip":                   helpTip,
	"join":                      joinStrings,
	"logRefs":                   logRefsHTML,
	"mapLines":                  mapLines,
	"recommendationAction":      recommendationAction,
	"recommendationLabel":       recommendationLabel,
	"recommendationName":        recommendationName,
	"recommendationPluginPitch": recommendationPluginPitch,
	"recommendationProblem":     recommendationProblem,
	"recommendationPurpose":     recommendationPurpose,
	"recommendationReason":      recommendationReason,
	"recommendationSignalLabel": recommendationSignalLabel,
	"recommendationSignals":     recommendationSignals,
	"recommendationURL":         recommendationURL,
	"recommendationVerdict":     recommendationVerdict,
	"evidenceTierLabel":         evidenceTierLabel,
	"validationDeltaPct":        validationDeltaPct,
	"validationWarningLabel":    validationWarningLabel,
	"receiptPanel":              receiptPanelHTML,
	"savingsRange":              savingsRange,
	"sourceLogLabel":            sourceLogLabel,
	"staticAssetPath":           staticAssetPath,
	"timelineChart":             timelineChartHTML,
}).Parse(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <meta name="robots" content="noindex,nofollow,noarchive" />
    <meta name="description" content="Private Agent Analyzer deterministic report generated from sanitized local analysis JSON." />
    <meta name="application-name" content="Agent Analyzer" />
    <meta name="theme-color" content="#0d1117" />
    <meta property="og:type" content="website" />
    <meta property="og:site_name" content="Agent Analyzer" />
    <meta property="og:title" content="Private Agent Analyzer Report" />
    <meta property="og:description" content="Private Agent Analyzer deterministic report generated from sanitized local analysis JSON." />
    <meta name="twitter:card" content="summary" />
    <meta name="twitter:title" content="Private Agent Analyzer Report" />
    <meta name="twitter:description" content="Private deterministic report generated from sanitized local Agent Analyzer JSON." />
    {{if ne .Job.Status "completed"}}<meta http-equiv="refresh" content="2" />{{end}}
    <title>Agent Analyzer Report</title>
    <link rel="icon" href="/favicon.ico" sizes="any" />
    <link rel="icon" href="/favicon-32x32.png" type="image/png" sizes="32x32" />
    <link rel="icon" href="/favicon-16x16.png" type="image/png" sizes="16x16" />
    <link rel="apple-touch-icon" href="/apple-touch-icon.png" sizes="180x180" />
    <link rel="manifest" href="/site.webmanifest" />
    <link rel="preload" href="{{staticAssetPath "vendor/tippy/tippy.css"}}" as="style" onload="this.onload=null;this.rel='stylesheet'" />
    <noscript><link rel="stylesheet" href="{{staticAssetPath "vendor/tippy/tippy.css"}}" /></noscript>
    <link rel="stylesheet" href="{{staticAssetPath "styles.css"}}" />
  </head>
  <body>
    <main class="shell">
      <section class="report" id="report">
        <p class="expiry" id="report-status">{{if eq .Job.Status "completed"}}<a href="#download-report-section">{{.StatusText}}</a>{{else}}{{.StatusText}}{{end}}</p>
        {{if eq .Job.Status "completed"}}
        <div class="zero-token-banner">
          <strong>0 model tokens used to generate this report.</strong>
          <span>Agent Analyzer uses deterministic local parsing and server-side rendering, not an LLM reading your logs. {{helpTip "How can report generation cost zero model tokens? The CLI parses local logs with deterministic Go code, writes sanitized JSON, and the hosted page renders that JSON. No raw transcript is sent to a model and no model call is made to produce this report."}}</span>
        </div>
        <section class="savings-hero" aria-labelledby="savings-hero-title">
          <div class="savings-copy">
          <p class="eyebrow">custom token recovery target</p>
          <h2 id="savings-hero-title"><span id="savings-percent">{{.Report.EstimatedWaste.Low}}-{{.Report.EstimatedWaste.High}}%</span> less waste, targeted by your custom skills {{helpTip "How is waste estimated? This is the estimated token range our generated plugin can plausibly help recover. Repeated-read guardrails, retry-loop stops, tool-output caps, context-boundary guidance, and cache hygiene each have bounded capture rates; the range is directional, not provider billing."}}</h2>
          <p id="waste" class="savings-token-range">{{savingsRange .Report.Metrics.EstimatedTokens .Report.EstimatedWaste}} estimated tokens are addressable from the sessions you analyzed.</p>
          <p class="command-note">Analyzed token volume: {{formatTokens .Report.Metrics.EstimatedTokens}} estimated input/output tokens; {{formatTokens .Report.Metrics.ToolOutputTokens}} estimated from tool output. {{helpTip "What is counted here? Accuracy depends on the source log. When native usage fields exist, we use them. Otherwise we estimate roughly one token per four characters. Tool-output volume is derived from tool-result payload size and similar estimates. This is directional, not invoice-grade accounting."}}</p>
          <p class="capacity-note">Analyze locally, download the custom skills generated from your own logs, then apply the exact guards for the token leaks Agent Analyzer found.</p>
          <div class="report-cta-row" aria-label="Report actions">
            <a class="report-primary-cta" href="#download-report-section">Download report pack</a>
            <a class="report-secondary-cta" href="#plugin-purchase">Download custom skills</a>
          </div>
          </div>
          <div class="score">
            <span id="score">{{.Report.Score}}</span>
            <small>efficiency score {{helpTip "What does this score mean? Efficiency is 100 minus the high end of estimated plugin-addressable token savings. The estimate is deterministic: each finding gets a bounded token-impact estimate and a conservative plugin capture range, then totals are capped at 65% to avoid promising perfect recovery."}}</small>
          </div>
        </section>
        <div class="problem-section">
          <h2>Top Problems {{helpTip "Why are these bubbles different sizes? Bubble size is estimated plugin-addressable token savings for that finding. Tool-output and cache issues use token fields when available; rereads, retries, and context spikes use bounded count-scaled estimates multiplied by conservative plugin capture rates."}}</h2>
          {{findingsBubbleChart .Report}}
        </div>
        {{if not .Report.SourceReports}}
        <div>
          <h2>Session Timeline {{helpTip "What are the turns? Timeline points are sampled from parsed log rows, usually every ten rows plus the final row. Token height is estimated context/token volume at that point, not a guaranteed exact provider context window."}}</h2>
          <p id="timeline-caption" class="timeline-caption">Estimated context/token volume by parsed turn. Green overlay = estimated tokens you can save.</p>
          {{timelineChart .Report.Timeline .Report.EstimatedWaste}}
        </div>
        {{end}}
        {{if .Report.SourceReports}}
        <section class="source-reports">
          <h2>Agent Logs Analyzed {{helpTip "Which sessions were analyzed? Each section is built from logs parsed locally for that supported agent source. Full local paths and raw session IDs are intentionally not uploaded; the report shows privacy-safe local references instead."}}</h2>
          {{range .Report.SourceReports}}
          <article class="source-report">
            <header class="source-report-header">
              <div>
                <h3>{{.SourceLabel}}</h3>
                <p>{{sourceLogLabel .LogCount}} analyzed locally</p>
                {{logRefs .LogRefs}}
              </div>
              <div class="source-score">
                <strong>{{.Score}}</strong>
                <span>efficiency {{helpTip "Source efficiency uses the same plugin-addressable savings model: 100 minus the high end of estimated token savings for this agent source."}}</span>
              </div>
            </header>
            <div class="source-report-grid">
              <div>
                <h4>Waste {{helpTip "Source-level waste is the plugin-addressable savings estimate for this agent source only, using the same bounded capture-rate model as the full report."}}</h4>
                <p>{{.EstimatedWaste.Low}}-{{.EstimatedWaste.High}}% avoidable token spend</p>
                <p class="command-note">{{formatTokens .Metrics.EstimatedTokens}} estimated input/output tokens; {{formatTokens .Metrics.ToolOutputTokens}} estimated tool-output tokens; {{formatInt .Metrics.Rereads}} rereads; {{formatInt .Metrics.FailedCommands}} retries. {{helpTip "What is counted for this agent? Token counts use native usage fields when available and fall back to rough text-size estimates. Rereads and retries are deterministic pattern detections over sanitized local parse output."}}</p>
              </div>
              <div>
                <h4>Top Problems</h4>
                <ol class="source-findings">
                  {{range .Findings}}
                  <li><strong>{{.Title}}</strong><span>{{.Severity}} - {{.CostImpact}}</span></li>
                  {{else}}
                  <li>No major deterministic problems detected.</li>
                  {{end}}
                </ol>
              </div>
            </div>
            {{if .Timeline}}
            <p class="timeline-caption">Estimated context/token volume by parsed turn for this source. Green overlay = estimated tokens you can save. {{helpTip "How should I read this chart? This source timeline is based on parsed log-row order, sampled for readability. It should be used to spot growth shape and spikes, not exact provider billing."}}</p>
            {{timelineChart .Timeline .EstimatedWaste}}
            {{else}}
            <p class="timeline-caption">Per-turn timeline unavailable for this aggregated source. Totals above cover {{sourceLogLabel .LogCount}}.</p>
            {{end}}
          </article>
          {{end}}
        </section>
        {{end}}
        <section class="plugin-pitch" id="plugin-pitch">
          <div>
            <p class="eyebrow">generated remediation</p>
            <h2>Copy quick fixes now. Download the plugin built for your leaks. {{helpTip "Where do these fixes come from? Fixes are generated from deterministic finding IDs and bounded evidence, not from raw prompts or an LLM reading your transcript. The plugin packages those findings into Claude-facing operating guidance and vetted setup instructions."}}</h2>
            <p>Add the relevant AGENTS.md lines now. The generated plugin packages your own report into benchmark-backed operating guidance so future sessions spend more of your plan writing software and less on the rereads, retries, dead context, and noisy tools found in your logs.</p>
            <ul class="plugin-benefits">
              <li>Session hygiene nudges.</li>
              <li>Scoped retrieval recommendations.</li>
              <li>Output-budgeted command habits.</li>
              <li>No unproven reducer installs.</li>
            </ul>
            <a class="plugin-cta" href="#plugin-purchase">Download custom skills</a>
          </div>
          <div class="plugin-fixes-card">
            <h3>Copy-ready AGENTS.md lines</h3>
            <p class="command-note">Use only the lines that match how you work.</p>
            {{actionPlan .Report}}
          </div>
        </section>
        {{if .Report.Recommendation}}
        <section id="recommendation-section" class="intel-section">
          <h2>Recommended tools to address waste {{helpTip "Why this recommendation? Ranking comes from public allowlisted tool metadata and deterministic signals such as tool-output bloat, retrieval friction, usage visibility, and MCP/skill utilization. Unknown private names are not echoed."}}</h2>
          <p class="section-note">These are not random installs. Telemetry tools are labeled as measurement only, and reducers are recommended only when our repeated runs showed savings for the matching waste signal. Use the generated plugin to turn the report into setup instructions.</p>
          <div class="recommendation-cta-row">
            <a class="plugin-cta" href="#plugin-purchase">Download custom skills</a>
            <a class="recommendation-allowlist-link" href="/allowed-tools.html">Review vetted allowlist</a>
            <a class="recommendation-allowlist-link" href="/proof/results.html">Review benchmark results</a>
          </div>
          {{with .Report.Recommendation.Primary}}{{template "recommendation" .}}{{end}}
          {{with .Report.Recommendation.Secondary}}{{template "recommendation" .}}{{end}}
          {{if and (not .Report.Recommendation.Primary) (not .Report.Recommendation.Secondary)}}
          <p id="recommendation-empty">No high-priority recommendation detected from current signals.</p>
          {{end}}
        </section>
        {{end}}
        {{with .Report.SavingsValidation}}
        <section id="savings-validation-section" class="intel-section savings-validation-section">
          <h2>Savings validation {{helpTip "Follow-up mode compares a later sanitized report against a baseline receipt. Natural logs can show reducer activity and normalized metric movement; controlled A/B benchmarks are required for causal proof."}}</h2>
          <p class="section-note">{{evidenceTierLabel .EvidenceTier}}: {{.Summary}}</p>
          <div class="validation-metrics">
            {{range .MetricDeltas}}
            <div class="validation-metric">
              <span>{{.Label}}</span>
              <strong class="{{if .Improved}}validation-improved{{else}}validation-worse{{end}}">{{validationDeltaPct .}}</strong>
            </div>
            {{end}}
          </div>
          {{if .Warnings}}
          <ul class="validation-warnings">
            {{range .Warnings}}<li>{{validationWarningLabel .}}</li>{{end}}
          </ul>
          {{end}}
        </section>
        {{end}}
        <section class="evidence-grid">
          <div class="evidence-card environment-card">
            <h2>Environment signals {{helpTip "This condenses bounded ecosystem fingerprints, MCP utilization, and skill utilization into the few signals that affect token waste. Unknown/private names are counted without exposing raw names."}}</h2>
            {{environmentSignals .Report}}
          </div>
          <div class="evidence-card receipt-card">
            <h2>Security Receipt {{helpTip "What happened to my raw logs? The public flow analyzes and redacts locally, then uploads sanitized report JSON. This receipt records the report's own privacy flags and local redaction counts; it is not a third-party audit."}}</h2>
            {{receiptPanel .Report}}
          </div>
        </section>
        <div class="upsell" id="download-report-section">
          <div class="upsell-copy">
          <p class="eyebrow">portable findings</p>
          <h2>Download the report pack and your custom plugin for free</h2>
          <p class="upsell-lede">The report pack shows where your own agent sessions are leaking tokens, includes the benchmark methodology and results, and gives you a partner voucher. The generated plugin turns those specific findings into setup rules and workflow nudges using only reducers that earned scoped recommendations.</p>
          <ul class="upsell-proof">
            <li>Raw transcripts stay local.</li>
            <li>Email required: enter it once to unlock both downloads and receive the links.</li>
            <li><a href="/proof/methodology.html">Benchmark methodology and primary data</a>.</li>
          </ul>
          </div>
          <div class="upsell-action" id="plugin-purchase">
          <form class="email-unlock-form" action="/api/report-deliveries" method="post">
            <input type="hidden" name="source_report_job_id" value="{{.Job.ID}}" />
            <input type="hidden" name="source_report_token" value="{{.ReportToken}}" />
            <label>Email for report pack + generated plugin
              <input type="email" name="email" placeholder="you@example.com" required />
            </label>
            <label class="checkbox-row">
              <input type="checkbox" name="marketing_opt_in" value="1" />
              <span>Send me occasional updates about the upcoming Spec Kitty Teamspace launch and agentic coding training.</span>
            </label>
            <button class="plugin-cta" type="submit">Unlock my custom plugin</button>
            <p class="command-note">The report pack and generated plugin are free. After submit, this page shows both download buttons and emails the links, the Spec Kitty training voucher reminder, and the Spec Kitty GitHub repo. Raw transcripts are not attached or uploaded.</p>
          </form>
          </div>
        </div>
        {{else}}
        <div class="score">
          <span id="score">--</span>
          <small>efficiency score</small>
        </div>
        <p>This page will show the deterministic report after analysis completes. Browser clients can poll for completion; non-JS clients should retry this URL.</p>
        {{end}}
      </section>
    </main>
    <script src="{{staticAssetPath "vendor/tippy/popper.min.js"}}" defer></script>
    <script src="{{staticAssetPath "vendor/tippy/tippy-bundle.umd.min.js"}}" defer></script>
    <script src="{{staticAssetPath "tooltips.js"}}" defer></script>
    <script src="{{staticAssetPath "report-actions.js"}}" defer></script>
  </body>
</html>
{{define "recommendation"}}
<div class="recommendation-card">
  <div class="recommendation-header">
    <div>
      <span class="recommendation-kicker">{{recommendationProblem .}}</span>
      <div class="recommendation-tool">{{recommendationName .}}</div>
    </div>
    <span class="recommendation-verdict">{{recommendationVerdict .}}</span>
  </div>
  <p class="recommendation-purpose">{{recommendationPurpose .}}</p>
  <div class="recommendation-next-step">
    <strong>{{recommendationAction .}}</strong>
    <span>{{recommendationPluginPitch .}}</span>
  </div>
  <div class="recommendation-meta">
    <span class="recommendation-reason">{{recommendationReason .}}</span>
    <span class="recommendation-confidence">{{.Confidence}} confidence</span>
    <span class="recommendation-risk">{{.RiskLevel}} install risk</span>
    {{with recommendationURL .}}<a class="recommendation-source" href="{{.}}" rel="noopener noreferrer">Source</a>{{end}}
  </div>
  <ul class="recommendation-signals">
    {{range .SignalIDs}}<li class="recommendation-signal">{{recommendationSignalLabel .}}</li>{{end}}
  </ul>
</div>
{{end}}`))

func recommendationName(rec analyzer.TokenSavingRecommendation) string {
	if rec.PrimaryToolName != "" {
		return rec.PrimaryToolName
	}
	if rec.PrimaryToolID != "" {
		return string(rec.PrimaryToolID)
	}
	return recommendationLabel(rec)
}

func recommendationURL(rec analyzer.TokenSavingRecommendation) string {
	if strings.HasPrefix(rec.PrimaryToolURL, "https://") {
		return rec.PrimaryToolURL
	}
	return ""
}

func recommendationProblem(rec analyzer.TokenSavingRecommendation) string {
	for _, signal := range rec.SignalIDs {
		switch signal {
		case analyzer.SignalNoUsageVisibility:
			return "You lack usage visibility"
		case analyzer.SignalRepeatedFileReads, analyzer.SignalBroadRepoExploration, analyzer.SignalUnchangedFileRereads:
			return "Your agent is rereading too much"
		case analyzer.SignalToolOutputBloat, analyzer.SignalShellOutputBloat, analyzer.SignalMCPToolOutputBloat:
			return "Tool output is flooding context"
		case analyzer.SignalMCPSkillBloat:
			return "Your tool surface may be too wide"
		case analyzer.SignalRetryLoop, analyzer.SignalContextGrowthSpikes:
			return "Session hygiene is degrading"
		case analyzer.SignalOutputVerbosity:
			return "Assistant output is accumulating"
		}
	}
	return "Tooling gap detected"
}

func recommendationPurpose(rec analyzer.TokenSavingRecommendation) string {
	switch rec.PrimaryToolID {
	case "ccusage":
		return "ccusage is measurement, not a reducer: it gives independent token, cache, and cost accounting so you can verify whether changes are working."
	case "ccstatusline":
		return "ccstatusline is awareness, not a reducer: keep it outside the prompt path so cost and context drift are visible without adding task overhead."
	case "context_mode":
		return "Context Mode compresses or externalizes noisy tool output before it pollutes future turns."
	case "claude_context":
		return "claude-context is not a default recommendation from our runs: it added overhead on the current fixture and needs a larger retrieval-amortization task before promotion."
	case "claude_token_efficient":
		return "claude-token-efficient showed only a small, noisy saving in our 3x runs, so we treat it as manual verbosity hygiene rather than a default install."
	case "claude_code_hooks_mastery":
		return "Claude Code Hooks Mastery is a reference set for deterministic hooks that can enforce session hygiene."
	case "rtk":
		return "RTK is a high-risk shell-output reducer candidate. Only consider the linked rtk-ai/rtk project, not unrelated packages with the same name."
	case "semble":
		return "Semble earned a scoped retrieval recommendation in our 3x runs when path-limited search replaced broad repeated file reads."
	case "squeez":
		return "Squeez earned a scoped shell/log compression recommendation; use it explicitly for noisy command output, not as a general reasoning-token reducer."
	}
	for _, signal := range rec.SignalIDs {
		switch signal {
		case analyzer.SignalMCPSkillBloat:
			return "This is not a request to install another tool. First prune or lazy-load MCPs and skills that are exposed but rarely used."
		case analyzer.SignalRetryLoop, analyzer.SignalContextGrowthSpikes:
			return "This is a workflow recommendation: add rules that make the agent stop, compact, or split the session before degradation compounds."
		case analyzer.SignalRepeatedFileReads, analyzer.SignalBroadRepoExploration, analyzer.SignalUnchangedFileRereads:
			return "Retrieval tooling can reduce repeated file reads by giving the agent a narrower way to locate relevant code."
		case analyzer.SignalNoUsageVisibility:
			return "Usage visibility tools make token burn and cache behavior visible so you can tell whether changes are working."
		}
	}
	return "This recommendation is matched from a vetted allowlist against deterministic waste signals in the report."
}

func recommendationAction(rec analyzer.TokenSavingRecommendation) string {
	if rec.PrimaryToolID == "" {
		return "Do not install anything yet."
	}
	switch rec.RiskLevel {
	case analyzer.RiskHigh:
		return "Review the source and prefer plugin-generated setup instructions."
	case analyzer.RiskMedium:
		return "Review the source first; prefer plugin-generated setup instructions."
	default:
		return "Review the source, or let the plugin configure the right path from this report."
	}
}

func recommendationPluginPitch(rec analyzer.TokenSavingRecommendation) string {
	if rec.PrimaryToolID == "" {
		return "The plugin can convert this into concrete config cleanup rules from your full history."
	}
	return "The plugin packages vetted recommendations and avoids one-off manual setup guesses."
}

func recommendationVerdict(rec analyzer.TokenSavingRecommendation) string {
	if rec.PrimaryToolID == "" {
		return "Prune first"
	}
	if rec.RiskLevel == analyzer.RiskHigh {
		return "Careful review"
	}
	return "Candidate"
}

func recommendationReason(rec analyzer.TokenSavingRecommendation) string {
	switch rec.Reason {
	case analyzer.ReasonAbsent:
		return "Not detected yet"
	case analyzer.ReasonInstalledInactive:
		return "Installed but inactive"
	case analyzer.ReasonConfiguredInactive:
		return "Configured but inactive"
	case analyzer.ReasonPruneFirst:
		return "Prune first"
	case analyzer.ReasonAuditConfig:
		return "Audit config"
	case analyzer.ReasonServerQuotaCheck:
		return "Check quota config"
	case analyzer.ReasonRejectedAlternative:
		return "Previously rejected"
	case analyzer.ReasonActivePersistent:
		return "Already active"
	default:
		return string(rec.Reason)
	}
}

func recommendationSignalLabel(signal analyzer.Signal) string {
	switch signal {
	case analyzer.SignalNoUsageVisibility:
		return "No usage visibility"
	case analyzer.SignalToolOutputBloat:
		return "Tool output bloat"
	case analyzer.SignalShellOutputBloat:
		return "Shell output bloat"
	case analyzer.SignalMCPToolOutputBloat:
		return "MCP output bloat"
	case analyzer.SignalRepeatedFileReads:
		return "Repeated file reads"
	case analyzer.SignalBroadRepoExploration:
		return "Broad repo exploration"
	case analyzer.SignalUnchangedFileRereads:
		return "Unchanged rereads"
	case analyzer.SignalMCPSkillBloat:
		return "MCP / skill bloat"
	case analyzer.SignalOutputVerbosity:
		return "Output verbosity"
	case analyzer.SignalRetryLoop:
		return "Retry loop"
	case analyzer.SignalContextGrowthSpikes:
		return "Context growth spikes"
	default:
		return string(signal)
	}
}

func recommendationLabel(rec analyzer.TokenSavingRecommendation) string {
	for _, signal := range rec.SignalIDs {
		switch signal {
		case analyzer.SignalMCPSkillBloat:
			return "Prune / lazy-load MCPs and skills"
		case analyzer.SignalRetryLoop, analyzer.SignalContextGrowthSpikes:
			return "Session hygiene audit"
		}
	}
	if rec.PrimaryToolID == "" {
		return "Tooling recommendation"
	}
	return "Install or configure this tool only after reviewing the source URL."
}

func recommendationSignals(rec analyzer.TokenSavingRecommendation) string {
	signals := make([]string, 0, len(rec.SignalIDs))
	for _, signal := range rec.SignalIDs {
		signals = append(signals, string(signal))
	}
	return joinStrings(signals)
}

func evidenceTierLabel(tier string) string {
	switch tier {
	case "verified":
		return "Verified reducer activity"
	case "observed":
		return "Observed improvement"
	case "proven":
		return "Controlled benchmark proof"
	default:
		return "Inconclusive"
	}
}

func validationDeltaPct(delta analyzer.SavingsMetricDelta) string {
	prefix := ""
	if delta.DeltaPct > 0 {
		prefix = "+"
	}
	return fmt.Sprintf("%s%d%%", prefix, delta.DeltaPct)
}

func validationWarningLabel(warning string) string {
	switch warning {
	case "small_log_sample":
		return "Small log sample."
	case "source_mix_changed":
		return "Source mix changed between baseline and follow-up."
	case "workload_volume_changed":
		return "Workload volume changed between baseline and follow-up."
	case "missing_native_token_counters":
		return "Native token counters were missing in one window."
	default:
		return warning
	}
}

func findingEvidence(e analyzer.FindingEvidence) string {
	var parts []string
	if e.Count > 0 {
		parts = append(parts, fmt.Sprintf("count: %d", e.Count))
	}
	if e.TokenShare > 0 {
		parts = append(parts, fmt.Sprintf("token share: %d%%", e.TokenShare))
	}
	if len(e.TopFiles) > 0 {
		parts = append(parts, "top files: "+joinStrings(e.TopFiles))
	}
	if e.Description != "" {
		parts = append(parts, e.Description)
	}
	if len(parts) == 0 {
		return "deterministic evidence recorded"
	}
	return strings.Join(parts, " | ")
}

func joinStrings(values []string) string {
	if len(values) == 0 {
		return "none detected"
	}
	return strings.Join(values, ", ")
}

func boolText(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

type actionCopy struct {
	Title      string
	Now        string
	Why        string
	AgentsLine string
	Plugin     string
}

func actionPlanHTML(report analyzer.Report) template.HTML {
	var b strings.Builder
	b.WriteString(`<ul id="fixes" class="action-list">`)
	if len(report.Findings) > 0 {
		limit := len(report.Findings)
		if limit > 4 {
			limit = 4
		}
		for _, finding := range report.Findings[:limit] {
			actionItemHTML(&b, actionForFinding(finding))
		}
		b.WriteString(`</ul>`)
		return template.HTML(b.String())
	}
	if len(report.ImmediateFixes) > 0 {
		limit := len(report.ImmediateFixes)
		if limit > 4 {
			limit = 4
		}
		for _, fix := range report.ImmediateFixes[:limit] {
			actionItemHTML(&b, actionCopy{
				Title:  "Apply the detected fix",
				Now:    fix,
				Plugin: "The plugin turns recurring fixes into operating guidance instead of a one-off note.",
			})
		}
		b.WriteString(`</ul>`)
		return template.HTML(b.String())
	}
	actionItemHTML(&b, actionCopy{
		Title:  "No urgent manual fix detected",
		Now:    "Keep sessions scoped and avoid pasting unnecessary tool output.",
		Plugin: "Download the report pack or use the plugin if you want these rules packaged for future sessions.",
	})
	b.WriteString(`</ul>`)
	return template.HTML(b.String())
}

func actionItemHTML(b *strings.Builder, action actionCopy) {
	fmt.Fprintf(
		b,
		`<li class="action-item"><div class="action-main"><strong>%s</strong><span>%s</span><small>%s</small></div><code>%s</code><button type="button" class="copy-agents-line" data-copy="%s">Copy line</button></li>`,
		htmlstd.EscapeString(action.Title),
		htmlstd.EscapeString(action.Now),
		htmlstd.EscapeString(defaultString(action.Why, "deterministic evidence recorded")),
		htmlstd.EscapeString(defaultString(action.AgentsLine, "Keep agent sessions scoped and avoid unnecessary context.")),
		htmlstd.EscapeString(defaultString(action.AgentsLine, "Keep agent sessions scoped and avoid unnecessary context.")),
	)
}

func actionForFinding(finding analyzer.Finding) actionCopy {
	switch finding.ID {
	case "repeated_file_reads":
		return actionCopy{
			Title:      "Stop rereading files blindly",
			Now:        "Before another broad read, name the exact file or symbol and ask the agent to summarize only what changed since the last read.",
			Why:        findingEvidence(finding.Evidence),
			AgentsLine: "Before rereading files, summarize known state and prefer targeted symbol searches or narrow line ranges over whole-file reads.",
			Plugin:     "The plugin packages repeated-path patterns from this scan into retrieval hygiene prompts.",
		}
	case "tool_output_bloat":
		return actionCopy{
			Title:      "Cap noisy command output",
			Now:        "Use rg filters, head/tail, --json summaries, or redirect logs to a file. Paste only the failing excerpt back into context.",
			Why:        findingEvidence(finding.Evidence),
			AgentsLine: "Cap shell command output by default; use focused filters and paste only the relevant failing excerpt back into context.",
			Plugin:     "The plugin can recommend shell-output reducers and context-safe command habits for your setup.",
		}
	case "retry_loop", "args_hashed_retry_loop":
		return actionCopy{
			Title:      "Break retry loops after two misses",
			Now:        "After two similar failures, stop editing. Restate the invariant, inspect the diff/test output, then restart with a smaller scope.",
			Why:        findingEvidence(finding.Evidence),
			AgentsLine: "After two failed attempts on the same issue, stop, inspect the invariant and latest error, then restart with a smaller scope.",
			Plugin:     "The plugin turns recurring retry signatures into session hygiene rules.",
		}
	case "context_growth_spikes", "cache_invalidation_spike":
		return actionCopy{
			Title:      "Treat context spikes as boundaries",
			Now:        "Use /compact or start a fresh session after large tool output, model/config changes, or a pivot from debugging to architecture.",
			Why:        findingEvidence(finding.Evidence),
			AgentsLine: "Treat major tool-output, model/config changes, and task pivots as context boundaries; compact or split the session before continuing.",
			Plugin:     "The plugin adds compact/split/restart nudges at the points your history shows degradation.",
		}
	case "mcp_bloat_high", "mcp_bloat_severe":
		return actionCopy{
			Title:      "Disable unused MCPs by default",
			Now:        "Move project-specific MCPs out of global config and lazy-load heavy servers only when the task needs them.",
			Why:        findingEvidence(finding.Evidence),
			AgentsLine: "Keep only frequently used MCP servers enabled by default; lazy-load project-specific or heavy MCPs when the task requires them.",
			Plugin:     "The plugin converts MCP bloat into a concrete setup checklist.",
		}
	case "skill_bloat_high", "skill_bloat_severe":
		return actionCopy{
			Title:      "Trim always-on skills",
			Now:        "Keep only high-use skills active by default. Move rarely used skills behind explicit invocation.",
			Why:        findingEvidence(finding.Evidence),
			AgentsLine: "Keep only high-signal skills in default context; invoke rare or project-specific skills explicitly when needed.",
			Plugin:     "The plugin can recommend a smaller skill surface from observed usage ratios.",
		}
	default:
		title := finding.Title
		if title == "" {
			title = "Apply the detected fix"
		}
		now := finding.Recommendation
		if now == "" {
			now = "Use a narrower workflow before continuing."
		}
		return actionCopy{
			Title:      title,
			Now:        now,
			Why:        findingEvidence(finding.Evidence),
			AgentsLine: "Keep agent sessions scoped, evidence-backed, and explicit about when context should be compacted or split.",
			Plugin:     "The plugin turns this report into a generated remediation pack.",
		}
	}
}

func workflowFingerprintsHTML(fingerprints []analyzer.EcosystemFingerprint) template.HTML {
	var b strings.Builder
	b.WriteString(`<ol id="workflow-fingerprints-list">`)
	if len(fingerprints) == 0 {
		b.WriteString(`<li class="fingerprint-card"><p class="empty-evidence">No known workflow fingerprints detected.</p></li>`)
		b.WriteString(`</ol>`)
		return template.HTML(b.String())
	}
	for _, fp := range fingerprints {
		b.WriteString(`<li class="fingerprint-card">`)
		b.WriteString(`<div class="fingerprint-header">`)
		fmt.Fprintf(&b, `<strong class="fingerprint-id">%s</strong>`, htmlstd.EscapeString(fp.ID))
		confidence := defaultString(fp.Confidence, "unknown")
		fmt.Fprintf(&b, `<span class="fingerprint-confidence status-chip confidence-%s">%s confidence</span>`, htmlstd.EscapeString(confidence), htmlstd.EscapeString(confidence))
		if fp.Active {
			statusChipHTML(&b, "active", "good")
		}
		if fp.Installed {
			statusChipHTML(&b, "installed", "good")
		}
		if fp.VersionBucket != "" {
			statusChipHTML(&b, "version "+fp.VersionBucket, "")
		}
		b.WriteString(`</div>`)
		b.WriteString(`<div class="fingerprint-body">`)
		factTileHTML(&b, "Evidence", fmt.Sprintf("%d", fp.EvidenceCount))
		sourceGroupsHTML(&b, fp.Sources)
		b.WriteString(`</div></li>`)
	}
	b.WriteString(`</ol>`)
	return template.HTML(b.String())
}

func toolingUtilizationHTML(util analyzer.ToolingUtilization) template.HTML {
	var b strings.Builder
	b.WriteString(`<div id="tooling-utilization-rows">`)
	mcpUtilizationHTML(&b, util.MCP)
	skillUtilizationHTML(&b, util.Skill)
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func environmentSignalsHTML(report analyzer.Report) template.HTML {
	e := report.Ecosystem
	rows := [][3]string{
		{
			"Runtime",
			joinNonEmpty([]string{e.Client, e.OperatingSystem, e.Shell, e.VersionControl}, "unknown"),
			"Basic host and client context for interpreting the scan.",
		},
		{
			"Coding agents",
			summarizeStrings(e.CodingAgents, 4),
			"Which agent surfaces appeared in the local scan.",
		},
		{
			"Workflow tools",
			summarizeFingerprints(e.WorkflowFingerprints),
			"Spec-driven or workflow tooling detected from bounded public fingerprints.",
		},
		{
			"MCP surface",
			summarizeMCP(e.ToolingUtilization.MCP),
			"High exposure with low use is a context-bloat signal.",
		},
		{
			"Skill surface",
			summarizeSkills(e.ToolingUtilization.Skill),
			"Loaded skills should earn their context by being used.",
		},
		{
			"Tooling inventory",
			summarizeInventory(e),
			"Package managers and plugins influence remediation recommendations.",
		},
	}
	var b strings.Builder
	b.WriteString(`<div id="environment-signals" class="environment-panel"><table class="environment-table"><thead><tr><th>Signal</th><th>Summary</th><th>Why it matters</th></tr></thead><tbody>`)
	for _, row := range rows {
		fmt.Fprintf(
			&b,
			`<tr><th scope="row">%s</th><td>%s</td><td>%s</td></tr>`,
			htmlstd.EscapeString(row[0]),
			htmlstd.EscapeString(row[1]),
			htmlstd.EscapeString(row[2]),
		)
	}
	b.WriteString(`</tbody></table></div>`)
	return template.HTML(b.String())
}

func joinNonEmpty(values []string, fallback string) string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return strings.Join(out, " / ")
}

func summarizeStrings(values []string, limit int) string {
	clean := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			clean = append(clean, value)
		}
	}
	if len(clean) == 0 {
		return "none detected"
	}
	if len(clean) <= limit {
		return strings.Join(clean, ", ")
	}
	return strings.Join(clean[:limit], ", ") + fmt.Sprintf(" +%d more", len(clean)-limit)
}

func summarizeFingerprints(fingerprints []analyzer.EcosystemFingerprint) string {
	clean := make([]analyzer.EcosystemFingerprint, 0, len(fingerprints))
	for _, fp := range fingerprints {
		if fp.ID != "" {
			clean = append(clean, fp)
		}
	}
	if len(clean) == 0 {
		return "none detected"
	}
	sort.SliceStable(clean, func(i, j int) bool {
		return clean[i].EvidenceCount > clean[j].EvidenceCount
	})
	shown := make([]string, 0, min(len(clean), 4))
	for _, fp := range clean[:min(len(clean), 4)] {
		flags := []string{}
		if fp.Active {
			flags = append(flags, "active")
		}
		if fp.Installed {
			flags = append(flags, "installed")
		}
		if len(flags) > 0 {
			shown = append(shown, fmt.Sprintf("%s (%s)", fp.ID, strings.Join(flags, ", ")))
			continue
		}
		shown = append(shown, fmt.Sprintf("%s (%s)", fp.ID, defaultString(fp.Confidence, "unknown")))
	}
	if len(clean) > len(shown) {
		return strings.Join(shown, ", ") + fmt.Sprintf(" +%d more", len(clean)-len(shown))
	}
	return strings.Join(shown, ", ")
}

func summarizeMCP(mcp analyzer.MCPUtilization) string {
	band := normalizeBandString(mcp.WarningBand)
	ratio := utilizationRatioText(mcp.ExposureKnown, mcp.UtilizationRatioPct, mcp.InferenceSource)
	called := summarizeStrings(mcp.UniqueKnownCalledIDs, 3)
	unknown := ""
	if mcp.UniqueUnknownCalledCount > 0 {
		unknown = fmt.Sprintf(" +%d unknown", mcp.UniqueUnknownCalledCount)
	}
	return fmt.Sprintf("%s band; %s; %s calls; called %s%s", band, ratio, formatTokens(mcp.CallCount), called, unknown)
}

func summarizeSkills(skill analyzer.SkillUtilization) string {
	band := normalizeBandString(skill.WarningBand)
	ratio := utilizationRatioText(skill.ExposureKnown, skill.UtilizationRatioPct, skill.InferenceSource)
	executed := summarizeStrings(skill.KnownExecutedIDs, 3)
	unknown := ""
	if skill.UnknownExecutedCount > 0 {
		unknown = fmt.Sprintf(" +%d unknown", skill.UnknownExecutedCount)
	}
	return fmt.Sprintf("%s band; %s; %s executions; used %s%s", band, ratio, formatTokens(skill.ExecutedCount), executed, unknown)
}

func summarizeInventory(e analyzer.Ecosystem) string {
	parts := []string{}
	if plugins := summarizeStrings(e.KnownPlugins, 2); plugins != "none detected" {
		parts = append(parts, "plugins: "+plugins)
	}
	if packages := summarizeStrings(e.PackageManagers, 4); packages != "none detected" {
		parts = append(parts, "packages: "+packages)
	}
	if frameworks := summarizeStrings(e.WorkflowFrameworks, 3); frameworks != "none detected" {
		parts = append(parts, "frameworks: "+frameworks)
	}
	if len(parts) == 0 {
		return "no notable inventory"
	}
	return strings.Join(parts, "; ")
}

func mcpUtilizationHTML(b *strings.Builder, mcp analyzer.MCPUtilization) {
	b.WriteString(`<div class="utilization-card" aria-label="MCP: utilization summary">`)
	utilizationHeaderHTML(b, "MCP", normalizeBandString(mcp.WarningBand), utilizationRatioText(mcp.ExposureKnown, mcp.UtilizationRatioPct, mcp.InferenceSource))
	b.WriteString(`<div class="utilization-body">`)
	utilizationGroupHTML(b, "Exposure", [][2]string{
		{"Servers", bucketLabel(mcp.ServerCountBucket)},
		{"Tools", bucketLabel(mcp.ExposedToolCountBucket)},
		{"Context", bucketLabel(mcp.ContextTokenBucket)},
		{"Efficiency", bucketLabel(mcp.ContextEfficiencyBucket)},
	})
	utilizationGroupHTML(b, "Usage", [][2]string{
		{"Calls", fmt.Sprintf("%d", mcp.CallCount)},
		{"Known", fmt.Sprintf("%d", mcp.KnownCallCount)},
		{"Unknown", fmt.Sprintf("%d", mcp.UnknownCallCount)},
	})
	chipPanelHTML(b, "Known called", mcp.UniqueKnownCalledIDs, unknownCountLabelWithSuffix(mcp.UniqueUnknownCalledCount, "unknown called"))
	chipPanelHTML(b, "Inventory", mcp.KnownServerIDs, unknownCountLabelWithSuffix(mcp.UnknownServerCount, "unknown servers"))
	b.WriteString(`</div></div>`)
}

func skillUtilizationHTML(b *strings.Builder, skill analyzer.SkillUtilization) {
	b.WriteString(`<div class="utilization-card" aria-label="Skills: utilization summary">`)
	utilizationHeaderHTML(b, "Skills", normalizeBandString(skill.WarningBand), utilizationRatioText(skill.ExposureKnown, skill.UtilizationRatioPct, skill.InferenceSource))
	b.WriteString(`<div class="utilization-body">`)
	utilizationGroupHTML(b, "Exposure", [][2]string{
		{"Skills", bucketLabel(skill.ExposedCountBucket)},
		{"Context", bucketLabel(skill.ContextTokenBucket)},
		{"Efficiency", bucketLabel(skill.ContextEfficiencyBucket)},
	})
	utilizationGroupHTML(b, "Usage", [][2]string{
		{"Executions", fmt.Sprintf("%d", skill.ExecutedCount)},
		{"Known", fmt.Sprintf("%d", len(skill.KnownExecutedIDs))},
		{"Unknown", fmt.Sprintf("%d", skill.UnknownExecutedCount)},
	})
	chipPanelHTML(b, "Known executed", skill.KnownExecutedIDs, unknownCountLabelWithSuffix(skill.UnknownExecutedCount, "unknown executed"))
	chipPanelHTML(b, "Known exposed", skill.KnownExposedIDs, unknownCountLabelWithSuffix(skill.UnknownExposedCount, "unknown exposed"))
	b.WriteString(`</div></div>`)
}

func utilizationHeaderHTML(b *strings.Builder, label, band, ratio string) {
	b.WriteString(`<header class="utilization-header">`)
	fmt.Fprintf(b, `<strong>%s</strong>`, htmlstd.EscapeString(label))
	b.WriteString(`<div class="utilization-header-meta">`)
	fmt.Fprintf(b, `<span class="band-chip band-%s">%s band</span>`, htmlstd.EscapeString(band), htmlstd.EscapeString(band))
	fmt.Fprintf(b, `<span class="utilization-ratio">%s</span>`, htmlstd.EscapeString(ratio))
	b.WriteString(`</div></header>`)
}

func utilizationGroupHTML(b *strings.Builder, label string, entries [][2]string) {
	fmt.Fprintf(b, `<section class="utilization-group"><h3>%s</h3><div class="fact-grid">`, htmlstd.EscapeString(label))
	for _, entry := range entries {
		factTileHTML(b, entry[0], entry[1])
	}
	b.WriteString(`</div></section>`)
}

func factTileHTML(b *strings.Builder, label, value string) {
	fmt.Fprintf(
		b,
		`<span class="fact-tile"><small>%s</small><strong>%s</strong></span>`,
		htmlstd.EscapeString(label),
		htmlstd.EscapeString(defaultString(value, "unknown")),
	)
}

func chipPanelHTML(b *strings.Builder, label string, values []string, extra string) {
	fmt.Fprintf(b, `<section class="mini-chip-group"><h3>%s</h3><div class="chip-list">`, htmlstd.EscapeString(label))
	if len(values) == 0 {
		chipHTML(b, "none detected", "muted")
	} else {
		for _, value := range values {
			if value != "" {
				chipHTML(b, value, "")
			}
		}
	}
	if extra != "" && !strings.HasPrefix(extra, "0 ") {
		chipHTML(b, extra, "unknown")
	}
	b.WriteString(`</div></section>`)
}

func sourceGroupsHTML(b *strings.Builder, sources []string) {
	b.WriteString(`<div class="fingerprint-source-groups">`)
	grouped := map[string][]string{
		"CLI":           {},
		"Config":        {},
		"Agent surface": {},
		"Other":         {},
	}
	for _, source := range sources {
		grouped[sourceCategory(source)] = append(grouped[sourceCategory(source)], sourceLabel(source))
	}
	order := []string{"CLI", "Config", "Agent surface", "Other"}
	wrote := false
	for _, group := range order {
		values := grouped[group]
		if len(values) == 0 {
			continue
		}
		wrote = true
		chipPanelHTML(b, group, values, "")
	}
	if !wrote {
		chipHTML(b, "no public source markers", "muted")
	}
	b.WriteString(`</div>`)
}

func statusChipHTML(b *strings.Builder, text, tone string) {
	className := "status-chip"
	if tone != "" {
		className += " status-chip-" + tone
	}
	fmt.Fprintf(b, `<span class="%s">%s</span>`, htmlstd.EscapeString(className), htmlstd.EscapeString(text))
}

func normalizeBandString(value string) string {
	switch value {
	case "severe", "high", "watch", "normal":
		return value
	default:
		return "unknown"
	}
}

func utilizationRatioText(exposureKnown bool, ratio int, source string) string {
	if exposureKnown {
		return fmt.Sprintf("%d%% utilization", ratio)
	}
	return "inferred from " + defaultString(source, "unknown exposure")
}

func bucketLabel(value string) string {
	return defaultString(value, "unknown")
}

func unknownCountLabelWithSuffix(count int, suffix string) string {
	return fmt.Sprintf("%d %s", count, suffix)
}

func sourceCategory(source string) string {
	if strings.HasPrefix(source, "cli_") || source == "command_name" {
		return "CLI"
	}
	if strings.HasPrefix(source, "config_") || source == "package_manifest" || source == "hook_config" {
		return "Config"
	}
	if source == "skill_name" || source == "slash_command" || source == "mcp_namespace" {
		return "Agent surface"
	}
	return "Other"
}

func sourceLabel(source string) string {
	labels := map[string]string{
		"cli_binary":        "binary present",
		"cli_version_probe": "version probe",
		"command_name":      "command name",
		"config_dir":        "config directory",
		"config_file":       "config file",
		"package_manifest":  "package manifest",
		"skill_name":        "skill name",
		"slash_command":     "slash command",
		"mcp_namespace":     "MCP namespace",
		"hook_config":       "hook config",
	}
	if label, ok := labels[source]; ok {
		return label
	}
	return strings.ReplaceAll(defaultString(source, "unknown"), "_", " ")
}

func ecosystemPanelHTML(report analyzer.Report) template.HTML {
	e := report.Ecosystem
	var b strings.Builder
	b.WriteString(`<div id="ecosystem" class="ecosystem-panel">`)
	b.WriteString(`<div class="ecosystem-summary">`)
	metricPillHTML(&b, "Client", defaultString(e.Client, "unknown"))
	metricPillHTML(&b, "OS", defaultString(e.OperatingSystem, "unknown"))
	metricPillHTML(&b, "Shell", defaultString(e.Shell, "unknown"))
	metricPillHTML(&b, "Version control", defaultString(e.VersionControl, "unknown"))
	b.WriteString(`</div>`)
	b.WriteString(`<div class="evidence-groups">`)
	chipGroupHTML(&b, "Coding agents", e.CodingAgents, "")
	chipGroupHTML(&b, "Workflow frameworks", e.WorkflowFrameworks, "")
	chipGroupHTML(&b, "MCPs", e.MCPServersKnown, unknownCountLabel(e.UnknownMCPServerCount))
	chipGroupHTML(&b, "Skills", e.KnownSkills, unknownCountLabel(e.UnknownSkillCount))
	chipGroupHTML(&b, "Plugins", e.KnownPlugins, unknownCountLabel(e.UnknownPluginCount))
	chipGroupHTML(&b, "Package managers", e.PackageManagers, "")
	b.WriteString(`</div></div>`)
	return template.HTML(b.String())
}

func receiptPanelHTML(report analyzer.Report) template.HTML {
	receipt := report.SecurityReceipt
	var b strings.Builder
	b.WriteString(`<div id="receipt" class="receipt-panel">`)
	b.WriteString(`<p class="receipt-boundary"><strong>Local redaction boundary:</strong> secrets are removed on your computer before upload. The hosted service receives sanitized report JSON with category counts and placeholders, not the original redacted values.</p>`)
	b.WriteString(`<div class="receipt-status-grid">`)
	receiptTileHTML(&b, "Model tokens for report", "0", "good")
	receiptTileHTML(&b, "Raw transcript to LLM", boolText(receipt.RawTranscriptSentToLLM), receiptTone(!receipt.RawTranscriptSentToLLM))
	receiptTileHTML(&b, "Outbound during analysis", boolText(receipt.OutboundDuringAnalysis), receiptTone(!receipt.OutboundDuringAnalysis))
	rawTTL := defaultString(receipt.RawLogTTL, "unknown")
	ttlTone := "warn"
	if rawTTL == "not uploaded" {
		ttlTone = "good"
	}
	receiptTileHTML(&b, "Raw log TTL", rawTTL, ttlTone)
	redactionTone := "good"
	if receipt.SecretsRedacted > 0 {
		redactionTone = "warn"
	}
	receiptTileHTML(&b, "Secrets redacted locally", fmt.Sprintf("%d", receipt.SecretsRedacted), redactionTone)
	b.WriteString(`</div>`)
	redactionGroupHTML(&b, report.Redactions)
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func metricPillHTML(b *strings.Builder, label, value string) {
	fmt.Fprintf(
		b,
		`<span class="metric-pill"><small>%s</small><strong>%s</strong></span>`,
		htmlstd.EscapeString(label),
		htmlstd.EscapeString(value),
	)
}

func chipGroupHTML(b *strings.Builder, label string, values []string, extra string) {
	fmt.Fprintf(b, `<section class="chip-group"><h3>%s</h3><div class="chip-list">`, htmlstd.EscapeString(label))
	if len(values) == 0 {
		chipHTML(b, "none detected", "muted")
	} else {
		for _, value := range values {
			if value == "" {
				continue
			}
			chipHTML(b, value, "")
		}
	}
	if extra != "" && !strings.HasPrefix(extra, "0 ") {
		chipHTML(b, extra, "unknown")
	}
	b.WriteString(`</div></section>`)
}

func chipHTML(b *strings.Builder, text, tone string) {
	className := "info-chip"
	if tone != "" {
		className += " info-chip-" + tone
	}
	fmt.Fprintf(b, `<span class="%s">%s</span>`, htmlstd.EscapeString(className), htmlstd.EscapeString(text))
}

func receiptTileHTML(b *strings.Builder, label, value, tone string) {
	fmt.Fprintf(
		b,
		`<div class="receipt-tile receipt-tile-%s" aria-label="%s: %s"><small>%s</small><strong>%s</strong></div>`,
		htmlstd.EscapeString(tone),
		htmlstd.EscapeString(label),
		htmlstd.EscapeString(value),
		htmlstd.EscapeString(label),
		htmlstd.EscapeString(value),
	)
}

func redactionGroupHTML(b *strings.Builder, redactions map[string]int) {
	b.WriteString(`<section class="chip-group redaction-group"><h3>Local redaction categories</h3><p>Only these category counts are included in the uploaded report.</p><div class="chip-list">`)
	keys := make([]string, 0, len(redactions))
	for key, value := range redactions {
		if value > 0 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		chipHTML(b, "none detected", "muted")
	} else {
		for _, key := range keys {
			chipHTML(b, fmt.Sprintf("%s: %d", key, redactions[key]), "unknown")
		}
	}
	b.WriteString(`</div></section>`)
}

func receiptTone(ok bool) string {
	if ok {
		return "good"
	}
	return "bad"
}

func unknownCountLabel(count int) string {
	return fmt.Sprintf("%d unknown", count)
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func bucketValue(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func savingsRange(tokens int, waste analyzer.WasteRange) string {
	low := tokens * waste.Low / 100
	high := tokens * waste.High / 100
	return fmt.Sprintf("%s-%s", formatTokens(low), formatTokens(high))
}

func sourceLogLabel(count int) string {
	if count == 1 {
		return "1 log"
	}
	return fmt.Sprintf("%d logs", count)
}

func logRefsHTML(refs []analyzer.AnalyzedLogRef) template.HTML {
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="log-ref-list" aria-label="Privacy-safe local log references">`)
	for _, ref := range refs {
		label := defaultString(ref.Label, "analyzed log")
		localRef := defaultString(ref.LocalRef, "local-ref")
		sizeBucket := defaultString(ref.SizeBucket, "size unknown")
		fmt.Fprintf(
			&b,
			`<span><strong>%s</strong><code>%s</code><small>%s</small></span>`,
			htmlstd.EscapeString(label),
			htmlstd.EscapeString(localRef),
			htmlstd.EscapeString(sizeBucket),
		)
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func formatInt(n int) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return sign + s
	}
	var out []byte
	first := len(s) % 3
	if first == 0 {
		first = 3
	}
	out = append(out, s[:first]...)
	for i := first; i < len(s); i += 3 {
		out = append(out, ',')
		out = append(out, s[i:i+3]...)
	}
	return sign + string(out)
}

func formatTokens(n int) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	switch {
	case n >= 1_000_000:
		return sign + trimOneDecimal(float64(n)/1_000_000) + "M"
	case n >= 1_000:
		return sign + trimOneDecimal(float64(n)/1_000) + "k"
	default:
		return sign + formatInt(n)
	}
}

func trimOneDecimal(value float64) string {
	text := fmt.Sprintf("%.1f", value)
	return strings.TrimSuffix(text, ".0")
}

func helpTip(text string) template.HTML {
	return template.HTML(fmt.Sprintf(
		`<button type="button" class="help-tip" data-tippy-content="%s" aria-label="More information">?</button>`,
		htmlstd.EscapeString(text),
	))
}

func findingsBubbleChartHTML(report analyzer.Report) template.HTML {
	if len(report.Findings) == 0 {
		return template.HTML(`<div id="findings" class="problem-bubbles problem-bubbles-empty"><p>No major deterministic problems detected.</p></div>`)
	}
	estimates := make([]int, len(report.Findings))
	maxEstimate := 1
	for index, finding := range report.Findings {
		estimate := representativeProblemTokens(finding, report)
		if estimate > maxEstimate {
			maxEstimate = estimate
		}
		estimates[index] = estimate
	}
	var b strings.Builder
	maxDiameter := maxBubbleDiameter(len(report.Findings))
	fmt.Fprintf(&b, `<div id="findings" class="problem-bubbles" role="list" aria-label="Top problems sized by representative token impact" style="--problem-count:%d">`, len(report.Findings))
	for index, finding := range report.Findings {
		diameter := bubbleDiameter(estimates[index], maxEstimate, maxDiameter)
		tone := bubbleTone(finding, index)
		detail := fmt.Sprintf("%s. %s", findingEvidence(finding.Evidence), finding.Recommendation)
		fmt.Fprintf(
			&b,
			`<article class="problem-bubble problem-bubble-%s" role="listitem" style="--bubble-size:%dpx; --bubble-offset:%dpx; --problem-title-size:%.1fpx; --problem-detail-size:%.1fpx" aria-label="%s">`,
			htmlstd.EscapeString(tone),
			diameter,
			bubbleOffset(index),
			bubbleLabelFontSize(finding.Title, diameter, 21, 10.5),
			bubbleLabelFontSize(finding.Severity+" - "+finding.CostImpact+" "+compactNumber(estimates[index])+" potential savings", diameter, 12, 9.5),
			htmlstd.EscapeString(fmt.Sprintf("%s. %s. %s potential token savings. %s", finding.Title, finding.Severity, compactNumber(estimates[index]), detail)),
		)
		fmt.Fprintf(&b, `<span class="problem-rank">%d</span>`, index+1)
		fmt.Fprintf(&b, `<strong>%s</strong>`, htmlstd.EscapeString(finding.Title))
		fmt.Fprintf(&b, `<span class="problem-meta">%s - %s</span>`, htmlstd.EscapeString(finding.Severity), htmlstd.EscapeString(finding.CostImpact))
		fmt.Fprintf(&b, `<span class="problem-impact">%s potential savings</span>`, htmlstd.EscapeString(compactNumber(estimates[index])))
		fmt.Fprintf(&b, `<p>%s</p>`, htmlstd.EscapeString(findingEvidence(finding.Evidence)))
		fmt.Fprintf(&b, `<p>%s</p>`, htmlstd.EscapeString(finding.Recommendation))
		b.WriteString(`</article>`)
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func representativeProblemTokens(finding analyzer.Finding, report analyzer.Report) int {
	if potential := findingSavingsHigh(finding.ID, report.PluginSavings); potential > 0 {
		return potential
	}
	total := report.Metrics.EstimatedTokens
	if total <= 0 {
		total = report.AnalysisSignals.InputTokens + report.AnalysisSignals.OutputTokens
	}
	if total <= 0 {
		total = 1000
	}
	if finding.Evidence.TokenShare > 0 {
		return clampRepresentativeTokens(total*finding.Evidence.TokenShare/100, total)
	}
	switch finding.ID {
	case "tool_output_bloat":
		if report.Metrics.ToolOutputTokens > 0 {
			return clampRepresentativeTokens(report.Metrics.ToolOutputTokens, total)
		}
	case "cache_invalidation_spike":
		if report.AnalysisSignals.CacheCreationTokens > 0 {
			return clampRepresentativeTokens(report.AnalysisSignals.CacheCreationTokens, total)
		}
	case "args_hashed_retry_loop":
		return percentageTokens(total, finding.Evidence.Count, 5, 34)
	case "retry_loop":
		count := finding.Evidence.Count
		if count == 0 {
			count = report.Metrics.RetryDepthMax
		}
		return percentageTokens(total, count, 5, 32)
	case "repeated_file_reads":
		count := finding.Evidence.Count
		if count == 0 {
			count = report.Metrics.Rereads
		}
		return percentageTokens(total, count, 3, 38)
	case "context_growth_spikes":
		count := finding.Evidence.Count
		if count == 0 {
			count = report.Metrics.ContextGrowthEvents
		}
		return percentageTokens(total, count, 4, 42)
	}
	wasteLow, wasteHigh := normalizedWaste(report.EstimatedWaste)
	wasteMid := (wasteLow + wasteHigh) / 2
	if wasteMid <= 0 {
		wasteMid = 12
	}
	return clampRepresentativeTokens(total*wasteMid/100, total)
}

func findingSavingsHigh(findingID string, savings analyzer.SavingsEstimate) int {
	for _, estimate := range savings.FindingEstimates {
		if estimate.FindingID == findingID {
			return estimate.PotentialTokensHigh
		}
	}
	return 0
}

func percentageTokens(total, count, perCountPct, maxPct int) int {
	if count <= 0 {
		count = 1
	}
	pct := count * perCountPct
	if pct > maxPct {
		pct = maxPct
	}
	if pct < 4 {
		pct = 4
	}
	return clampRepresentativeTokens(total*pct/100, total)
}

func clampRepresentativeTokens(tokens, total int) int {
	if tokens < 1 {
		return 1
	}
	limit := total
	if limit < 1 {
		limit = tokens
	}
	if tokens > limit {
		return limit
	}
	return tokens
}

func maxBubbleDiameter(count int) int {
	if count <= 0 {
		return 268
	}
	const (
		chartWidth = 1040
		gap        = 22
		maxSize    = 268
		minSize    = 132
	)
	available := chartWidth - gap*(count-1)
	if available < minSize {
		return minSize
	}
	size := available / count
	if size > maxSize {
		return maxSize
	}
	if size < minSize {
		return minSize
	}
	return size
}

func bubbleDiameter(tokens, maxTokens, maxDiameter int) int {
	if maxTokens <= 0 {
		maxTokens = 1
	}
	if maxDiameter <= 0 {
		maxDiameter = 268
	}
	ratio := float64(tokens) / float64(maxTokens)
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	minDiameter := int(float64(maxDiameter) * 0.68)
	if minDiameter < 132 {
		minDiameter = 132
	}
	if minDiameter > 170 {
		minDiameter = 170
	}
	return minDiameter + int(ratio*float64(maxDiameter-minDiameter))
}

func bubbleLabelFontSize(text string, diameter int, maxPx, minPx float64) float64 {
	chars := len([]rune(text))
	if chars < 1 {
		chars = 1
	}
	available := float64(diameter) * 0.72
	if available < 90 {
		available = 90
	}
	estimated := available / (float64(chars) * 0.56)
	if estimated < minPx {
		return minPx
	}
	if estimated > maxPx {
		return maxPx
	}
	return estimated
}

func bubbleTone(finding analyzer.Finding, index int) string {
	switch finding.ID {
	case "tool_output_bloat", "cache_invalidation_spike":
		return "orange"
	case "repeated_file_reads", "context_growth_spikes":
		return "blue"
	case "retry_loop", "args_hashed_retry_loop":
		return "green"
	default:
		tones := []string{"orange", "blue", "green"}
		return tones[index%len(tones)]
	}
}

func bubbleOffset(index int) int {
	offsets := []int{0, 28, -8, 18, -18, 10}
	return offsets[index%len(offsets)]
}

func timelineChartHTML(points []analyzer.TimelinePoint, waste analyzer.WasteRange) template.HTML {
	if len(points) == 0 {
		return template.HTML(`<div class="timeline-empty">No timeline points were available.</div>`)
	}
	visible := sampleTimelinePoints(points, 60)
	maxTokens := 1
	for _, point := range visible {
		if point.EstimatedTokens > maxTokens {
			maxTokens = point.EstimatedTokens
		}
	}
	wasteLow, wasteHigh := normalizedWaste(waste)
	savingsPct := (wasteLow + wasteHigh) / 2
	if savingsPct > 95 {
		savingsPct = 95
	}
	firstTurn := visible[0].Turn
	lastTurn := visible[len(visible)-1].Turn
	var b strings.Builder
	fmt.Fprintf(&b, `<div class="timeline-legend" aria-hidden="true"><span class="timeline-legend-item"><span class="timeline-legend-swatch timeline-legend-observed"></span>estimated volume consumed</span><span class="timeline-legend-item"><span class="timeline-legend-swatch timeline-legend-savings"></span>green overlay = %d-%d%% you may save</span></div>`, wasteLow, wasteHigh)
	fmt.Fprintf(&b, `<div class="timeline-frame"><div class="timeline-y-axis" aria-hidden="true"><span>%s tokens</span><span>0</span></div>`, compactNumber(maxTokens))
	fmt.Fprintf(&b, `<div class="timeline" role="img" aria-label="%s"><div class="timeline-savings-callout" aria-hidden="true"><span>This is what you can save</span></div>`, htmlstd.EscapeString(fmt.Sprintf("Session timeline from turn %d to turn %d; maximum %d estimated token volume; potential savings range %d-%d percent.", firstTurn, lastTurn, maxTokens, wasteLow, wasteHigh)))
	for _, point := range visible {
		height := point.EstimatedTokens * 100 / maxTokens
		if height < 4 {
			height = 4
		}
		savedLow := point.EstimatedTokens * wasteLow / 100
		savedHigh := point.EstimatedTokens * wasteHigh / 100
		tooltip := fmt.Sprintf("turn %d | %s estimated token volume | %s-%s estimated potential savings | %s estimated tool-output tokens | %s rereads | %s retries",
			point.Turn,
			formatTokens(point.EstimatedTokens),
			formatTokens(savedLow),
			formatTokens(savedHigh),
			formatTokens(point.ToolTokens),
			formatInt(point.Rereads),
			formatInt(point.Retries),
		)
		escapedTooltip := htmlstd.EscapeString(tooltip)
		fmt.Fprintf(&b, `<span class="timeline-bar" style="height:%d%%" data-tooltip="%s" tabindex="0" role="img" aria-label="%s">`, height, escapedTooltip, escapedTooltip)
		if savingsPct > 0 {
			fmt.Fprintf(&b, `<span class="timeline-savings" style="height:%d%%" aria-hidden="true"></span>`, savingsPct)
		}
		b.WriteString(`</span>`)
	}
	b.WriteString(`</div></div>`)
	b.WriteString(timelineAxisHTML(visible))
	return template.HTML(b.String())
}

func normalizedWaste(waste analyzer.WasteRange) (int, int) {
	low := clampPercent(waste.Low)
	high := clampPercent(waste.High)
	if low > high {
		low, high = high, low
	}
	return low, high
}

func clampPercent(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func timelineAxisHTML(points []analyzer.TimelinePoint) string {
	if len(points) == 0 {
		return ""
	}
	type tick struct {
		class string
		label string
	}
	candidates := []tick{
		{class: "first", label: fmt.Sprintf("turn %d", points[0].Turn)},
		{class: "middle", label: fmt.Sprintf("turn %d", points[(len(points)-1)/2].Turn)},
		{class: "last", label: fmt.Sprintf("turn %d", points[len(points)-1].Turn)},
	}
	seen := map[string]bool{}
	var b strings.Builder
	b.WriteString(`<div class="timeline-x-axis" aria-hidden="true">`)
	for _, candidate := range candidates {
		if seen[candidate.label] {
			continue
		}
		seen[candidate.label] = true
		fmt.Fprintf(&b, `<span class="timeline-tick timeline-tick-%s">%s</span>`, candidate.class, htmlstd.EscapeString(candidate.label))
	}
	b.WriteString(`</div>`)
	return b.String()
}

func sampleTimelinePoints(points []analyzer.TimelinePoint, limit int) []analyzer.TimelinePoint {
	if limit <= 0 || len(points) <= limit {
		out := make([]analyzer.TimelinePoint, len(points))
		copy(out, points)
		return out
	}
	out := make([]analyzer.TimelinePoint, 0, limit)
	lastIndex := -1
	for i := 0; i < limit; i++ {
		index := i * (len(points) - 1) / (limit - 1)
		if index == lastIndex {
			continue
		}
		out = append(out, points[index])
		lastIndex = index
	}
	return out
}

func compactNumber(value int) string {
	return formatTokens(value)
}

func mapLines(values map[string]int) string {
	if len(values) == 0 {
		return "none\n"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "- %s: %d\n", key, values[key])
	}
	return b.String()
}
