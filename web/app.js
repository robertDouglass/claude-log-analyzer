const onboardingEl = document.querySelector("#onboarding");
const reportEl = document.querySelector("#report");
const sessionPanel = document.querySelector("#session-panel");
const sessionStatus = document.querySelector("#session-status");
const promptBlock = document.querySelector("#claude-prompt");
const copyPromptButtons = document.querySelectorAll("[data-copy-prompt]");
const unlockPaidButton = document.querySelector("#unlock-paid");
const waiverAccepted = document.querySelector("#waiver-accepted");
const paidStatus = document.querySelector("#paid-status");
const paidCommand = document.querySelector("#paid-command");
const copyPaidCommandButton = document.querySelector("#copy-paid-command");

const route = parseReportRoute();

if (route) {
  onboardingEl.hidden = true;
  reportEl.hidden = false;
  updateReportDownloadLinks(route);
  pollReport(route.jobID, route.token);
} else {
  reportEl.hidden = true;
  document.querySelectorAll("[data-run-command]").forEach((block) => {
    block.textContent = runCommand();
  });
  if (sessionPanel) sessionPanel.hidden = false;
}

copyPromptButtons.forEach((button) => {
  button.addEventListener("click", () => {
    const panel = button.closest(".command-panel");
    const commandBlock = panel?.querySelector("[data-run-command]") || promptBlock;
    copyText(commandBlock.textContent, button);
  });
});
copyPaidCommandButton?.addEventListener("click", () => copyText(paidCommand.textContent, copyPaidCommandButton));

unlockPaidButton?.addEventListener("click", async () => {
  unlockPaidButton.disabled = true;
  paidStatus.textContent = "creating waiver-gated paid scan commands";
  try {
    const session = await createPaidSession();
    paidCommand.textContent = session.prompt;
    copyPaidCommandButton.hidden = false;
    if (session.job_id && session.report_path) {
      paidStatus.textContent =
        `paid token expires ${new Date(session.expires_at).toLocaleTimeString()} - review these commands before running them`;
      pollPaidJob(session.job_id, session.report_path);
    } else {
      paidStatus.textContent = "review these local-first commands; they upload only the sanitized aggregate JSON";
    }
  } catch (error) {
    paidStatus.textContent = `could not unlock paid scan: ${error.message}`;
    unlockPaidButton.disabled = false;
  }
});

async function createSession() {
  const response = await fetch("/api/analysis-sessions", { method: "POST" });
  if (!response.ok) {
    throw await responseError(response);
  }
  return response.json();
}

function runCommand() {
  return [
    "npx --yes agent-analyzer@latest run",
  ].join("\n");
}

async function createPaidSession() {
  const acknowledgment =
    "I understand that Agent Analyzer provides deterministic analysis and vetted setup recommendations, but any installation or code change is executed by Claude Code, my package manager, or third-party tools with my approval and at my own risk.";
  const response = await fetch("/api/paid-sessions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      waiver_accepted: Boolean(waiverAccepted?.checked),
      acknowledgment,
    }),
  });
  if (!response.ok) {
    throw await responseError(response);
  }
  return response.json();
}

async function pollJob(jobID, reportPath) {
  for (;;) {
    const response = await fetch(`/api/jobs/${jobID}`);
    const job = await response.json();
    if (job.status === "uploading") {
      setSessionStatus("Ready. Paste Step 1 into Claude Code; this page will update after Claude uploads the session.");
    } else if (job.status === "pending" || job.status === "processing") {
      setSessionStatus("Analyzing uploaded session.");
    } else if (job.status === "completed") {
      setSessionStatus(`Report ready: <a href="${reportPath}">${reportPath}</a>`, false, true);
      return;
    } else if (job.status === "failed") {
      setSessionStatus("Analysis failed.");
      return;
    }
    await sleep(1000);
  }
}

async function pollPaidJob(jobID, reportPath) {
  for (;;) {
    const response = await fetch(`/api/jobs/${jobID}`);
    const job = await response.json();
    if (job.status === "uploading") {
      paidStatus.textContent = "waiting for paid scan upload";
    } else if (job.status === "pending" || job.status === "processing") {
      paidStatus.textContent = "analyzing sanitized paid scan report";
    } else if (job.status === "completed") {
      paidStatus.innerHTML = `paid report ready: <a href="${reportPath}">${reportPath}</a>`;
      return;
    } else if (job.status === "failed") {
      paidStatus.textContent = "paid analysis failed";
      return;
    }
    await sleep(1000);
  }
}

async function pollReport(jobID, token) {
  setReportStatus("This private report link will remain available. Waiting for analysis.");
  for (;;) {
    const jobResponse = await fetch(`/api/jobs/${jobID}`);
    if (jobResponse.ok) {
      const job = await jobResponse.json();
      if (job.status === "failed") {
        setReportStatus("Analysis failed.");
        return;
      }
      if (job.status !== "completed") {
        setReportStatus(`This private report link will remain available. Status: ${job.status}.`);
        await sleep(1000);
        continue;
      }
    }
    const reportResponse = await fetch(`/api/public-reports/${jobID}/${token}`);
    if (reportResponse.status === 404) {
      await sleep(1000);
      continue;
    }
    if (!reportResponse.ok) {
      setReportStatus(`Report unavailable: ${(await responseError(reportResponse)).message}`);
      return;
    }
    renderReport(await reportResponse.json());
    return;
  }
}

function renderReport(report) {
  document.querySelector("#report-status").innerHTML =
    '<a href="#download-report-section">Download your custom skills and save tokens now.</a>';
  document.querySelector("#score").textContent = report.score;
  const wasteRange = normalizeWasteRange(report.estimated_waste_pct);
  document.querySelector("#savings-percent").textContent = `${wasteRange.low}-${wasteRange.high}%`;
  document.querySelector("#waste").textContent = `${estimatedSavingsRange(report)} estimated tokens are addressable from the sessions you analyzed.`;
  renderTokenVolume(report);

  const findings = document.querySelector("#findings");
  findings.innerHTML = "";
  findings.className = "problem-bubbles";
  const estimates = (report.findings || []).map((finding) => representativeProblemTokens(finding, report));
  const maxEstimate = Math.max(...estimates, 1);
  for (const [index, finding] of (report.findings || []).entries()) {
    findings.appendChild(buildFindingItem(finding, report, index, estimates[index], maxEstimate));
  }
  if ((report.findings || []).length === 0) {
    findings.classList.add("problem-bubbles-empty");
    const item = document.createElement("p");
    item.textContent = "No major deterministic waste pattern detected.";
    findings.appendChild(item);
  }

  renderActionPlan(report);

  const timelineSection = document.querySelector("#timeline-section");
  if ((report.source_reports || []).length > 0) {
    if (timelineSection) timelineSection.hidden = true;
  } else {
    if (timelineSection) timelineSection.hidden = false;
    renderTimeline(report.timeline || [], report.estimated_waste_pct);
  }
  renderRecommendation(report);
  renderSavingsValidation(report);
  renderEnvironmentSignals(report);
  renderReceipt(report.security_receipt, report.redactions);
  renderPluginDownloadPreview(report);
}

function renderActionPlan(report) {
  const fixes = document.querySelector("#fixes");
  if (!fixes) return;
  fixes.replaceChildren();
  const findings = Array.isArray(report?.findings) ? report.findings : [];
  const actionableFindings = findings.slice(0, 4);
  if (actionableFindings.length > 0) {
    actionableFindings.forEach((finding) => fixes.appendChild(buildActionItem(finding)));
    return;
  }
  const fallbackFixes = Array.isArray(report?.immediate_fixes) ? report.immediate_fixes : [];
  if (fallbackFixes.length > 0) {
    fallbackFixes.slice(0, 4).forEach((fix) => fixes.appendChild(buildFallbackActionItem(fix)));
    return;
  }
  const item = document.createElement("li");
  item.className = "action-item";
  item.innerHTML = [
    "<strong>No urgent manual fix detected.</strong>",
    "<span>Download the report pack or use the generated plugin if you want these rules packaged for future sessions.</span>",
  ].join("");
  fixes.appendChild(item);
}

function buildActionItem(finding) {
  const action = actionForFinding(finding);
  const item = document.createElement("li");
  item.className = "action-item";
  const main = document.createElement("div");
  main.className = "action-main";
  const title = document.createElement("strong");
  title.textContent = action.title;
  const now = document.createElement("span");
  now.textContent = action.now;
  const why = document.createElement("span");
  why.textContent = action.why || findingEvidence(finding?.evidence);
  why.className = "action-evidence";
  main.append(title, now, why);
  const agentsLine = document.createElement("code");
  agentsLine.textContent = action.agentsLine;
  const copy = document.createElement("button");
  copy.type = "button";
  copy.className = "copy-agents-line";
  copy.dataset.copy = action.agentsLine;
  copy.textContent = "Copy line";
  item.append(main, agentsLine, copy);
  return item;
}

function buildFallbackActionItem(fix) {
  const item = document.createElement("li");
  item.className = "action-item";
  const main = document.createElement("div");
  main.className = "action-main";
  const title = document.createElement("strong");
  title.textContent = "Apply the detected fix";
  const now = document.createElement("span");
  now.textContent = String(fix || "Use a narrower workflow before continuing.");
  const why = document.createElement("span");
  why.textContent = "deterministic evidence recorded";
  why.className = "action-evidence";
  main.append(title, now, why);
  const agentsLine = document.createElement("code");
  agentsLine.textContent = "Keep agent sessions scoped and avoid unnecessary context.";
  const copy = document.createElement("button");
  copy.type = "button";
  copy.className = "copy-agents-line";
  copy.dataset.copy = agentsLine.textContent;
  copy.textContent = "Copy line";
  item.append(main, agentsLine, copy);
  return item;
}

function actionForFinding(finding) {
  switch (finding?.id) {
    case "repeated_file_reads":
      return {
        title: "Stop rereading files blindly",
        now: "Before another broad read, name the exact file or symbol and ask the agent to summarize only what changed since the last read.",
        why: findingEvidence(finding?.evidence),
        agentsLine: "Before rereading files, summarize known state and prefer targeted symbol searches or narrow line ranges over whole-file reads.",
        plugin: "The plugin packages repeated-path patterns from this scan into retrieval hygiene prompts.",
      };
    case "tool_output_bloat":
      return {
        title: "Cap noisy command output",
        now: "Use rg filters, head/tail, --json summaries, or redirect logs to a file. Paste only the failing excerpt back into context.",
        why: findingEvidence(finding?.evidence),
        agentsLine: "Cap shell command output by default; use focused filters and paste only the relevant failing excerpt back into context.",
        plugin: "The plugin can recommend shell-output reducers and context-safe command habits for your setup.",
      };
    case "retry_loop":
    case "args_hashed_retry_loop":
      return {
        title: "Break retry loops after two misses",
        now: "After two similar failures, stop editing. Restate the invariant, inspect the diff/test output, then restart with a smaller scope.",
        why: findingEvidence(finding?.evidence),
        agentsLine: "After two failed attempts on the same issue, stop, inspect the invariant and latest error, then restart with a smaller scope.",
        plugin: "The plugin turns recurring retry signatures into session hygiene rules.",
      };
    case "context_growth_spikes":
    case "cache_invalidation_spike":
      return {
        title: "Treat context spikes as boundaries",
        now: "Use /compact or start a fresh session after large tool output, model/config changes, or a pivot from debugging to architecture.",
        why: findingEvidence(finding?.evidence),
        agentsLine: "Treat major tool-output, model/config changes, and task pivots as context boundaries; compact or split the session before continuing.",
        plugin: "The plugin adds compact/split/restart nudges at the points your history shows degradation.",
      };
    case "mcp_bloat_high":
    case "mcp_bloat_severe":
      return {
        title: "Disable unused MCPs by default",
        now: "Move project-specific MCPs out of global config and lazy-load heavy servers only when the task needs them.",
        why: findingEvidence(finding?.evidence),
        agentsLine: "Keep only frequently used MCP servers enabled by default; lazy-load project-specific or heavy MCPs when the task requires them.",
        plugin: "The plugin converts MCP bloat into a concrete setup checklist.",
      };
    case "skill_bloat_high":
    case "skill_bloat_severe":
      return {
        title: "Trim always-on skills",
        now: "Keep only high-use skills active by default. Move rarely used skills behind explicit invocation.",
        why: findingEvidence(finding?.evidence),
        agentsLine: "Keep only high-signal skills in default context; invoke rare or project-specific skills explicitly when needed.",
        plugin: "The plugin can recommend a smaller skill surface from observed usage ratios.",
      };
    default:
      return {
        title: typeof finding?.title === "string" && finding.title.length > 0 ? finding.title : "Apply the detected fix",
        now: typeof finding?.recommendation === "string" && finding.recommendation.length > 0
          ? finding.recommendation
          : "Use a narrower workflow before continuing.",
        why: findingEvidence(finding?.evidence),
        agentsLine: "Keep agent sessions scoped, evidence-backed, and explicit about when context should be compacted or split.",
        plugin: "The plugin turns this report into a generated remediation pack.",
      };
  }
}

function escapeHTML(value) {
  return String(value || "").replace(/[&<>"']/g, (char) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  })[char]);
}

function buildFindingItem(finding, report, index, estimatedTokens, maxEstimate) {
  const item = document.createElement("article");
  item.className = `problem-bubble problem-bubble-${bubbleTone(finding, index)}`;
  const findingCount = Array.isArray(report?.findings) ? report.findings.length : 1;
  const diameter = bubbleDiameter(estimatedTokens, maxEstimate, maxBubbleDiameter(findingCount));
  item.style.setProperty("--bubble-size", `${diameter}px`);
  item.style.setProperty("--bubble-offset", `${bubbleOffset(index)}px`);
  item.setAttribute("role", "listitem");
  item.setAttribute(
    "aria-label",
    [
      typeof finding?.title === "string" ? finding.title : "Problem",
      typeof finding?.severity === "string" ? finding.severity : "unknown severity",
      `${formatCompactNumber(estimatedTokens)} potential savings`,
      findingEvidence(finding?.evidence),
      typeof finding?.recommendation === "string" ? finding.recommendation : "",
    ].filter(Boolean).join(". "),
  );

  const rank = document.createElement("span");
  rank.className = "problem-rank";
  rank.textContent = String(index + 1);
  item.appendChild(rank);

  const title = document.createElement("strong");
  const titleText = typeof finding?.title === "string" ? finding.title : "";
  title.textContent = titleText;
  item.style.setProperty("--problem-title-size", `${bubbleLabelFontSize(titleText, diameter, 21, 10.5)}px`);
  item.appendChild(title);

  const meta = document.createElement("span");
  meta.className = "problem-meta";
  const severity = typeof finding?.severity === "string" ? finding.severity : "unknown";
  const impact = typeof finding?.cost_impact === "string" ? finding.cost_impact : "unknown";
  const metaText = `${severity} - ${impact}`;
  meta.textContent = metaText;
  item.appendChild(meta);

  const estimate = document.createElement("span");
  estimate.className = "problem-impact";
  const estimateText = `${formatCompactNumber(estimatedTokens)} potential savings`;
  estimate.textContent = estimateText;
  item.style.setProperty("--problem-detail-size", `${bubbleLabelFontSize(`${metaText} ${estimateText}`, diameter, 12, 9.5)}px`);
  item.appendChild(estimate);

  const evidence = document.createElement("p");
  evidence.textContent = findingEvidence(finding?.evidence);
  item.appendChild(evidence);

  const recommendation = document.createElement("p");
  recommendation.textContent = typeof finding?.recommendation === "string" ? finding.recommendation : "";
  item.appendChild(recommendation);

  return item;
}

function representativeProblemTokens(finding, report) {
  const potentialSavings = findingSavingsHigh(finding?.id, report?.plugin_savings);
  if (potentialSavings > 0) return potentialSavings;
  const metrics = report?.metrics || {};
  const signals = report?.analysis_signals || {};
  let total = numberValue(metrics.estimated_tokens);
  if (total <= 0) total = numberValue(signals.input_tokens) + numberValue(signals.output_tokens);
  if (total <= 0) total = 1000;
  const evidence = finding?.evidence || {};
  const tokenShare = numberValue(evidence.token_share_pct);
  if (tokenShare > 0) return clampProblemTokens(Math.round(total * tokenShare / 100), total);
  const count = numberValue(evidence.count);
  switch (finding?.id) {
    case "tool_output_bloat":
      return clampProblemTokens(numberValue(metrics.tool_output_tokens) || Math.round(total * 0.25), total);
    case "cache_invalidation_spike":
      return clampProblemTokens(numberValue(signals.cache_creation_tokens) || Math.round(total * 0.22), total);
    case "args_hashed_retry_loop":
      return percentageProblemTokens(total, count, 5, 34);
    case "retry_loop":
      return percentageProblemTokens(total, count || numberValue(metrics.retry_depth_max), 5, 32);
    case "repeated_file_reads":
      return percentageProblemTokens(total, count || numberValue(metrics.rereads), 3, 38);
    case "context_growth_spikes":
      return percentageProblemTokens(total, count || numberValue(metrics.context_growth_events), 4, 42);
    default: {
      const wasteRange = normalizeWasteRange(report?.estimated_waste_pct);
      const wasteMid = Math.max(12, Math.round((wasteRange.low + wasteRange.high) / 2));
      return clampProblemTokens(Math.round(total * wasteMid / 100), total);
    }
  }
}

function findingSavingsHigh(findingID, savings) {
  if (!findingID || !Array.isArray(savings?.finding_estimates)) return 0;
  const estimate = savings.finding_estimates.find((item) => item?.finding_id === findingID);
  return numberValue(estimate?.potential_tokens_high);
}

function percentageProblemTokens(total, count, perCountPct, maxPct) {
  const boundedCount = Math.max(1, numberValue(count));
  const pct = Math.min(maxPct, Math.max(4, boundedCount * perCountPct));
  return clampProblemTokens(Math.round(total * pct / 100), total);
}

function clampProblemTokens(tokens, total) {
  return Math.min(Math.max(1, numberValue(tokens)), Math.max(1, numberValue(total)));
}

function renderTokenVolume(report) {
  const tokenVolume = document.querySelector("#token-volume");
  if (!tokenVolume) return;
  const metrics = report?.metrics || {};
  tokenVolume.replaceChildren();
  tokenVolume.append(
    `Analyzed token volume: ${formatCompactNumber(numberValue(metrics.estimated_tokens))} estimated input/output tokens; ` +
      `${formatCompactNumber(numberValue(metrics.tool_output_tokens))} estimated from tool output. `,
  );
  tokenVolume.appendChild(buildHelpTip(
    "What is counted here? Accuracy depends on the source log. When native usage fields exist, we use them. Otherwise we estimate roughly one token per four characters. Tool-output volume is derived from tool-result payload size and similar estimates. This is directional, not invoice-grade accounting.",
  ));
  window.AgentAnalyzerTooltips?.init(tokenVolume);
}

function estimatedSavingsRange(report) {
  const metrics = report?.metrics || {};
  const wasteRange = normalizeWasteRange(report?.estimated_waste_pct);
  const total = numberValue(metrics.estimated_tokens);
  const low = Math.round(total * wasteRange.low / 100);
  const high = Math.round(total * wasteRange.high / 100);
  return `${formatCompactNumber(low)}-${formatCompactNumber(high)}`;
}

function buildHelpTip(text) {
  const tip = document.createElement("button");
  tip.type = "button";
  tip.className = "help-tip";
  tip.setAttribute("data-tippy-content", text);
  tip.setAttribute("aria-label", "More information");
  tip.textContent = "?";
  return tip;
}

function maxBubbleDiameter(count) {
  const boundedCount = Math.max(1, numberValue(count));
  const chartWidth = 1040;
  const gap = 22;
  const available = chartWidth - gap * (boundedCount - 1);
  return Math.max(132, Math.min(268, Math.floor(available / boundedCount)));
}

function bubbleDiameter(tokens, maxTokens, maxDiameter = 268) {
  const ratio = Math.min(1, Math.max(0, numberValue(tokens) / Math.max(1, numberValue(maxTokens))));
  const boundedMax = Math.max(132, numberValue(maxDiameter) || 268);
  const minDiameter = Math.min(170, Math.max(132, Math.floor(boundedMax * 0.68)));
  return minDiameter + Math.round(ratio * (boundedMax - minDiameter));
}

function bubbleLabelFontSize(text, diameter, maxPx, minPx) {
  const chars = Math.max(1, String(text || "").length);
  const available = Math.max(90, diameter * 0.72);
  const estimated = available / (chars * 0.56);
  return Number(Math.max(minPx, Math.min(maxPx, estimated)).toFixed(1));
}

function bubbleTone(finding, index) {
  switch (finding?.id) {
    case "tool_output_bloat":
    case "cache_invalidation_spike":
      return "orange";
    case "repeated_file_reads":
    case "context_growth_spikes":
      return "blue";
    case "retry_loop":
    case "args_hashed_retry_loop":
      return "green";
    default:
      return ["orange", "blue", "green"][index % 3];
  }
}

function bubbleOffset(index) {
  return [0, 28, -8, 18, -18, 10][index % 6];
}

function renderTimeline(points, estimatedWaste) {
  const chart = document.querySelector("#timeline");
  const yMax = document.querySelector("#timeline-y-max");
  const xAxis = document.querySelector("#timeline-x-axis");
  const legend = document.querySelector("#timeline-legend");
  chart.innerHTML = "";
  if (xAxis) xAxis.replaceChildren();
  if (legend) legend.replaceChildren();
  if (points.length === 0) {
    chart.textContent = "No timeline points detected.";
    chart.removeAttribute("aria-label");
    if (yMax) yMax.textContent = "max";
    return;
  }
  const visiblePoints = sampleTimelinePoints(points, 60);
  const maxTokens = Math.max(...visiblePoints.map((point) => numberValue(point.estimated_tokens)), 1);
  const wasteRange = normalizeWasteRange(estimatedWaste);
  const savingsPct = Math.min(95, Math.max(0, (wasteRange.low + wasteRange.high) / 2));
  const firstTurn = numberValue(visiblePoints[0]?.turn);
  const lastTurn = numberValue(visiblePoints[visiblePoints.length - 1]?.turn);
  chart.setAttribute(
    "aria-label",
    `Session timeline showing estimated context/token volume from turn ${firstTurn} to turn ${lastTurn}; maximum ${formatNumber(maxTokens)} estimated tokens; potential savings range ${wasteRange.low}-${wasteRange.high} percent.`,
  );
  if (yMax) yMax.textContent = `${formatCompactNumber(maxTokens)} tokens`;
  renderTimelineLegend(legend, wasteRange);
  const callout = document.createElement("div");
  callout.className = "timeline-savings-callout";
  callout.setAttribute("aria-hidden", "true");
  const calloutText = document.createElement("span");
  calloutText.textContent = "This is what you can save";
  callout.appendChild(calloutText);
  chart.appendChild(callout);
  for (const point of visiblePoints) {
    const bar = document.createElement("span");
    const estimatedTokens = numberValue(point.estimated_tokens);
    const savedTokensLow = Math.round(estimatedTokens * wasteRange.low / 100);
    const savedTokensHigh = Math.round(estimatedTokens * wasteRange.high / 100);
    const tooltip = [
      `turn ${numberValue(point.turn)}`,
      `${formatNumber(estimatedTokens)} estimated token volume`,
      `${formatNumber(savedTokensLow)}-${formatNumber(savedTokensHigh)} estimated potential savings`,
      `${formatNumber(numberValue(point.tool_tokens))} estimated tool-output tokens`,
      `${formatNumber(numberValue(point.rereads))} rereads`,
      `${formatNumber(numberValue(point.retries))} retries`,
    ].join(" | ");
    bar.className = "timeline-bar";
    bar.style.height = `${Math.max(4, (estimatedTokens / maxTokens) * 100)}%`;
    bar.dataset.tooltip = tooltip;
    bar.tabIndex = 0;
    bar.setAttribute("role", "img");
    bar.setAttribute("aria-label", tooltip);
    if (savingsPct > 0) {
      const savings = document.createElement("span");
      savings.className = "timeline-savings";
      savings.style.height = `${savingsPct}%`;
      savings.setAttribute("aria-hidden", "true");
      bar.appendChild(savings);
    }
    chart.appendChild(bar);
  }
  renderTimelineAxis(xAxis, visiblePoints);
}

function sampleTimelinePoints(points, limit) {
  if (!Array.isArray(points)) return [];
  if (limit <= 0 || points.length <= limit) return points.slice();
  const sampled = [];
  let lastIndex = -1;
  for (let i = 0; i < limit; i += 1) {
    const index = Math.floor(i * (points.length - 1) / (limit - 1));
    if (index === lastIndex) continue;
    sampled.push(points[index]);
    lastIndex = index;
  }
  return sampled;
}

function normalizeWasteRange(estimatedWaste) {
  const low = Math.round(clampPercent(estimatedWaste?.low));
  const high = Math.round(clampPercent(estimatedWaste?.high));
  return {
    low: Math.min(low, high),
    high: Math.max(low, high),
  };
}

function clampPercent(value) {
  return Math.min(100, Math.max(0, numberValue(value)));
}

function renderTimelineLegend(legend, wasteRange) {
  if (!legend) return;
  const observed = document.createElement("span");
  observed.className = "timeline-legend-item";
  const observedSwatch = document.createElement("span");
  observedSwatch.className = "timeline-legend-swatch timeline-legend-observed";
  observed.appendChild(observedSwatch);
  observed.append("estimated volume consumed");
  legend.appendChild(observed);

  const avoidable = document.createElement("span");
  avoidable.className = "timeline-legend-item";
  const avoidableSwatch = document.createElement("span");
  avoidableSwatch.className = "timeline-legend-swatch timeline-legend-savings";
  avoidable.appendChild(avoidableSwatch);
  avoidable.append(`green overlay = ${wasteRange.low}-${wasteRange.high}% you may save`);
  legend.appendChild(avoidable);
}

function renderTimelineAxis(axis, visiblePoints) {
  if (!axis || visiblePoints.length === 0) return;
  const first = visiblePoints[0];
  const middle = visiblePoints[Math.floor((visiblePoints.length - 1) / 2)];
  const last = visiblePoints[visiblePoints.length - 1];
  const ticks = [
    { key: "first", label: `turn ${numberValue(first.turn)}` },
    { key: "middle", label: `turn ${numberValue(middle.turn)}` },
    { key: "last", label: `turn ${numberValue(last.turn)}` },
  ].filter((tick, index, all) => all.findIndex((item) => item.label === tick.label) === index);
  for (const tick of ticks) {
    const item = document.createElement("span");
    item.className = `timeline-tick timeline-tick-${tick.key}`;
    item.textContent = tick.label;
    axis.appendChild(item);
  }
}

function numberValue(value) {
  return Number.isFinite(Number(value)) ? Number(value) : 0;
}

function formatNumber(value) {
  return new Intl.NumberFormat("en-US").format(numberValue(value));
}

function formatCompactNumber(value) {
  return new Intl.NumberFormat("en-US", {
    notation: "compact",
    maximumFractionDigits: 1,
  }).format(numberValue(value));
}

function renderWorkflowFingerprints(report) {
  const section = document.querySelector("#workflow-fingerprints");
  const list = document.querySelector("#workflow-fingerprints-list");
  if (!section || !list) return;
  const fps = report && report.ecosystem && Array.isArray(report.ecosystem.workflow_fingerprints)
    ? report.ecosystem.workflow_fingerprints
    : [];
  list.replaceChildren();
  if (fps.length === 0) {
    section.hidden = true;
    return;
  }
  section.hidden = false;
  for (const fp of fps) {
    if (!fp || typeof fp !== "object") continue;
    const row = document.createElement("li");
    row.className = "fingerprint-card";

    const header = document.createElement("div");
    header.className = "fingerprint-header";
    const title = document.createElement("strong");
    title.className = "fingerprint-id";
    title.textContent = typeof fp.id === "string" ? fp.id : "";
    header.appendChild(title);

    const confidence = document.createElement("span");
    const confValue = typeof fp.confidence === "string" ? fp.confidence : "";
    confidence.className = "fingerprint-confidence status-chip";
    if (confValue) confidence.classList.add(`confidence-${confValue}`);
    confidence.textContent = confValue ? `${confValue} confidence` : "unknown confidence";
    header.appendChild(confidence);

    if (fp.active === true) {
      header.appendChild(statusChip("active", "good"));
    }
    if (fp.installed === true) {
      header.appendChild(statusChip("installed", "good"));
    }
    if (typeof fp.version_bucket === "string" && fp.version_bucket.length > 0) {
      header.appendChild(statusChip(`version ${fp.version_bucket}`, ""));
    }
    row.appendChild(header);

    const body = document.createElement("div");
    body.className = "fingerprint-body";
    body.appendChild(factTile("Evidence", String(typeof fp.evidence_count === "number" ? fp.evidence_count : 0)));
    body.appendChild(sourceGroups(Array.isArray(fp.sources) ? fp.sources : []));
    row.appendChild(body);

    list.appendChild(row);
  }
}

function sourceGroups(sources) {
  const panel = document.createElement("div");
  panel.className = "fingerprint-source-groups";
  const groups = {
    "CLI": [],
    "Config": [],
    "Agent surface": [],
    "Other": [],
  };
  for (const source of sources) {
    const id = typeof source === "string" ? source : "";
    const label = SOURCE_LABEL[id] || humanizeID(id);
    groups[sourceCategory(id)].push(label);
  }
  for (const [label, values] of Object.entries(groups)) {
    if (values.length === 0) continue;
    const group = document.createElement("section");
    group.className = "mini-chip-group";
    const title = document.createElement("h3");
    title.textContent = label;
    const chips = document.createElement("div");
    chips.className = "chip-list";
    values.forEach((value) => chips.appendChild(chip(value, "")));
    group.append(title, chips);
    panel.appendChild(group);
  }
  if (panel.childElementCount === 0) {
    panel.appendChild(chip("no public source markers", "muted"));
  }
  return panel;
}

function statusChip(text, tone) {
  const item = document.createElement("span");
  item.className = `status-chip${tone ? ` status-chip-${tone}` : ""}`;
  item.textContent = text;
  return item;
}

const SOURCE_LABEL = {
  cli_binary: "binary present",
  cli_version_probe: "version probe",
  command_name: "command name",
  config_dir: "config directory",
  config_file: "config file",
  package_manifest: "package manifest",
  skill_name: "skill name",
  slash_command: "slash command",
  mcp_namespace: "MCP namespace",
  hook_config: "hook config",
};

function sourceCategory(id) {
  if (id.startsWith("cli_") || id === "command_name") return "CLI";
  if (id.startsWith("config_") || id === "package_manifest" || id === "hook_config") return "Config";
  if (id === "skill_name" || id === "slash_command" || id === "mcp_namespace") return "Agent surface";
  return "Other";
}

function humanizeID(value) {
  return String(value || "unknown").replaceAll("_", " ");
}

// Token-saving recommendation rendering — kitty-specs/token-saving-recommendation-phase-b-01KS0JZ4.
//
// Every text node is composed via textContent from allowlisted enum values
// produced by internal/analyzer/token_saving_*.go. Unknown values are never
// echoed into the DOM; that keeps this renderer privacy-safe even if it is
// handed malformed report JSON.

const TOOL_LABEL = {
  ccusage: "ccusage",
  ccstatusline: "ccstatusline",
  claude_code_usage_monitor: "Claude Code Usage Monitor",
  claude_code_usage_tracker: "Claude Code Usage Tracker",
  tokenusage: "tokenusage",
  claude_meter: "Claude Meter",
  context_mode: "Context Mode",
  distill: "Distill",
  token_optimizer_mcp: "Token Optimizer MCP",
  rtk: "RTK (Rust Token Killer, rtk-ai/rtk)",
  leanctx: "LeanCtx",
  headroom: "Headroom",
  claude_context: "Claude Context",
  grepai: "GrepAI",
  serena: "Serena",
  codegraph: "CodeGraph",
  codebase_memory_mcp: "Codebase Memory MCP",
  code_review_graph: "Code Review Graph",
  semble: "Semble",
  jcodemunch_mcp: "jcodemunch MCP",
  token_savior: "Token Savior",
  cocoindex_code: "CocoIndex Code",
  read_once: "Read Once",
  openwolf: "OpenWolf",
  memsearch: "MemSearch",
  claude_token_efficient: "Claude Token Efficient",
  caveman: "Caveman",
  claude_code_hooks_mastery: "Claude Code Hooks Mastery",
  awesome_claude_code: "Awesome Claude Code",
};

const TOOL_URL = {
  ccusage: "https://github.com/ryoppippi/ccusage",
  ccstatusline: "https://github.com/sirmalloc/ccstatusline",
  claude_code_usage_monitor: "https://github.com/Maciek-roboblog/Claude-Code-Usage-Monitor",
  claude_code_usage_tracker: "https://github.com/LyndonWangWork/Claude-Code-Usage-Tracker",
  context_mode: "https://github.com/mksglu/context-mode",
  rtk: "https://github.com/rtk-ai/rtk",
  claude_context: "https://github.com/zilliztech/claude-context",
  grepai: "https://github.com/yoanbernabeu/grepai",
  memsearch: "https://github.com/zilliztech/memsearch",
  claude_token_efficient: "https://github.com/drona23/claude-token-efficient",
  caveman: "https://github.com/JuliusBrussee/caveman",
  claude_code_hooks_mastery: "https://github.com/disler/claude-code-hooks-mastery",
  awesome_claude_code: "https://github.com/hesreallyhim/awesome-claude-code",
};

const REASON_LABEL = {
  absent: "Not detected yet",
  installed_inactive: "Installed but not active",
  configured_inactive: "Configured but not active",
  active_persistent: "Already active",
  rejected_alternative: "Previously rejected",
  prune_first: "Prune your current tooling first",
  audit_config: "Audit current config",
  no_op: "No action needed",
  server_quota_check: "Server quota check",
};

const CONFIDENCE_LABEL = {
  low: "Low confidence",
  medium: "Medium confidence",
  high: "High confidence",
};

const RISK_LABEL = {
  low: "Low risk",
  medium: "Medium risk",
  high: "High risk",
};

const POLICY_LABEL = {
  bundle: "Bundled",
  recommend: "Recommended",
  recommend_with_waiver: "Recommended (waiver required)",
  research_only: "Research only",
  reference_only: "Reference only",
};

const SIGNAL_LABEL = {
  tool_output_bloat: "Tool output bloat",
  shell_output_bloat: "Shell output bloat",
  mcp_tool_output_bloat: "MCP tool output bloat",
  repeated_file_reads: "Repeated file reads",
  broad_repo_exploration: "Broad repo exploration",
  unchanged_file_rereads: "Unchanged file rereads",
  mcp_skill_bloat: "MCP / skill bloat",
  output_verbosity: "Output verbosity",
  retry_loop: "Retry loop",
  context_growth_spikes: "Context growth spikes",
  no_usage_visibility: "No usage visibility",
};

const SAVINGS_BUCKET_LABEL = {
  low: "Low estimated savings",
  medium: "Medium estimated savings",
  high: "High estimated savings",
};

const FAILURE_MODE_LABEL = {
  noisy_terminal_logs: "Noisy terminal logs",
  tool_output_flooding: "Tool output flooding",
  repeated_codebase_navigation: "Repeated codebase navigation",
  broad_file_reads_or_verbose_output: "Broad reads / verbose output",
  memory_gaps: "Memory gaps",
  cross_cutting_telemetry: "Cross-cutting hygiene",
};

const INSTALL_SURFACE_LABEL = {
  local_binary_plus_claude_hook: "Local binary + Claude hook",
  local_binary_explicit_compression: "Local binary + explicit compression",
  claude_plugin_plus_mcp: "Claude plugin + MCP",
  mcp_plus_external_vector_store: "MCP + external vector store",
  local_binary_plus_optional_embedding_provider: "Local binary + optional embeddings",
  local_cli_or_local_config: "Local CLI/config",
  mcp_server: "MCP server",
  retrieval_tool: "Retrieval tool",
  local_instruction_config: "Local instruction config",
  prune_or_lazy_load_existing_mcp_and_skills: "Prune / lazy-load existing tools",
  session_workflow_and_config_audit: "Session workflow audit",
};

const TOOL_PURPOSE = {
  ccusage: "ccusage is measurement, not a reducer: it gives independent token, cache, and cost accounting so you can verify whether changes are working.",
  ccstatusline: "ccstatusline is awareness, not a reducer: keep it outside the prompt path so cost and context drift are visible without adding task overhead.",
  context_mode: "Context Mode compresses or externalizes noisy tool output before it pollutes future turns.",
  claude_context: "claude-context is not a default recommendation from our runs: it added overhead on the current fixture and needs a larger retrieval-amortization task before promotion.",
  claude_token_efficient: "claude-token-efficient showed only a small, noisy saving in our 3x runs, so we treat it as manual verbosity hygiene rather than a default install.",
  claude_code_hooks_mastery: "Claude Code Hooks Mastery is a reference set for deterministic hooks that can enforce session hygiene.",
  rtk: "RTK is a high-risk shell-output reducer candidate. Only consider the linked rtk-ai/rtk project, not unrelated packages with the same name.",
  semble: "Semble earned a scoped retrieval recommendation in our 3x runs when path-limited search replaced broad repeated file reads.",
  squeez: "Squeez is not recommended: it conflicts with Spec Kitty workflows despite a positive old shell/log compression benchmark result.",
};

function savingsBucket(report) {
  const high = report?.estimated_waste_pct?.high ?? 0;
  if (high < 10) return "low";
  if (high < 30) return "medium";
  return "high";
}

function labelFrom(table, value, fallback) {
  return typeof value === "string" && Object.hasOwn(table, value) ? table[value] : fallback;
}

function renderRecommendation(report) {
  const section = document.querySelector("#recommendation-section");
  const primaryRoot = document.querySelector("#recommendation-primary");
  const secondaryRoot = document.querySelector("#recommendation-secondary");
  const emptyNote = document.querySelector("#recommendation-empty");
  if (!section || !primaryRoot || !secondaryRoot || !emptyNote) return;

  // Reset slot DOM on every render.
  primaryRoot.replaceChildren();
  secondaryRoot.replaceChildren();
  emptyNote.replaceChildren();

  // FR-012: legacy report JSON (no recommendation field) renders nothing.
  if (report?.recommendation == null) {
    section.hidden = true;
    primaryRoot.hidden = true;
    secondaryRoot.hidden = true;
    emptyNote.hidden = true;
    return;
  }

  const rec = report.recommendation;
  section.hidden = false;

  const primary = rec.primary;
  if (primary && typeof primary === "object") {
    primaryRoot.hidden = false;
    primaryRoot.appendChild(buildRecommendationCard(primary, savingsBucket(report)));
  } else {
    primaryRoot.hidden = true;
  }

  const secondary = rec.secondary;
  if (secondary && typeof secondary === "object") {
    secondaryRoot.hidden = false;
    secondaryRoot.appendChild(buildRecommendationCard(secondary, null));
  } else {
    secondaryRoot.hidden = true;
  }

  // FR-006 / FR-009: no-op note when both Primary and Secondary are absent.
  const noActionable = !primary && !secondary;
  if (noActionable) {
    const skippedCount = Array.isArray(rec.skipped) ? rec.skipped.length : 0;
    const unknownCount = typeof rec.unknown_id_count === "number" ? rec.unknown_id_count : 0;
    const sentence = document.createElement("span");
    let text =
      `Engine evaluated ${skippedCount} candidate${skippedCount === 1 ? "" : "s"}; ` +
      `none warranted a recommendation.`;
    if (unknownCount > 0) {
      text +=
        ` (${unknownCount} unknown identifier${unknownCount === 1 ? "" : "s"} ` +
        `${unknownCount === 1 ? "was" : "were"} counted only.)`;
    }
    sentence.textContent = text;
    emptyNote.appendChild(sentence);
    emptyNote.hidden = false;
  } else {
    emptyNote.hidden = true;
  }
}

function renderSavingsValidation(report) {
  const section = document.querySelector("#savings-validation-section");
  const summary = document.querySelector("#savings-validation-summary");
  const metricsRoot = document.querySelector("#savings-validation-metrics");
  const warningsRoot = document.querySelector("#savings-validation-warnings");
  if (!section || !summary || !metricsRoot || !warningsRoot) return;
  metricsRoot.replaceChildren();
  warningsRoot.replaceChildren();

  const validation = report?.savings_validation;
  if (!validation || typeof validation !== "object") {
    section.hidden = true;
    return;
  }

  section.hidden = false;
  const tier = typeof validation.evidence_tier === "string" ? validation.evidence_tier : "inconclusive";
  const tierLabel = labelFrom(SAVINGS_TIER_LABEL, tier, "Inconclusive");
  const text = typeof validation.summary === "string" && validation.summary.length > 0
    ? validation.summary
    : "Follow-up comparison used bounded report metrics only.";
  summary.textContent = `${tierLabel}: ${text}`;

  const deltas = Array.isArray(validation.metric_deltas) ? validation.metric_deltas : [];
  for (const delta of deltas) {
    if (!delta || typeof delta !== "object") continue;
    const row = document.createElement("div");
    row.className = "validation-metric";
    const label = document.createElement("span");
    label.textContent = typeof delta.label === "string" ? delta.label : "Metric";
    const value = document.createElement("strong");
    const pct = typeof delta.delta_pct === "number" ? delta.delta_pct : 0;
    value.textContent = `${pct > 0 ? "+" : ""}${pct}%`;
    value.className = delta.improved ? "validation-improved" : "validation-worse";
    row.append(label, value);
    metricsRoot.appendChild(row);
  }

  const warnings = Array.isArray(validation.warnings) ? validation.warnings : [];
  for (const warning of warnings) {
    if (typeof warning !== "string" || warning.length === 0) continue;
    const item = document.createElement("li");
    item.textContent = labelFrom(SAVINGS_WARNING_LABEL, warning, warning);
    warningsRoot.appendChild(item);
  }
  warningsRoot.hidden = warningsRoot.childElementCount === 0;
}

const SAVINGS_TIER_LABEL = {
  verified: "Verified reducer activity",
  observed: "Observed improvement",
  inconclusive: "Inconclusive",
  proven: "Controlled benchmark proof",
};

const SAVINGS_WARNING_LABEL = {
  small_log_sample: "Small log sample.",
  source_mix_changed: "Source mix changed between baseline and follow-up.",
  workload_volume_changed: "Workload volume changed between baseline and follow-up.",
  missing_native_token_counters: "Native token counters were missing in one window.",
};

function buildRecommendationCard(rec, savingsBucketValue) {
  const card = document.createElement("div");
  card.className = "recommendation-card";

  // Tool label. Advisory recommendations intentionally carry an empty
  // PrimaryToolID; render those as actions instead of blank tool cards.
  const toolID = typeof rec.primary_tool_id === "string" ? rec.primary_tool_id : "";
  const toolName = typeof rec.primary_tool_name === "string" ? rec.primary_tool_name : "";
  const header = document.createElement("div");
  header.className = "recommendation-header";
  const headerText = document.createElement("div");
  const kicker = document.createElement("span");
  kicker.className = "recommendation-kicker";
  kicker.textContent = recommendationProblem(rec);
  const toolEl = document.createElement("div");
  toolEl.className = "recommendation-tool";
  toolEl.textContent = toolID.length > 0
    ? (toolName.length > 0 ? toolName : labelFrom(TOOL_LABEL, toolID, "Unknown tool"))
    : advisoryRecommendationLabel(rec);
  headerText.append(kicker, toolEl);
  const verdict = document.createElement("span");
  verdict.className = "recommendation-verdict";
  verdict.textContent = recommendationVerdict(rec);
  header.append(headerText, verdict);
  card.appendChild(header);

  const purpose = document.createElement("p");
  purpose.className = "recommendation-purpose";
  purpose.textContent = recommendationPurpose(rec);
  card.appendChild(purpose);

  const nextStep = document.createElement("div");
  nextStep.className = "recommendation-next-step";
  const nextStrong = document.createElement("strong");
  nextStrong.textContent = recommendationAction(rec);
  const nextSpan = document.createElement("span");
  nextSpan.textContent = recommendationPluginPitch(rec);
  nextStep.append(nextStrong, nextSpan);
  card.appendChild(nextStep);

  const reportSourceURL = typeof rec.primary_tool_url === "string" ? rec.primary_tool_url : "";
  const sourceURL = reportSourceURL.startsWith("https://")
    ? reportSourceURL
    : (toolID.length > 0 && Object.hasOwn(TOOL_URL, toolID) ? TOOL_URL[toolID] : "");

  // Optional savings-bucket badge (Primary only).
  if (typeof savingsBucketValue === "string" && savingsBucketValue.length > 0) {
    const savings = document.createElement("span");
    savings.className = "recommendation-savings-bucket";
    savings.textContent = labelFrom(SAVINGS_BUCKET_LABEL, savingsBucketValue, "Estimated savings");
    card.appendChild(savings);
  }

  const meta = document.createElement("div");
  meta.className = "recommendation-meta";

  const reason = typeof rec.reason === "string" ? rec.reason : "";
  const reasonEl = document.createElement("span");
  reasonEl.className = "recommendation-reason";
  reasonEl.textContent = labelFrom(REASON_LABEL, reason, "Unknown reason");
  meta.appendChild(reasonEl);

  const confidence = typeof rec.confidence === "string" ? rec.confidence : "";
  const confidenceEl = document.createElement("span");
  confidenceEl.className = "recommendation-confidence";
  confidenceEl.textContent = labelFrom(CONFIDENCE_LABEL, confidence, "Unknown confidence");
  meta.appendChild(confidenceEl);

  const risk = typeof rec.risk_level === "string" ? rec.risk_level : "";
  const riskEl = document.createElement("span");
  riskEl.className = "recommendation-risk";
  riskEl.textContent = `${labelFrom(RISK_LABEL, risk, "Unknown risk")} install risk`;
  meta.appendChild(riskEl);

  if (sourceURL.length > 0) {
    const source = document.createElement("a");
    source.className = "recommendation-source";
    source.href = sourceURL;
    source.rel = "noopener noreferrer";
    source.target = "_blank";
    source.textContent = "Source";
    meta.appendChild(source);
  }

  card.appendChild(meta);

  const failureModes = Array.isArray(rec.failure_modes) ? rec.failure_modes : [];
  if (failureModes.length > 0 || typeof rec.install_surface === "string" || typeof rec.data_movement_risk === "string") {
    const fit = document.createElement("div");
    fit.className = "recommendation-fit";
    for (const mode of failureModes) {
      const id = typeof mode === "string" ? mode : "";
      if (!Object.hasOwn(FAILURE_MODE_LABEL, id)) continue;
      const chip = document.createElement("span");
      chip.textContent = FAILURE_MODE_LABEL[id];
      fit.appendChild(chip);
    }
    const surface = typeof rec.install_surface === "string" ? rec.install_surface : "";
    if (surface.length > 0) {
      const chip = document.createElement("span");
      chip.textContent = labelFrom(INSTALL_SURFACE_LABEL, surface, "Install surface reviewed");
      fit.appendChild(chip);
    }
    const dataRisk = typeof rec.data_movement_risk === "string" ? rec.data_movement_risk : "";
    if (Object.hasOwn(RISK_LABEL, dataRisk)) {
      const chip = document.createElement("span");
      chip.textContent = `Data movement: ${RISK_LABEL[dataRisk].toLowerCase()}`;
      fit.appendChild(chip);
    }
    if (fit.childElementCount > 0) card.appendChild(fit);
  }

  const conflicts = Array.isArray(rec.conflicts_with) ? rec.conflicts_with : [];
  const safeConflicts = conflicts
    .map((id) => (typeof id === "string" && Object.hasOwn(TOOL_LABEL, id) ? TOOL_LABEL[id] : ""))
    .filter(Boolean);
  if (safeConflicts.length > 0) {
    const conflict = document.createElement("p");
    conflict.className = "recommendation-conflicts";
    conflict.textContent = `Overlaps with ${safeConflicts.join(", ")}. Choose one tool for this failure mode unless you explicitly approve both.`;
    card.appendChild(conflict);
  }

  const ambiguity = typeof rec.ambiguity_warning === "string" ? rec.ambiguity_warning : "";
  if (ambiguity.length > 0 && toolID === "rtk") {
    const warning = document.createElement("p");
    warning.className = "recommendation-warning";
    warning.textContent = "RTK means github.com/rtk-ai/rtk. Do not install the unrelated npm package named rtk.";
    card.appendChild(warning);
  }

  // Signal chips.
  const signals = Array.isArray(rec.signal_ids) ? rec.signal_ids : [];
  if (signals.length > 0) {
    const signalList = document.createElement("ul");
    signalList.className = "recommendation-signals";
    for (const sig of signals) {
      const id = typeof sig === "string" ? sig : "";
      if (id.length === 0) continue;
      const chip = document.createElement("li");
      chip.className = "recommendation-signal";
      chip.textContent = labelFrom(SIGNAL_LABEL, id, "Unknown signal");
      signalList.appendChild(chip);
    }
    card.appendChild(signalList);
  }

  return card;
}

function recommendationProblem(rec) {
  const signals = Array.isArray(rec.signal_ids) ? rec.signal_ids : [];
  if (signals.includes("no_usage_visibility")) return "You lack usage visibility";
  if (signals.some((signal) => ["repeated_file_reads", "broad_repo_exploration", "unchanged_file_rereads"].includes(signal))) {
    return "Your agent is rereading too much";
  }
  if (signals.some((signal) => ["tool_output_bloat", "shell_output_bloat", "mcp_tool_output_bloat"].includes(signal))) {
    return "Tool output is flooding context";
  }
  if (signals.includes("mcp_skill_bloat")) return "Your tool surface may be too wide";
  if (signals.some((signal) => ["retry_loop", "context_growth_spikes"].includes(signal))) return "Session hygiene is degrading";
  if (signals.includes("output_verbosity")) return "Assistant output is accumulating";
  return "Tooling gap detected";
}

function recommendationPurpose(rec) {
  const toolID = typeof rec.primary_tool_id === "string" ? rec.primary_tool_id : "";
  if (Object.hasOwn(TOOL_PURPOSE, toolID)) return TOOL_PURPOSE[toolID];
  const signals = Array.isArray(rec.signal_ids) ? rec.signal_ids : [];
  if (signals.includes("mcp_skill_bloat")) {
    return "This is not a request to install another tool. First prune or lazy-load MCPs and skills that are exposed but rarely used.";
  }
  if (signals.some((signal) => ["retry_loop", "context_growth_spikes"].includes(signal))) {
    return "This is a workflow recommendation: add rules that make the agent stop, compact, or split the session before degradation compounds.";
  }
  if (signals.some((signal) => ["repeated_file_reads", "broad_repo_exploration", "unchanged_file_rereads"].includes(signal))) {
    return "Retrieval tooling can reduce repeated file reads by giving the agent a narrower way to locate relevant code.";
  }
  if (signals.includes("no_usage_visibility")) {
    return "Usage visibility tools make token burn and cache behavior visible so you can tell whether changes are working.";
  }
  return "This recommendation is matched from a vetted allowlist against deterministic waste signals in the report.";
}

function recommendationAction(rec) {
  const toolID = typeof rec.primary_tool_id === "string" ? rec.primary_tool_id : "";
  const risk = typeof rec.risk_level === "string" ? rec.risk_level : "";
  if (toolID.length === 0) return "Do not install anything yet.";
  if (risk === "high") return "Review the source and prefer plugin-generated setup instructions.";
  if (risk === "medium") return "Review the source first; prefer plugin-generated setup instructions.";
  return "Review the source, or let the plugin configure the right path from this report.";
}

function recommendationPluginPitch(rec) {
  const toolID = typeof rec.primary_tool_id === "string" ? rec.primary_tool_id : "";
  if (toolID.length === 0) {
    return "The plugin can convert this into concrete config cleanup rules from your full history.";
  }
  return "The plugin packages vetted recommendations and avoids one-off manual setup guesses.";
}

function recommendationVerdict(rec) {
  const toolID = typeof rec.primary_tool_id === "string" ? rec.primary_tool_id : "";
  const risk = typeof rec.risk_level === "string" ? rec.risk_level : "";
  if (toolID.length === 0) return "Prune first";
  if (risk === "high") return "Careful review";
  return "Candidate";
}

function advisoryRecommendationLabel(rec) {
  const reason = typeof rec.reason === "string" ? rec.reason : "";
  const signals = Array.isArray(rec.signal_ids) ? rec.signal_ids : [];
  if (reason === "prune_first" || signals.includes("mcp_skill_bloat")) {
    return "Prune / lazy-load MCPs and skills";
  }
  if (reason === "audit_config" || signals.includes("retry_loop") || signals.includes("context_growth_spikes")) {
    return "Session hygiene audit";
  }
  return "Tooling recommendation";
}

// The four allowlisted advice IDs are emitted by internal/analyzer/analyzer.go:368-394.
// If those IDs change, this UI must be updated in lockstep.
const ADVICE_LOOKUP = {
  mcp: { severe: "mcp_bloat_severe", high: "mcp_bloat_high" },
  skill: { severe: "skill_bloat_severe", high: "skill_bloat_high" },
};
// No keys for watch/normal/unknown — structurally enforces FR-006.

function findingById(report, id) {
  const list = report && Array.isArray(report.findings) ? report.findings : [];
  return list.find((f) => f && f.id === id) || null;
}

// Empty/missing warning_band → render as "unknown" (matches analyzer guarantee
// in tooling_classify.go:149-151 / 191-193 when exposure_known is false; also
// tolerates a future struct-default zero-value).
function normalizeBand(b) {
  const v = typeof b === "string" ? b : "";
  return (v === "severe" || v === "high" || v === "watch" || v === "normal") ? v : "unknown";
}

function renderToolingUtilization(report) {
  const section = document.querySelector("#tooling-utilization");
  const rowsRoot = document.querySelector("#tooling-utilization-rows");
  if (!section || !rowsRoot) return;
  const tu = report && report.ecosystem && report.ecosystem.tooling_utilization;
  rowsRoot.replaceChildren();
  if (!tu) {
    section.hidden = true;
    return;
  }
  section.hidden = false;

  const mcp = tu.mcp;
  if (mcp && typeof mcp === "object") {
    rowsRoot.appendChild(buildMCPRow(report, mcp));
  }
  const skill = tu.skill;
  if (skill && typeof skill === "object") {
    rowsRoot.appendChild(buildSkillRow(report, skill));
  }
}

function buildMCPRow(report, mcp) {
  const row = document.createElement("div");
  row.className = "utilization-card";

  const band = normalizeBand(mcp.warning_band);
  row.appendChild(utilizationHeader("MCP", band, utilizationRatioText(mcp.exposure_known, mcp.utilization_ratio_pct, mcp.inference_source)));

  const body = document.createElement("div");
  body.className = "utilization-body";

  body.appendChild(utilizationGroup("Exposure", [
    ["Servers", bucketText(mcp.server_count_bucket)],
    ["Tools", bucketText(mcp.exposed_tool_count_bucket)],
    ["Context", bucketText(mcp.context_token_bucket)],
    ["Efficiency", bucketText(mcp.context_efficiency_bucket)],
  ]));

  body.appendChild(utilizationGroup("Usage", [
    ["Calls", String(numberOrZero(mcp.call_count))],
    ["Known", String(numberOrZero(mcp.known_call_count))],
    ["Unknown", String(numberOrZero(mcp.unknown_call_count))],
  ]));

  body.appendChild(chipPanel("Known called", mcp.unique_known_called_ids, `${numberOrZero(mcp.unique_unknown_called_count)} unknown called`));
  body.appendChild(chipPanel("Inventory", mcp.known_server_ids, `${numberOrZero(mcp.unknown_server_count)} unknown servers`));

  row.appendChild(body);

  // Advice block (FR-005 / FR-006).
  const adviceId = ADVICE_LOOKUP.mcp[band];
  const finding = adviceId ? findingById(report, adviceId) : null;
  if (finding && typeof finding.recommendation === "string" && finding.recommendation.length > 0) {
    const advice = document.createElement("p");
    advice.className = "band-advice";
    advice.textContent = finding.recommendation;
    row.appendChild(advice);
  }

  return row;
}

function buildSkillRow(report, skill) {
  const row = document.createElement("div");
  row.className = "utilization-card";

  const band = normalizeBand(skill.warning_band);
  row.appendChild(utilizationHeader("Skills", band, utilizationRatioText(skill.exposure_known, skill.utilization_ratio_pct, skill.inference_source)));

  const body = document.createElement("div");
  body.className = "utilization-body";

  body.appendChild(utilizationGroup("Exposure", [
    ["Skills", bucketText(skill.exposed_count_bucket)],
    ["Context", bucketText(skill.context_token_bucket)],
    ["Efficiency", bucketText(skill.context_efficiency_bucket)],
  ]));

  body.appendChild(utilizationGroup("Usage", [
    ["Executions", String(numberOrZero(skill.executed_count))],
    ["Known", String(Array.isArray(skill.known_executed_ids) ? skill.known_executed_ids.length : 0)],
    ["Unknown", String(numberOrZero(skill.unknown_executed_count))],
  ]));

  body.appendChild(chipPanel("Known executed", skill.known_executed_ids, `${numberOrZero(skill.unknown_executed_count)} unknown executed`));
  body.appendChild(chipPanel("Known exposed", skill.known_exposed_ids, `${numberOrZero(skill.unknown_exposed_count)} unknown exposed`));

  row.appendChild(body);

  // Advice block.
  const adviceId = ADVICE_LOOKUP.skill[band];
  const finding = adviceId ? findingById(report, adviceId) : null;
  if (finding && typeof finding.recommendation === "string" && finding.recommendation.length > 0) {
    const advice = document.createElement("p");
    advice.className = "band-advice";
    advice.textContent = finding.recommendation;
    row.appendChild(advice);
  }

  return row;
}

function utilizationHeader(label, band, ratioText) {
  const header = document.createElement("header");
  header.className = "utilization-header";
  const title = document.createElement("strong");
  title.textContent = label;
  const meta = document.createElement("div");
  meta.className = "utilization-header-meta";
  const chip = document.createElement("span");
  chip.className = `band-chip band-${band}`;
  chip.textContent = `${band} band`;
  const ratio = document.createElement("span");
  ratio.className = "utilization-ratio";
  ratio.textContent = ratioText;
  meta.append(chip, ratio);
  header.append(title, meta);
  return header;
}

function utilizationRatioText(exposureKnown, ratioPct, source) {
  if (exposureKnown === true) {
    return `${numberOrZero(ratioPct)}% utilization`;
  }
  const src = typeof source === "string" && source.length > 0 ? source : "unknown exposure";
  return `inferred from ${src}`;
}

function renderEnvironmentSignals(report) {
  const target = document.querySelector("#environment-signals");
  if (!target) return;
  target.replaceChildren();
  const ecosystem = report?.ecosystem;
  if (!ecosystem) {
    target.appendChild(emptyPanel("No environment signals detected."));
    return;
  }

  const table = document.createElement("table");
  table.className = "environment-table";
  table.innerHTML = "<thead><tr><th>Signal</th><th>Summary</th><th>Why it matters</th></tr></thead>";
  const body = document.createElement("tbody");
  environmentRows(report).forEach((row) => body.appendChild(environmentRow(row[0], row[1], row[2])));
  table.appendChild(body);
  target.appendChild(table);
}

function environmentRows(report) {
  const ecosystem = report?.ecosystem || {};
  const util = ecosystem.tooling_utilization || {};
  const mcp = util.mcp || {};
  const skill = util.skill || {};
  const fingerprints = Array.isArray(ecosystem.workflow_fingerprints) ? ecosystem.workflow_fingerprints : [];
  return [
    [
      "Runtime",
      joinCompact([ecosystem.client, ecosystem.operating_system, ecosystem.shell, ecosystem.version_control], "unknown"),
      "Basic host and client context for interpreting the scan.",
    ],
    [
      "Coding agents",
      summarizeList(ecosystem.coding_agents, 4),
      "Which agent surfaces appeared in the local scan.",
    ],
    [
      "Workflow tools",
      summarizeFingerprints(fingerprints),
      "Spec-driven or workflow tooling detected from bounded public fingerprints.",
    ],
    [
      "MCP surface",
      summarizeMCP(mcp),
      "High exposure with low use is a context-bloat signal.",
    ],
    [
      "Skill surface",
      summarizeSkills(skill),
      "Loaded skills should earn their context by being used.",
    ],
    [
      "Tooling inventory",
      summarizeInventory(ecosystem),
      "Package managers and plugins influence remediation recommendations.",
    ],
  ];
}

function environmentRow(signal, summary, detail) {
  const tr = document.createElement("tr");
  const signalCell = document.createElement("th");
  signalCell.scope = "row";
  signalCell.textContent = signal;
  const summaryCell = document.createElement("td");
  summaryCell.textContent = summary;
  const detailCell = document.createElement("td");
  detailCell.textContent = detail;
  tr.append(signalCell, summaryCell, detailCell);
  return tr;
}

function joinCompact(values, fallback) {
  const safe = (Array.isArray(values) ? values : []).filter((value) => typeof value === "string" && value.length > 0);
  return safe.length > 0 ? safe.join(" / ") : fallback;
}

function summarizeList(values, limit) {
  const safe = Array.isArray(values) ? values.filter(Boolean) : [];
  if (safe.length === 0) return "none detected";
  const shown = safe.slice(0, limit).join(", ");
  const hidden = safe.length - limit;
  return hidden > 0 ? `${shown} +${hidden} more` : shown;
}

function summarizeFingerprints(fingerprints) {
  const ranked = fingerprints
    .filter((fp) => fp && typeof fp.id === "string" && fp.id.length > 0)
    .slice()
    .sort((a, b) => numberOrZero(b.evidence_count) - numberOrZero(a.evidence_count));
  if (ranked.length === 0) return "none detected";
  const shown = ranked.slice(0, 4).map((fp) => {
    const flags = [];
    if (fp.active === true) flags.push("active");
    if (fp.installed === true) flags.push("installed");
    const confidence = typeof fp.confidence === "string" && fp.confidence ? fp.confidence : "unknown";
    return flags.length > 0 ? `${fp.id} (${flags.join(", ")})` : `${fp.id} (${confidence})`;
  });
  const hidden = ranked.length - shown.length;
  return hidden > 0 ? `${shown.join(", ")} +${hidden} more` : shown.join(", ");
}

function summarizeMCP(mcp) {
  const band = normalizeBand(mcp.warning_band);
  const ratio = utilizationRatioText(mcp.exposure_known, mcp.utilization_ratio_pct, mcp.inference_source);
  const calls = numberOrZero(mcp.call_count);
  const called = summarizeList(mcp.unique_known_called_ids, 3);
  const unknown = numberOrZero(mcp.unique_unknown_called_count);
  return `${band} band; ${ratio}; ${formatCompactNumber(calls)} calls; called ${called}${unknown > 0 ? ` +${unknown} unknown` : ""}`;
}

function summarizeSkills(skill) {
  const band = normalizeBand(skill.warning_band);
  const ratio = utilizationRatioText(skill.exposure_known, skill.utilization_ratio_pct, skill.inference_source);
  const executions = numberOrZero(skill.executed_count);
  const executed = summarizeList(skill.known_executed_ids, 3);
  const unknown = numberOrZero(skill.unknown_executed_count);
  return `${band} band; ${ratio}; ${formatCompactNumber(executions)} executions; used ${executed}${unknown > 0 ? ` +${unknown} unknown` : ""}`;
}

function summarizeInventory(ecosystem) {
  const parts = [];
  const plugins = summarizeList(ecosystem.known_plugins, 2);
  if (plugins !== "none detected") parts.push(`plugins: ${plugins}`);
  const packages = summarizeList(ecosystem.package_managers, 4);
  if (packages !== "none detected") parts.push(`packages: ${packages}`);
  const frameworks = summarizeList(ecosystem.workflow_frameworks, 3);
  if (frameworks !== "none detected") parts.push(`frameworks: ${frameworks}`);
  return parts.length > 0 ? parts.join("; ") : "no notable inventory";
}

function utilizationGroup(label, entries) {
  const group = document.createElement("section");
  group.className = "utilization-group";
  const title = document.createElement("h3");
  title.textContent = label;
  const facts = document.createElement("div");
  facts.className = "fact-grid";
  entries.forEach(([name, value]) => facts.appendChild(factTile(name, value)));
  group.append(title, facts);
  return group;
}

function factTile(label, value) {
  const item = document.createElement("span");
  item.className = "fact-tile";
  const k = document.createElement("small");
  k.textContent = label;
  const v = document.createElement("strong");
  v.textContent = value || "unknown";
  item.append(k, v);
  return item;
}

function chipPanel(label, values, extra) {
  const group = document.createElement("section");
  group.className = "mini-chip-group";
  const title = document.createElement("h3");
  title.textContent = label;
  const list = document.createElement("div");
  list.className = "chip-list";
  const safeValues = Array.isArray(values) ? values.filter(Boolean) : [];
  if (safeValues.length === 0) {
    list.appendChild(chip("none detected", "muted"));
  } else {
    safeValues.forEach((value) => list.appendChild(chip(String(value), "")));
  }
  if (extra && !String(extra).startsWith("0 ")) {
    list.appendChild(chip(extra, "unknown"));
  }
  group.append(title, list);
  return group;
}

function bucketText(value) {
  return typeof value === "string" && value.length > 0 ? value : "unknown";
}

function appendBucket(parent, label, value) {
  const span = document.createElement("span");
  span.className = "bucket-cell";
  const v = typeof value === "string" && value.length > 0 ? value : "—";
  span.textContent = `${label}: ${v}`;
  parent.appendChild(span);
}

function appendCount(parent, label, value) {
  const span = document.createElement("span");
  span.className = "count-cell";
  const n = typeof value === "number" ? value : 0;
  span.textContent = `${label}: ${n}`;
  parent.appendChild(span);
}

function renderEcosystem(ecosystem) {
  const target = document.querySelector("#ecosystem");
  if (!target) return;
  target.textContent = "";
  if (!ecosystem) {
    target.appendChild(emptyPanel("No ecosystem signals detected."));
    return;
  }
  const summary = document.createElement("div");
  summary.className = "ecosystem-summary";
  [
    ["Client", ecosystem.client || "unknown"],
    ["OS", ecosystem.operating_system || "unknown"],
    ["Shell", ecosystem.shell || "unknown"],
    ["Version control", ecosystem.version_control || "unknown"],
  ].forEach(([label, value]) => summary.appendChild(metricPill(label, value)));
  target.appendChild(summary);

  const groups = document.createElement("div");
  groups.className = "evidence-groups";
  groups.appendChild(chipGroup("Coding agents", ecosystem.coding_agents));
  groups.appendChild(chipGroup("Workflow frameworks", ecosystem.workflow_frameworks));
  groups.appendChild(chipGroup("MCPs", ecosystem.mcp_servers_known, `${numberOrZero(ecosystem.unknown_mcp_server_count)} unknown`));
  groups.appendChild(chipGroup("Skills", ecosystem.known_skills, `${numberOrZero(ecosystem.unknown_skill_count)} unknown`));
  groups.appendChild(chipGroup("Plugins", ecosystem.known_plugins, `${numberOrZero(ecosystem.unknown_plugin_count)} unknown`));
  groups.appendChild(chipGroup("Package managers", ecosystem.package_managers));
  target.appendChild(groups);
}

function renderReceipt(receipt, redactions) {
  const target = document.querySelector("#receipt");
  if (!target) return;
  target.textContent = "";
  if (!receipt) {
    target.appendChild(emptyPanel("No security receipt available."));
    return;
  }
  const boundary = document.createElement("p");
  boundary.className = "receipt-boundary";
  const strong = document.createElement("strong");
  strong.textContent = "Local redaction boundary:";
  boundary.append(
    strong,
    document.createTextNode(
      " secrets are removed on your computer before upload. The hosted service receives sanitized report JSON with category counts and placeholders, not the original redacted values."
    )
  );
  target.appendChild(boundary);

  const statusGrid = document.createElement("div");
  statusGrid.className = "receipt-status-grid";
  statusGrid.appendChild(statusTile("Model tokens for report", "0", "good"));
  statusGrid.appendChild(statusTile("Raw transcript to LLM", receipt.raw_transcript_sent_to_llm === true ? "yes" : "no", receipt.raw_transcript_sent_to_llm === true ? "bad" : "good"));
  statusGrid.appendChild(statusTile("Outbound during analysis", receipt.outbound_during_analysis === true ? "yes" : "no", receipt.outbound_during_analysis === true ? "bad" : "good"));
  statusGrid.appendChild(statusTile("Raw log TTL", receipt.raw_log_ttl || "unknown", receipt.raw_log_ttl === "not uploaded" ? "good" : "warn"));
  statusGrid.appendChild(statusTile("Secrets redacted locally", String(numberOrZero(receipt.secrets_redacted)), numberOrZero(receipt.secrets_redacted) > 0 ? "warn" : "good"));
  target.appendChild(statusGrid);
  target.appendChild(redactionGroup(redactions || receipt.redactions));
}

function metricPill(label, value) {
  const item = document.createElement("span");
  item.className = "metric-pill";
  const k = document.createElement("small");
  k.textContent = label;
  const v = document.createElement("strong");
  v.textContent = value || "unknown";
  item.append(k, v);
  return item;
}

function chipGroup(label, values, extra) {
  const group = document.createElement("section");
  group.className = "chip-group";
  const title = document.createElement("h3");
  title.textContent = label;
  group.appendChild(title);
  const list = document.createElement("div");
  list.className = "chip-list";
  const safeValues = Array.isArray(values) ? values.filter(Boolean) : [];
  if (safeValues.length === 0) {
    list.appendChild(chip("none detected", "muted"));
  } else {
    safeValues.forEach((value) => list.appendChild(chip(String(value), "")));
  }
  if (extra && !String(extra).startsWith("0 ")) {
    list.appendChild(chip(extra, "unknown"));
  }
  group.appendChild(list);
  return group;
}

function chip(text, tone) {
  const item = document.createElement("span");
  item.className = `info-chip${tone ? ` info-chip-${tone}` : ""}`;
  item.textContent = text;
  return item;
}

function statusTile(label, value, tone) {
  const item = document.createElement("div");
  item.className = `receipt-tile receipt-tile-${tone}`;
  item.setAttribute("aria-label", `${label}: ${value}`);
  const k = document.createElement("small");
  k.textContent = label;
  const v = document.createElement("strong");
  v.textContent = value;
  item.append(k, v);
  return item;
}

function redactionGroup(redactions) {
  const group = document.createElement("section");
  group.className = "chip-group redaction-group";
  const title = document.createElement("h3");
  title.textContent = "Local redaction categories";
  const note = document.createElement("p");
  note.textContent = "Only these category counts are included in the uploaded report.";
  const list = document.createElement("div");
  list.className = "chip-list";
  const entries = Object.entries(redactions || {}).filter(([, value]) => Number(value) > 0);
  if (entries.length === 0) {
    list.appendChild(chip("none detected", "muted"));
  } else {
    entries.forEach(([key, value]) => list.appendChild(chip(`${key}: ${value}`, "unknown")));
  }
  group.append(title, note, list);
  return group;
}

function emptyPanel(text) {
  const p = document.createElement("p");
  p.className = "empty-evidence";
  p.textContent = text;
  return p;
}

function numberOrZero(value) {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function findingEvidence(evidence) {
  if (!evidence) return "Deterministic evidence recorded.";
  const parts = [];
  if (evidence.description) parts.push(evidence.description);
  if (evidence.count) parts.push(`count: ${evidence.count}`);
  if (evidence.token_share_pct) parts.push(`token share: ${evidence.token_share_pct}%`);
  if (evidence.top_files && evidence.top_files.length) parts.push(`top files: ${evidence.top_files.join(", ")}`);
  return parts.join(" | ") || "Deterministic evidence recorded.";
}

function renderPluginDownloadPreview() {
  if (!route) return;
  updateReportDownloadLinks(route);
}

function updateReportDownloadLinks(activeRoute) {
  const extended = document.querySelector("#extended-report-link");
  const plugin = document.querySelector("#plugin-download-link");
  const jobIDInput = document.querySelector('input[name="source_report_job_id"]');
  const tokenInput = document.querySelector('input[name="source_report_token"]');
  if (!activeRoute) return;
  if (extended) {
    extended.href = `${window.location.origin}/api/public-reports/${activeRoute.jobID}/${activeRoute.token}/download.zip`;
  }
  if (plugin) {
    plugin.href = `${window.location.origin}/api/public-artifacts/${activeRoute.jobID}/${activeRoute.token}/plugin.zip`;
  }
  if (jobIDInput) {
    jobIDInput.value = activeRoute.jobID;
  }
  if (tokenInput) {
    tokenInput.value = activeRoute.token;
  }
}

function parseReportRoute() {
  const match = window.location.pathname.match(/^\/r\/([^/]+)\/([^/]+)$/);
  if (!match) return null;
  return { jobID: match[1], token: match[2] };
}

function setReportStatus(message) {
  document.querySelector("#report-status").textContent = message;
}

function setSessionStatus(message, hidden = false, html = false) {
  if (!sessionStatus) return;
  sessionStatus.hidden = hidden;
  if (html) {
    sessionStatus.innerHTML = message;
  } else {
    sessionStatus.textContent = message;
  }
}

async function responseError(response) {
  const error = new Error(await response.text());
  error.status = response.status;
  return error;
}

async function copyText(text, button) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
    } catch (error) {
      copyTextFallback(text);
    }
  } else {
    copyTextFallback(text);
  }
  const previous = button.textContent;
  button.textContent = "Copied";
  setTimeout(() => {
    button.textContent = previous;
  }, 1200);
  trackCTACopy(button?.dataset?.analyticsCtaId);
}

function copyTextFallback(text) {
  const field = document.createElement("textarea");
  field.value = text;
  field.setAttribute("readonly", "");
  field.style.position = "fixed";
  field.style.top = "-1000px";
  document.body.appendChild(field);
  field.select();
  document.execCommand("copy");
  field.remove();
}

function trackCTACopy(id) {
  if (!id) return;
  const path = `/api/analytics/cta-copy/${encodeURIComponent(id)}`;
  if (navigator.sendBeacon) {
    if (navigator.sendBeacon(path)) return;
  }
  fetch(path, { method: "POST", keepalive: true }).catch(() => {});
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
