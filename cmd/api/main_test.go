package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/priivacy-ai/agent-log-analyzer/internal/analytics"
	"github.com/priivacy-ai/agent-log-analyzer/internal/analyzer"
	"github.com/priivacy-ai/agent-log-analyzer/internal/app"
	"github.com/priivacy-ai/agent-log-analyzer/internal/localstore"
)

type fakeStore struct {
	job        app.Job
	report     analyzer.Report
	queueDepth int
}

type usageFakeStore struct {
	fakeStore
	events  []analytics.UsageEvent
	unlocks []app.EmailUnlock
}

type captureEmailSender struct {
	messages []emailMessage
}

func (sender *captureEmailSender) Send(message emailMessage) error {
	sender.messages = append(sender.messages, message)
	return nil
}

type failingEmailSender struct {
	err error
}

func (sender failingEmailSender) Send(emailMessage) error {
	return sender.err
}

func (f fakeStore) SaveUpload(jobID string, data []byte) (string, error) {
	return "", errors.New("not implemented")
}

func (f fakeStore) ReadUpload(path string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (f fakeStore) CreateJob(job app.Job) error {
	return errors.New("not implemented")
}

func (f fakeStore) ClaimNextJob() (app.Job, bool, error) {
	return app.Job{}, false, errors.New("not implemented")
}

func (f fakeStore) CompleteJob(job app.Job, report analyzer.Report) error {
	return errors.New("not implemented")
}

func (f fakeStore) FailJob(job app.Job, jobErr error) error {
	return errors.New("not implemented")
}

func (f fakeStore) GetJob(id string) (app.Job, error) {
	if id != f.job.ID {
		return app.Job{}, errors.New("not found")
	}
	return f.job, nil
}

func (f fakeStore) QueueDepth() (int, error) {
	return f.queueDepth, nil
}

func (f fakeStore) GetReport(id string) (analyzer.Report, error) {
	if id != f.job.ID {
		return analyzer.Report{}, errors.New("not found")
	}
	return f.report, nil
}

func loadReportFixture(t *testing.T, name string) analyzer.Report {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "fixtures", "reports", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report fixture %s: %v", name, err)
	}
	var report analyzer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode report fixture %s: %v", name, err)
	}
	return report
}

func (f *usageFakeStore) AppendUsageEvent(event analytics.UsageEvent) error {
	f.events = append(f.events, event)
	return nil
}

func (f *usageFakeStore) ReadUsageEvents(since time.Time, limit int) ([]analytics.UsageEvent, error) {
	var events []analytics.UsageEvent
	for _, event := range f.events {
		if !since.IsZero() && event.Timestamp.Before(since) {
			continue
		}
		events = append(events, event)
		if limit > 0 && len(events) >= limit {
			break
		}
	}
	return events, nil
}

func (f *usageFakeStore) ListEmailUnlocks(since time.Time, limit int) ([]app.EmailUnlock, error) {
	var unlocks []app.EmailUnlock
	for _, unlock := range f.unlocks {
		if !since.IsZero() && unlock.CreatedAt.Before(since) {
			continue
		}
		unlocks = append(unlocks, unlock)
		if limit > 0 && len(unlocks) >= limit {
			break
		}
	}
	return unlocks, nil
}

func TestSanitizePathRedactsDynamicIDs(t *testing.T) {
	for _, path := range []string{
		"/api/uploads/job-1234567890",
		"/api/uploads/job-1234567890/finalize",
		"/api/paid-uploads/job-1234567890",
		"/api/paid-uploads/job-1234567890/finalize",
		"/api/public-reports/job-1234567890/token-secret",
		"/api/public-reports/job-1234567890/token-secret/extended.md",
		"/api/public-reports/job-1234567890/token-secret/download.zip",
		"/api/public-artifacts/job-1234567890/token-secret/plugin.zip",
		"/api/report-deliveries",
		"/api/analytics/cta-copy/not-allowed-secret",
		"/api/jobs/job-1234567890",
		"/r/job-1234567890/token-secret",
	} {
		got := sanitizePath(path)
		if strings.Contains(got, "job-1234567890") || strings.Contains(got, "token-secret") {
			t.Fatalf("sanitizePath leaked job id for %q: %q", path, got)
		}
	}
	if got := sanitizePath("/api/analytics/cta-copy/npx_landing_hero"); got != "/api/analytics/cta-copy/npx_landing_hero" {
		t.Fatalf("expected known cta path to remain distinct, got %q", got)
	}
	if got := sanitizePath("/api/analytics/cta-copy/private-value"); got != "/api/analytics/cta-copy/:id" {
		t.Fatalf("expected unknown cta path to be collapsed, got %q", got)
	}
}

func TestCTACopyAnalyticsHandlerAllowsOnlyKnownIDs(t *testing.T) {
	handler := ctaCopyAnalyticsHandler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/analytics/cta-copy/npx_landing_hero", nil)
	req.SetPathValue("id", "npx_landing_hero")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for known cta, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/analytics/cta-copy/not-allowed", nil)
	req.SetPathValue("id", "not-allowed")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown cta, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestReportDeliveryEmailsReportPackAndPluginAttachments(t *testing.T) {
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	report := analyzer.Report{
		JobID:   "job-source-1234",
		Version: analyzer.Version,
		Score:   72,
		Metrics: analyzer.Metrics{
			Turns:           12,
			EstimatedTokens: 2400,
			SessionCount:    3,
		},
		EstimatedWaste: analyzer.WasteRange{Low: 12, High: 18},
		SecurityReceipt: analyzer.SecurityReceipt{
			RawTranscriptSentToLLM: false,
			OutboundDuringAnalysis: false,
			RawLogTTL:              "not uploaded",
		},
	}
	sourceJob := app.Job{
		ID:              "job-source-1234",
		Status:          app.StatusCompleted,
		ScanType:        app.ScanTypeSingle,
		ReportTokenHash: tokenHash("source-token"),
	}
	if err := store.CreateCompletedReport(sourceJob, report); err != nil {
		t.Fatal(err)
	}
	sender := &captureEmailSender{}
	body := `{"email":"Dev@Example.com","marketing_opt_in":true,"source_report_job_id":"job-source-1234","source_report_token":"source-token"}`
	req := httptest.NewRequest(http.MethodPost, "/api/report-deliveries", strings.NewReader(body))
	req.Host = "example.test"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	createReportDeliveryHandler(store, sender).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected accepted status, got %d: %s", rec.Code, rec.Body.String())
	}
	var response reportDeliveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if response.ReportURL != "http://example.test/api/public-reports/job-source-1234/source-token/download.zip" ||
		response.PluginURL != "http://example.test/api/public-artifacts/job-source-1234/source-token/plugin.zip" {
		t.Fatalf("unexpected download URLs in response: %#v", response)
	}
	unlock, err := store.GetEmailUnlock(response.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if unlock.Email != "dev@example.com" || !unlock.MarketingOptIn || unlock.SourceReportJobID != sourceJob.ID || unlock.Status != app.EmailUnlockUsed {
		t.Fatalf("unexpected stored email delivery record: %#v", unlock)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("expected one email, got %#v", sender.messages)
	}
	message := sender.messages[0]
	if message.To != "dev@example.com" ||
		!strings.Contains(message.Body, "Report pack: http://example.test/api/public-reports/job-source-1234/source-token/download.zip") ||
		!strings.Contains(message.Body, "Generated optimization plugin: http://example.test/api/public-artifacts/job-source-1234/source-token/plugin.zip") ||
		!strings.Contains(message.Body, "Spec Kitty training voucher") ||
		!strings.Contains(message.Body, "https://github.com/Priivacy-ai/spec-kitty") ||
		!strings.Contains(message.Body, "Choose your harness") ||
		!strings.Contains(message.Body, "claude plugin install") ||
		!strings.Contains(message.Body, "/agent-analyzer-status") ||
		!strings.Contains(message.Body, "harnesses/codex/AGENTS-snippet.md") ||
		!strings.Contains(message.Body, "harnesses/claude-desktop-mcp/README.md") ||
		!strings.Contains(message.Body, "Raw transcripts were not uploaded") {
		t.Fatalf("unexpected delivery email: %#v", message)
	}
	if len(message.Attachments) != 2 {
		t.Fatalf("expected report pack and plugin attachments, got %#v", message.Attachments)
	}
	if message.Attachments[0].Filename != "agent-analyzer-report-pack.zip" || message.Attachments[1].Filename != "agent-analyzer-optimization-plugin.zip" {
		t.Fatalf("unexpected attachment filenames: %#v", message.Attachments)
	}
	for _, attachment := range message.Attachments {
		if attachment.ContentType != "application/zip" || !bytes.HasPrefix(attachment.Data, []byte("PK")) {
			t.Fatalf("attachment %s is not a zip: type=%q len=%d", attachment.Filename, attachment.ContentType, len(attachment.Data))
		}
	}
}

func TestPluginReadyEmailBodyNamesHarnessDirectories(t *testing.T) {
	body := pluginReadyEmailBody("https://example.test/report", "https://example.test/plugin.zip")
	for _, want := range []string{
		"harnesses/codex/",
		"harnesses/opencode/",
		"harnesses/cursor/",
		"harnesses/kiro/",
		"harnesses/antigravity/",
		"harnesses/claude-desktop-mcp/",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("plugin ready email missing harness path %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "files under harnesses/") {
		t.Fatalf("plugin ready email should name specific harness directories:\n%s", body)
	}
}

func TestUsageStatsEndpointRequiresAdminToken(t *testing.T) {
	t.Setenv("CLAUDE_ANALYZER_ADMIN_TOKEN", "secret-admin-token")
	store := &usageFakeStore{}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage-stats", nil)
	rec := httptest.NewRecorder()

	buildMux(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized status, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUsageStatsEndpointReturnsAggregates(t *testing.T) {
	t.Setenv("CLAUDE_ANALYZER_ADMIN_TOKEN", "secret-admin-token")
	now := time.Now().UTC()
	store := &usageFakeStore{
		events: []analytics.UsageEvent{
			{Timestamp: now.Add(-time.Hour), Method: http.MethodGet, Path: "/healthz", Status: http.StatusOK, AuthSurface: "none", UserAgent: "curl"},
			{Timestamp: now.Add(-2 * time.Hour), Method: http.MethodGet, Path: "/api/admin/usage-stats", Status: http.StatusOK, AuthSurface: "admin_token", Authenticated: true, UserAgent: "browser", Browser: "chrome", OperatingSystem: "macos", DeviceClass: "desktop", Language: "en", Region: "US", ReferrerHost: "example.com", ClientHash: "client-1", ClientIPPrefix: "203.0.113.0/24", UTM: map[string]string{"utm_source": "launch", "utm_campaign": "spring"}},
			{Timestamp: now.Add(-48 * time.Hour), Method: http.MethodPost, Path: "/api/client-reports", Status: http.StatusCreated, AuthSurface: "none", UserAgent: "node", Browser: "node", OperatingSystem: "linux", DeviceClass: "automation"},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage-stats?since=24h", nil)
	req.Header.Set("Authorization", "Bearer secret-admin-token")
	rec := httptest.NewRecorder()

	buildMux(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected usage stats status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var stats analytics.UsageStats
	if err := json.NewDecoder(rec.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.EventCount != 2 || stats.Requests.Success != 2 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	if stats.UniqueClientHashes != 1 {
		t.Fatalf("expected one unique client hash, got %d", stats.UniqueClientHashes)
	}
	if stats.ByBrowser[0].Key != "chrome" || stats.ByOperatingSystem[0].Key != "macos" || stats.ByLanguage[0].Key != "en" {
		t.Fatalf("expected enriched aggregate stats, got %#v", stats)
	}
}

func TestLogRequestsAppendsSanitizedUsageEvent(t *testing.T) {
	t.Setenv("CLAUDE_ANALYZER_USAGE_HASH_SALT", "test-salt")
	store := &usageFakeStore{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusAccepted, map[string]string{"ok": "true"})
	})
	req := httptest.NewRequest(http.MethodGet, "/api/public-reports/job-1234567890/report-token", nil)
	req.RemoteAddr = "203.0.113.7:12345"
	req.Host = "analyzer.spec-kitty.ai"
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,de;q=0.8")
	req.Header.Set("Referer", "https://analyzer.spec-kitty.ai/r/job-secret/private-token?x=secret")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	rec := httptest.NewRecorder()

	logRequests(next, store).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected wrapped response, got %d", rec.Code)
	}
	if len(store.events) != 1 {
		t.Fatalf("expected one usage event, got %d", len(store.events))
	}
	event := store.events[0]
	if event.Path != "/api/public-reports/:id/:token" {
		t.Fatalf("expected sanitized path, got %q", event.Path)
	}
	if strings.Contains(event.Path, "job-1234567890") || strings.Contains(event.Path, "report-token") {
		t.Fatalf("usage event leaked path secret: %#v", event)
	}
	if event.Status != http.StatusAccepted || event.UserAgent != "browser" || event.ClientHash == "" {
		t.Fatalf("unexpected usage event: %#v", event)
	}
	if event.Host != "analyzer.spec-kitty.ai" || event.Scheme != "https" {
		t.Fatalf("expected host/scheme enrichment: %#v", event)
	}
	if event.Browser != "chrome" || event.BrowserMajor != "125" || event.OperatingSystem != "macos" || event.OSMajor != "14" || event.DeviceClass != "desktop" {
		t.Fatalf("expected browser/os enrichment: %#v", event)
	}
	if event.Language != "en" || event.Region != "US" || event.AcceptLanguage == "" {
		t.Fatalf("expected language enrichment: %#v", event)
	}
	if event.ClientIPVersion != "ipv4" || event.ClientIPPrefix != "203.0.113.0/24" {
		t.Fatalf("expected anonymized IP prefix: %#v", event)
	}
	if event.ReferrerPath != "/r/:id/:token" || strings.Contains(event.ReferrerPath, "private-token") {
		t.Fatalf("expected sanitized internal referrer: %#v", event)
	}
}

func TestAdminEmailUnlocksEndpointReturnsProjectedEmailRecords(t *testing.T) {
	t.Setenv("CLAUDE_ANALYZER_ADMIN_TOKEN", "secret-admin-token")
	now := time.Now().UTC()
	store := &usageFakeStore{
		unlocks: []app.EmailUnlock{
			{
				ID:                    "unlock-1",
				Email:                 "user@example.com",
				EmailHash:             "do-not-return",
				MarketingOptIn:        true,
				ConfirmationTokenHash: "do-not-return",
				FullScanTokenHash:     "do-not-return",
				Status:                app.EmailUnlockConfirmed,
				CreatedAt:             now.Add(-time.Hour),
				UpdatedAt:             now.Add(-time.Hour),
				ConfirmedAt:           now.Add(-30 * time.Minute),
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/email-unlocks?since=24h", nil)
	req.Header.Set("Authorization", "Bearer secret-admin-token")
	rec := httptest.NewRecorder()

	buildMux(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected email unlock export status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "user@example.com") || !strings.Contains(body, `"unique_emails":["user@example.com"]`) {
		t.Fatalf("expected projected email record, got %s", body)
	}
	for _, forbidden := range []string{"do-not-return", "confirmation_token_hash", "full_scan_token_hash", "email_hash"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("admin email export leaked sensitive field %q: %s", forbidden, body)
		}
	}
}

func TestGetPublicArtifactReturnsPluginZipForSingleScan(t *testing.T) {
	store := fakeStore{
		job: app.Job{
			ID:              "job-1234567890",
			Status:          app.StatusCompleted,
			ScanType:        app.ScanTypeSingle,
			ReportTokenHash: tokenHash("report-token"),
		},
		report: analyzer.Report{JobID: "job-1234567890", Version: "test"},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/public-artifacts/job-1234567890/report-token/plugin.zip", nil)
	req.SetPathValue("id", "job-1234567890")
	req.SetPathValue("token", "report-token")
	rec := httptest.NewRecorder()

	getPublicArtifactHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected plugin zip status, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("PK")) {
		t.Fatalf("expected zip response, got %q", rec.Body.String())
	}
}

func TestGetExtendedReportDownloadsPackage(t *testing.T) {
	store := fakeStore{
		job: app.Job{
			ID:              "job-1234567890",
			Status:          app.StatusCompleted,
			ScanType:        app.ScanTypeSingle,
			ReportTokenHash: tokenHash("report-token"),
		},
		report: analyzer.Report{
			JobID:          "job-1234567890",
			Version:        "test",
			Score:          64,
			EstimatedWaste: analyzer.WasteRange{Low: 12, High: 20},
			Metrics:        analyzer.Metrics{Turns: 10, SessionCount: 3, EstimatedTokens: 12000, ToolOutputTokens: 4000},
			Findings: []analyzer.Finding{
				{ID: "repeated_file_reads", Title: "Excessive repeated file reads", Severity: "high", CostImpact: "medium-high", Evidence: analyzer.FindingEvidence{Count: 4}, Recommendation: "Use targeted search before rereading files."},
			},
			SecurityReceipt: analyzer.SecurityReceipt{
				RawLogTTL:       "not uploaded",
				SecretsRedacted: 2,
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/public-reports/job-1234567890/report-token/download.zip", nil)
	req.SetPathValue("id", "job-1234567890")
	req.SetPathValue("token", "report-token")
	rec := httptest.NewRecorder()

	getExtendedReportHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected report package status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "application/zip") {
		t.Fatalf("expected zip content type, got %q", contentType)
	}
	reader, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("expected zip package: %v", err)
	}
	names := map[string]bool{}
	for _, file := range reader.File {
		names[file.Name] = true
	}
	for _, want := range []string{
		"agent-token-saving-field-guide.pdf",
		"personalized-agent-analyzer-report.pdf",
		"agent-analyzer-report.json",
		"plugin-preview.md",
		"partner-vouchers/spec-kitty-training-voucher.pdf",
		"partner-vouchers/spec-kitty-training-voucher.txt",
	} {
		if !names[want] {
			t.Fatalf("download package missing %q; got %#v", want, names)
		}
	}
	guidePDF := mustZipEntry(t, reader, "agent-token-saving-field-guide.pdf")
	reportPDF := mustZipEntry(t, reader, "personalized-agent-analyzer-report.pdf")
	voucherPDF := mustZipEntry(t, reader, "partner-vouchers/spec-kitty-training-voucher.pdf")
	for name, data := range map[string][]byte{"guide": guidePDF, "report": reportPDF, "voucher": voucherPDF} {
		if !bytes.HasPrefix(data, []byte("%PDF")) {
			t.Fatalf("%s PDF missing header: %q", name, data[:min(len(data), 8)])
		}
	}
	reportJSON := mustZipEntry(t, reader, "agent-analyzer-report.json")
	if !bytes.Contains(reportJSON, []byte(`"job_id": "job-1234567890"`)) || bytes.Contains(reportJSON, []byte("raw transcript")) {
		t.Fatalf("sanitized JSON entry unexpected:\n%s", string(reportJSON))
	}
	preview := mustZipEntry(t, reader, "plugin-preview.md")
	if !bytes.Contains(preview, []byte("Plugin Preview")) ||
		!bytes.Contains(preview, []byte("Harness install matrix")) ||
		!bytes.Contains(preview, []byte("Codex")) ||
		!bytes.Contains(preview, []byte("sanitized report JSON only")) {
		t.Fatalf("plugin preview missing expected copy:\n%s", string(preview))
	}
	voucher := mustZipEntry(t, reader, "partner-vouchers/spec-kitty-training-voucher.txt")
	if !regexp.MustCompile(`Code: [A-Z0-9]{6}`).Match(voucher) ||
		!bytes.Contains(voucher, []byte("20% off Spec Kitty training")) ||
		!bytes.Contains(voucher, []byte("https://spec-kitty.ai/training")) ||
		!bytes.Contains(voucher, []byte("Agent Analyzer partner credit")) {
		t.Fatalf("voucher missing code/discount:\n%s", string(voucher))
	}
	if !bytes.Contains(voucherPDF, []byte("SPEC KITTY")) || !bytes.Contains(voucherPDF, []byte("spec-kitty.ai/training")) {
		t.Fatalf("voucher PDF missing branding or training link")
	}
}

func TestExtendedReportPackageUsesRealReportFixtures(t *testing.T) {
	for _, name := range []string{
		"agent-analyzer-full-scan-report.json",
		"agent-analyzer-report.json",
	} {
		t.Run(name, func(t *testing.T) {
			report := loadReportFixture(t, name)
			analyzer.AttachRecommendation(&report)
			report.JobID = "job-real-fixture"
			store := fakeStore{
				job: app.Job{
					ID:              "job-real-fixture",
					Status:          app.StatusCompleted,
					ScanType:        app.ScanTypeSingle,
					ReportTokenHash: tokenHash("report-token"),
				},
				report: report,
			}
			req := httptest.NewRequest(http.MethodGet, "/api/public-reports/job-real-fixture/report-token/download.zip", nil)
			req.SetPathValue("id", "job-real-fixture")
			req.SetPathValue("token", "report-token")
			rec := httptest.NewRecorder()

			getExtendedReportHandler(store).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected report package status 200, got %d: %s", rec.Code, rec.Body.String())
			}
			reader, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
			if err != nil {
				t.Fatalf("expected zip package: %v", err)
			}
			for _, want := range []string{
				"agent-token-saving-field-guide.pdf",
				"personalized-agent-analyzer-report.pdf",
				"agent-analyzer-report.json",
				"plugin-preview.md",
			} {
				_ = mustZipEntry(t, reader, want)
			}
			reportJSON := mustZipEntry(t, reader, "agent-analyzer-report.json")
			wantRegistryVersion := []byte(`"registry_version": "` + analyzer.RegistryVersion() + `"`)
			if !bytes.Contains(reportJSON, []byte(`"source_reports"`)) ||
				!bytes.Contains(reportJSON, wantRegistryVersion) ||
				bytes.Contains(reportJSON, []byte("sk-ant-")) {
				t.Fatalf("real fixture report JSON missing expected sanitized/current data")
			}
			preview := mustZipEntry(t, reader, "plugin-preview.md")
			if !bytes.Contains(preview, []byte("semble")) ||
				!bytes.Contains(preview, []byte("sanitized report JSON only")) {
				t.Fatalf("plugin preview did not use benchmark-backed fixture recommendation:\n%s", string(preview))
			}
		})
	}
}

func TestGetPublicArtifactReturnsPluginZipForEmailFullScan(t *testing.T) {
	store := fakeStore{
		job: app.Job{
			ID:              "job-1234567890",
			Status:          app.StatusCompleted,
			ScanType:        app.ScanTypeFullScan,
			ReportTokenHash: tokenHash("report-token"),
		},
		report: analyzer.Report{
			JobID:   "job-1234567890",
			Version: "test",
			Findings: []analyzer.Finding{
				{ID: "repeated_file_reads", Severity: "high", CostImpact: "high"},
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/public-artifacts/job-1234567890/report-token/plugin.zip", nil)
	req.Host = "example.test"
	req.SetPathValue("id", "job-1234567890")
	req.SetPathValue("token", "report-token")
	rec := httptest.NewRecorder()

	getPublicArtifactHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected plugin zip status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("PK")) {
		t.Fatalf("expected zip response, got %q", rec.Body.String())
	}
}

func mustZipEntry(t *testing.T, reader *zip.Reader, name string) []byte {
	t.Helper()
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", name, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read zip entry %s: %v", name, err)
		}
		return data
	}
	t.Fatalf("zip entry %s not found", name)
	return nil
}

func TestReportPageServerRendersCompletedReport(t *testing.T) {
	store := fakeStore{
		job: app.Job{
			ID:              "job-1234567890",
			Status:          app.StatusCompleted,
			ScanType:        app.ScanTypeSingle,
			ReportTokenHash: tokenHash("report-token"),
		},
		report: analyzer.Report{
			JobID:   "job-1234567890",
			Version: "test",
			Score:   42,
			EstimatedWaste: analyzer.WasteRange{
				Low:  22,
				High: 30,
			},
			Metrics: analyzer.Metrics{
				Turns:            12,
				EstimatedTokens:  12000,
				ToolOutputTokens: 4000,
			},
			Findings: []analyzer.Finding{{
				ID:             "tool_output_bloat",
				Title:          "Large shell/tool output overhead",
				Severity:       "high",
				CostImpact:     "high",
				Evidence:       analyzer.FindingEvidence{TokenShare: 68},
				Recommendation: "Cap command output.",
			}},
			Timeline: []analyzer.TimelinePoint{{
				Turn:            10,
				EstimatedTokens: 10000,
				ToolTokens:      3000,
				Rereads:         2,
				Retries:         1,
			}},
			SourceReports: []analyzer.SourceReport{
				{
					SourceID:       "claude_code",
					SourceLabel:    "Claude Code",
					LogCount:       1,
					LogRefs:        []analyzer.AnalyzedLogRef{{Label: "Claude Code log 1", LocalRef: "claude_code-safe123", SizeBucket: "10-100 KB"}},
					Score:          42,
					EstimatedWaste: analyzer.WasteRange{Low: 22, High: 30},
					Metrics:        analyzer.Metrics{EstimatedTokens: 10000, ToolOutputTokens: 3000, Rereads: 2, FailedCommands: 1},
					Findings: []analyzer.Finding{{
						Title:      "Large shell/tool output overhead",
						Severity:   "high",
						CostImpact: "high",
					}},
					Timeline: []analyzer.TimelinePoint{{
						Turn:            10,
						EstimatedTokens: 10000,
						ToolTokens:      3000,
						Rereads:         2,
						Retries:         1,
					}},
				},
				{
					SourceID:       "codex",
					SourceLabel:    "Codex",
					LogCount:       1,
					LogRefs:        []analyzer.AnalyzedLogRef{{Label: "Codex log 1", LocalRef: "codex-safe456", SizeBucket: "100 KB-1 MB"}},
					Score:          55,
					EstimatedWaste: analyzer.WasteRange{Low: 12, High: 20},
					Metrics:        analyzer.Metrics{EstimatedTokens: 2000, ToolOutputTokens: 200},
					Timeline: []analyzer.TimelinePoint{{
						Turn:            5,
						EstimatedTokens: 2000,
						ToolTokens:      200,
					}},
				},
			},
			ImmediateFixes: []string{"Use narrower shell commands."},
			Ecosystem: analyzer.Ecosystem{
				Client:          "claude-code",
				OperatingSystem: "macos",
				MCPServersKnown: []string{"github"},
				ToolingUtilization: analyzer.ToolingUtilization{
					MCP: analyzer.MCPUtilization{
						WarningBand:              "high",
						UtilizationRatioPct:      10,
						ServerCountBucket:        "many",
						ContextTokenBucket:       "15k_50k",
						CallCount:                3,
						UniqueKnownCalledIDs:     []string{"github"},
						UniqueUnknownCalledCount: 1,
					},
				},
			},
			SecurityReceipt: analyzer.SecurityReceipt{
				SecretsRedacted: 3,
				RawLogTTL:       "not uploaded",
			},
			Recommendation: &analyzer.RecommendationSet{
				Primary: &analyzer.TokenSavingRecommendation{
					PrimaryToolID:   "rtk",
					PrimaryToolName: "RTK (Rust Token Killer, rtk-ai/rtk)",
					PrimaryToolURL:  "https://github.com/rtk-ai/rtk",
					Reason:          analyzer.ReasonAbsent,
					SignalIDs:       []analyzer.Signal{analyzer.SignalToolOutputBloat},
					Confidence:      analyzer.ConfidenceLow,
					RiskLevel:       analyzer.RiskHigh,
					InstallPolicy:   analyzer.PolicyRecommendWithWaiver,
				},
				RegistryVersion: analyzer.RegistryVersion(),
				EngineVersion:   analyzer.EngineVersion(),
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/r/job-1234567890/report-token", nil)
	req.SetPathValue("id", "job-1234567890")
	req.SetPathValue("token", "report-token")
	rec := httptest.NewRecorder()

	reportPageHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("expected html content type, got %q", contentType)
	}
	if cacheControl := rec.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("expected no-store cache control, got %q", cacheControl)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"42",
		"Large shell/tool output overhead",
		"Cap noisy command output",
		"Download custom skills",
		"Download report pack",
		"Download the report pack and your custom plugin for free",
		"Email required: enter it once to unlock both downloads and receive the links.",
		"Email for report pack + generated plugin",
		"upcoming Spec Kitty Teamspace launch",
		"Unlock my custom plugin",
		"0 model tokens used to generate this report.",
		"Model tokens for report",
		"Copy line",
		"claude_code-safe123",
		"/allowed-tools.html",
		"RTK (Rust Token Killer, rtk-ai/rtk)",
		"https://github.com/rtk-ai/rtk",
		"Raw log TTL: not uploaded",
		"Environment signals",
		"MCP surface",
		"Agent Logs Analyzed",
		"Claude Code",
		"Codex",
		"timeline-bar",
		"potential savings",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("server-rendered report missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<th>Turn</th>") {
		t.Fatalf("server-rendered report regressed to tabular timeline: %s", body)
	}
	if strings.Contains(body, "Checkout enforcement is not active in this test build") || strings.Contains(body, "$10 / €10 target price") {
		t.Fatalf("server-rendered report leaked test-build pricing copy: %s", body)
	}
	if strings.Contains(body, "Find out what&#39;s wasting your Claude Code tokens") || strings.Contains(body, "Run the local analyzer") {
		t.Fatalf("server-rendered report returned onboarding shell instead of report: %s", body)
	}
}

func TestReportPageRendersRealReportFixtures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		wantLabels  []string
		wantMissing []string
	}{
		{
			name:        "agent-analyzer-full-scan-report.json",
			wantLabels:  []string{"Agent Logs Analyzed", "Claude Code", "Codex", "OpenCode", "Recommended tools to address waste", "semble"},
			wantMissing: []string{"claude-context adds semantic retrieval", "ccusage reads Claude Code usage data"},
		},
		{
			name:        "agent-analyzer-report.json",
			wantLabels:  []string{"Agent Logs Analyzed", "Claude Code", "Codex", "Kiro IDE", "OpenCode", "Recommended tools to address waste", "semble"},
			wantMissing: []string{"claude-context adds semantic retrieval", "ccusage reads Claude Code usage data"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := loadReportFixture(t, tc.name)
			analyzer.AttachRecommendation(&report)
			store := fakeStore{
				job: app.Job{
					ID:               "job-real-fixture",
					Status:           app.StatusCompleted,
					ScanType:         app.ScanTypePaidBundle,
					ReportTokenHash:  tokenHash("report-token"),
					WaiverAcceptedAt: time.Now().UTC(),
				},
				report: report,
			}
			req := httptest.NewRequest(http.MethodGet, "/r/job-real-fixture/report-token", nil)
			req.SetPathValue("id", "job-real-fixture")
			req.SetPathValue("token", "report-token")
			rec := httptest.NewRecorder()

			reportPageHandler(store).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, want := range append(tc.wantLabels,
				"Download report pack",
				"Email for report pack + generated plugin",
				"Unlock my custom plugin",
				"Security Receipt",
				"Raw log TTL: not uploaded",
				"0 model tokens used to generate this report.",
				"benchmark-backed operating guidance",
				"No unproven reducer installs",
			) {
				if !strings.Contains(body, want) {
					t.Fatalf("fixture-rendered report missing %q", want)
				}
			}
			for _, forbidden := range append(tc.wantMissing, "sk-ant-") {
				if strings.Contains(body, forbidden) {
					t.Fatalf("fixture-rendered report contained forbidden text %q", forbidden)
				}
			}
		})
	}
}

func TestTimelineChartSamplesAcrossFullTimeline(t *testing.T) {
	points := make([]analyzer.TimelinePoint, 120)
	for i := range points {
		points[i] = analyzer.TimelinePoint{
			Turn:            i + 1,
			EstimatedTokens: (i + 1) * 100,
		}
	}

	sampled := sampleTimelinePoints(points, 60)
	if len(sampled) != 60 {
		t.Fatalf("expected 60 sampled points, got %d", len(sampled))
	}
	if sampled[0].Turn != 1 || sampled[len(sampled)-1].Turn != 120 {
		t.Fatalf("expected full-range sampling from first to last turn, got first=%#v last=%#v", sampled[0], sampled[len(sampled)-1])
	}
	if sampled[1].EstimatedTokens <= sampled[0].EstimatedTokens {
		t.Fatalf("expected sampled timeline to preserve accumulation, got %#v", sampled[:2])
	}
}

func TestProblemBubbleDiameterCapsByFindingCount(t *testing.T) {
	maxDiameter := maxBubbleDiameter(4)
	if maxDiameter >= 268 {
		t.Fatalf("expected four-bubble row to cap below default max, got %d", maxDiameter)
	}
	if got := bubbleDiameter(100, 100, maxDiameter); got > maxDiameter {
		t.Fatalf("bubble diameter exceeded row cap: got %d cap %d", got, maxDiameter)
	}
}

func TestReportPageRequiresReportToken(t *testing.T) {
	store := fakeStore{
		job: app.Job{
			ID:              "job-1234567890",
			Status:          app.StatusCompleted,
			ReportTokenHash: tokenHash("report-token"),
		},
		report: analyzer.Report{JobID: "job-1234567890", Version: "test"},
	}
	req := httptest.NewRequest(http.MethodGet, "/r/job-1234567890/wrong-token", nil)
	req.SetPathValue("id", "job-1234567890")
	req.SetPathValue("token", "wrong-token")
	rec := httptest.NewRecorder()

	reportPageHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetPublicArtifactReturnsPluginZipForPaidWaiver(t *testing.T) {
	store := fakeStore{
		job: app.Job{
			ID:                   "job-1234567890",
			Status:               app.StatusCompleted,
			ScanType:             app.ScanTypePaidBundle,
			ReportTokenHash:      tokenHash("report-token"),
			WaiverAcceptedAt:     time.Now().UTC(),
			UploadTokenExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		},
		report: analyzer.Report{
			JobID:   "job-1234567890",
			Version: "test",
			Findings: []analyzer.Finding{
				{ID: "repeated_file_reads", Severity: "high", CostImpact: "high"},
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/public-artifacts/job-1234567890/report-token/plugin.zip", nil)
	req.Host = "example.test"
	req.SetPathValue("id", "job-1234567890")
	req.SetPathValue("token", "report-token")
	rec := httptest.NewRecorder()

	getPublicArtifactHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected plugin zip status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "application/zip" {
		t.Fatalf("expected zip content type, got %q", contentType)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("PK")) {
		t.Fatalf("expected zip response, got %q", rec.Body.String())
	}
}

func TestGetJobHandlerDoesNotReturnStoragePaths(t *testing.T) {
	store := fakeStore{
		job: app.Job{
			ID:              "job-1234567890",
			Status:          app.StatusCompleted,
			UploadPath:      "s3://private-upload-bucket/uploads/job-1234567890.log",
			ReportPath:      "s3://private-report-bucket/reports/job-1234567890.json",
			UploadTokenHash: "private-upload-token-hash",
			ReportTokenHash: "private-report-token-hash",
			CreatedAt:       time.Now().UTC(),
			UpdatedAt:       time.Now().UTC(),
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/job-1234567890", nil)
	req.SetPathValue("id", "job-1234567890")
	rec := httptest.NewRecorder()

	getJobHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private-upload-bucket") || strings.Contains(rec.Body.String(), "private-report-bucket") {
		t.Fatalf("job response leaked storage path: %s", rec.Body.String())
	}
	var job app.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatalf("response is not valid job JSON: %v", err)
	}
	if job.UploadPath != "" || job.ReportPath != "" {
		t.Fatalf("expected paths stripped from response: %#v", job)
	}
	if job.UploadTokenHash != "" || job.ReportTokenHash != "" {
		t.Fatalf("expected token hashes stripped from response: %#v", job)
	}
}

func TestLegacyUploadRoutesAreNotMounted(t *testing.T) {
	store := fakeStore{job: app.Job{ID: "job-1234567890", Status: app.StatusCompleted}}
	mux := buildMux(store)
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/jobs"},
		{http.MethodPost, "/api/upload-url"},
		{http.MethodPost, "/api/jobs/job-1234567890/finalize"},
		{http.MethodGet, "/api/reports/job-1234567890"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s should not be mounted, got status %d", tc.method, tc.path, rec.Code)
		}
	}
}

func TestHealthRoutes(t *testing.T) {
	mux := buildMux(fakeStore{})
	for _, path := range []string{"/health", "/healthz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s got status %d", path, rec.Code)
		}
		if body := strings.TrimSpace(rec.Body.String()); body != `{"status":"ok"}` {
			t.Fatalf("%s returned unexpected body %q", path, body)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("%s content-type = %q", path, got)
		}
	}
}

func TestBenchmarkProofRoutesAreLinkedAndServed(t *testing.T) {
	mux := buildMux(fakeStore{})
	for _, tc := range []struct {
		path string
		want string
	}{
		{"/", "/proof/"},
		{"/proof/", "Benchmark Landscape"},
		{"/proof/methodology.html", "Privacy Boundary"},
		{"/proof/results.html", "Primary sanitized benchmark recordings"},
		{"/proof/benchmark-comparison.html", "External Benchmark Comparison"},
		{"/docs/benchmarks/repeated-benchmark-suite.md", "Repeated Benchmark Suite"},
		{"/docs/benchmarks/api-cost-translation.md", "API Cost Translation"},
		{"/docs/benchmarks/primary-data/", "index.json"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("%s got status %d: %s", tc.path, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("%s missing %q", tc.path, tc.want)
			}
		})
	}
}

func TestAnalysisSessionCurlFlow(t *testing.T) {
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/analysis-sessions", nil)
	req.Host = "example.test"
	rec := httptest.NewRecorder()

	createAnalysisSessionHandler(store, 100, 15*time.Minute).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var session analysisSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("response is not valid session JSON: %v", err)
	}
	if session.JobID == "" || session.Token == "" || session.Command == "" || session.Prompt == "" {
		t.Fatalf("unexpected session response: %#v", session)
	}
	if strings.Contains(session.Command, ".log?X-Amz") {
		t.Fatalf("expected token curl command, got signed URL command: %s", session.Command)
	}

	uploadReq := httptest.NewRequest(http.MethodPut, "/api/uploads/"+session.JobID, strings.NewReader("log line"))
	uploadReq.SetPathValue("id", session.JobID)
	uploadReq.Header.Set("Authorization", "Bearer "+session.Token)
	uploadRec := httptest.NewRecorder()
	tokenUploadHandler(store).ServeHTTP(uploadRec, uploadReq)

	if uploadRec.Code != http.StatusCreated {
		t.Fatalf("expected upload status 201, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}

	finalizeReq := httptest.NewRequest(http.MethodPost, "/api/uploads/"+session.JobID+"/finalize", nil)
	finalizeReq.SetPathValue("id", session.JobID)
	finalizeReq.Header.Set("Authorization", "Bearer "+session.Token)
	finalizeRec := httptest.NewRecorder()
	finalizeTokenUploadHandler(store).ServeHTTP(finalizeRec, finalizeReq)

	if finalizeRec.Code != http.StatusAccepted {
		t.Fatalf("expected finalize status 202, got %d: %s", finalizeRec.Code, finalizeRec.Body.String())
	}
	job, err := store.GetJob(session.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != app.StatusPending {
		t.Fatalf("expected pending job, got %#v", job)
	}

	secondUpload := httptest.NewRequest(http.MethodPut, "/api/uploads/"+session.JobID, strings.NewReader("again"))
	secondUpload.SetPathValue("id", session.JobID)
	secondUpload.Header.Set("Authorization", "Bearer "+session.Token)
	secondRec := httptest.NewRecorder()
	tokenUploadHandler(store).ServeHTTP(secondRec, secondUpload)

	if secondRec.Code != http.StatusConflict {
		t.Fatalf("expected reused upload status 409, got %d: %s", secondRec.Code, secondRec.Body.String())
	}
}

func TestClientReportUploadStoresSanitizedReportOnly(t *testing.T) {
	root := t.TempDir()
	store, err := localstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	report := analyzer.Report{
		JobID:   "local",
		Version: analyzer.Version,
		Score:   72,
		Metrics: analyzer.Metrics{Turns: 12, EstimatedTokens: 2400},
		SecurityReceipt: analyzer.SecurityReceipt{
			RawTranscriptSentToLLM: false,
			OutboundDuringAnalysis: false,
			SecretsRedacted:        2,
			RawLogTTL:              "not uploaded",
		},
		SourceReports: []analyzer.SourceReport{
			{
				SourceID: "codex",
				LogRefs: []analyzer.AnalyzedLogRef{
					{SizeBucket: "10-100 KB", ContentHashSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
				},
			},
		},
		AggregateEvent: analyzer.AggregateSafeEvent{Event: "analysis_completed", ParserType: "jsonl"},
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/client-reports", bytes.NewReader(body))
	req.Host = "example.test"
	rec := httptest.NewRecorder()

	createClientReportHandler(store, 0).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected client report status 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var session analysisSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("response is not valid session JSON: %v", err)
	}
	if session.Token != "" || session.UploadPath != "" || !strings.Contains(session.ReportURL, "/r/") {
		t.Fatalf("expected report-only response, got %#v", session)
	}
	if session.ExpiresAt != nil || strings.Contains(rec.Body.String(), "expires_at") {
		t.Fatalf("expected permanent report response without expires_at, got %#v body=%s", session, rec.Body.String())
	}
	stored, err := store.GetReport(session.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.JobID != session.JobID || stored.SecurityReceipt.RawLogTTL != "not uploaded" {
		t.Fatalf("stored report mismatch: %#v", stored)
	}
	if stored.Recommendation == nil || stored.Recommendation.EngineVersion != analyzer.EngineVersion() {
		t.Fatalf("stored report did not receive current recommendation set: %#v", stored.Recommendation)
	}
	job, err := store.GetJob(session.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != app.StatusCompleted || job.UploadPath != "" || job.UploadTokenHash != "" {
		t.Fatalf("expected completed report-only job, got %#v", job)
	}
	analyticsData, err := os.ReadFile(filepath.Join(root, "analytics", "events.jsonl"))
	if err != nil {
		t.Fatalf("read analytics event: %v", err)
	}
	if !strings.Contains(string(analyticsData), `"analyzed_log_hashes"`) ||
		!strings.Contains(string(analyticsData), `"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"`) {
		t.Fatalf("analytics event missing analyzed log hash: %s", analyticsData)
	}
}

func TestClientReportUploadRejectsUnsafeReceipt(t *testing.T) {
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	report := analyzer.Report{
		Version: analyzer.Version,
		Metrics: analyzer.Metrics{Turns: 1},
		SecurityReceipt: analyzer.SecurityReceipt{
			RawTranscriptSentToLLM: true,
		},
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/client-reports", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	createClientReportHandler(store, 15*time.Minute).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected unsafe report status 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPaidClientReportUploadStoresSanitizedAggregateAndArtifactWorks(t *testing.T) {
	t.Setenv("CLAUDE_ANALYZER_ENABLE_LOCAL_PAID_SESSIONS", "true")
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	report := paidAggregateReport()
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/paid-client-reports", bytes.NewReader(body))
	req.Host = "example.test"
	req.Header.Set("X-Waiver-Accepted", "true")
	req.Header.Set("X-Waiver-Acknowledgment", "I accept at my own risk")
	rec := httptest.NewRecorder()

	createPaidClientReportHandler(store, 15*time.Minute).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected paid report status 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var session analysisSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("response is not valid session JSON: %v", err)
	}
	if session.Token != "" || session.UploadPath != "" || !strings.Contains(session.ReportURL, "/r/") {
		t.Fatalf("expected report-only paid response, got %#v", session)
	}
	job, err := store.GetJob(session.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.ScanType != app.ScanTypePaidBundle || job.Status != app.StatusCompleted || job.UploadPath != "" || job.WaiverAcceptedAt.IsZero() {
		t.Fatalf("expected completed waiver-gated paid report job without upload path, got %#v", job)
	}
	stored, err := store.GetReport(session.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AggregateEvent.ParserType != "paid_bundle" || stored.SecurityReceipt.RawLogTTL != "not uploaded" {
		t.Fatalf("stored paid report is not sanitized aggregate: %#v", stored)
	}

	artifactReq := httptest.NewRequest(http.MethodGet, "/api/public-artifacts/"+session.JobID+"/token/plugin.zip", nil)
	artifactReq.Host = "example.test"
	artifactReq.SetPathValue("id", session.JobID)
	artifactReq.SetPathValue("token", strings.TrimPrefix(session.ReportPath, "/r/"+session.JobID+"/"))
	artifactRec := httptest.NewRecorder()
	getPublicArtifactHandler(store).ServeHTTP(artifactRec, artifactReq)

	if artifactRec.Code != http.StatusOK {
		t.Fatalf("expected plugin zip from sanitized paid report, got %d: %s", artifactRec.Code, artifactRec.Body.String())
	}
	if !bytes.HasPrefix(artifactRec.Body.Bytes(), []byte("PK")) {
		t.Fatalf("expected zip response")
	}
}

func TestPaidClientReportUploadRejectsNonAggregateReport(t *testing.T) {
	t.Setenv("CLAUDE_ANALYZER_ENABLE_LOCAL_PAID_SESSIONS", "true")
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	report := paidAggregateReport()
	report.AggregateEvent.ParserType = "jsonl"
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/paid-client-reports", bytes.NewReader(body))
	req.Header.Set("X-Waiver-Accepted", "true")
	req.Header.Set("X-Waiver-Acknowledgment", "I accept at my own risk")
	rec := httptest.NewRecorder()

	createPaidClientReportHandler(store, 15*time.Minute).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected non-aggregate paid report rejection 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestEmailUnlockConfirmAndFullScanUploadLifecycle(t *testing.T) {
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sender := &captureEmailSender{}
	sourceReport := analyzer.Report{
		JobID:   "source-job",
		Version: analyzer.Version,
		Score:   72,
		Metrics: analyzer.Metrics{Turns: 12, EstimatedTokens: 2400},
		SecurityReceipt: analyzer.SecurityReceipt{
			RawTranscriptSentToLLM: false,
			OutboundDuringAnalysis: false,
			RawLogTTL:              "not uploaded",
		},
	}
	sourceJob := app.Job{
		ID:              "job-source-1234",
		Status:          app.StatusCompleted,
		ScanType:        app.ScanTypeSingle,
		ReportTokenHash: tokenHash("source-token"),
	}
	if err := store.CreateCompletedReport(sourceJob, sourceReport); err != nil {
		t.Fatal(err)
	}

	form := "email=dev%40example.com&marketing_opt_in=1&source_report_job_id=job-source-1234&source_report_token=source-token"
	req := httptest.NewRequest(http.MethodPost, "/api/email-unlocks", strings.NewReader(form))
	req.Host = "example.test"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	createEmailUnlockHandler(store, sender).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected email unlock page status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0].Body, "/email/confirm/") {
		t.Fatalf("expected confirmation email, got %#v", sender.messages)
	}
	confirmPath := extractPathFromEmail(sender.messages[0].Body, "/email/confirm/")
	parts := strings.Split(confirmPath, "/")
	if len(parts) < 5 {
		t.Fatalf("could not parse confirm path %q", confirmPath)
	}
	confirmReq := httptest.NewRequest(http.MethodGet, confirmPath, nil)
	confirmReq.Host = "example.test"
	confirmReq.SetPathValue("id", parts[3])
	confirmReq.SetPathValue("token", parts[4])
	confirmRec := httptest.NewRecorder()
	confirmEmailUnlockHandler(store, sender).ServeHTTP(confirmRec, confirmReq)

	if confirmRec.Code != http.StatusOK {
		t.Fatalf("expected confirm status 200, got %d: %s", confirmRec.Code, confirmRec.Body.String())
	}
	if !strings.Contains(confirmRec.Body.String(), "npx --yes agent-analyzer@latest full-scan --token") {
		t.Fatalf("confirmation page missing full-scan command: %s", confirmRec.Body.String())
	}
	if !strings.Contains(confirmRec.Body.String(), `class="copy-agents-line"`) || !strings.Contains(confirmRec.Body.String(), "Copy command") || !strings.Contains(confirmRec.Body.String(), "/report-actions.js") {
		t.Fatalf("confirmation page missing command copy widget: %s", confirmRec.Body.String())
	}
	if len(sender.messages) != 2 || !strings.Contains(sender.messages[1].Body, "full-scan --token") {
		t.Fatalf("expected full-scan command email, got %#v", sender.messages)
	}
	fullScanToken := extractTokenAfter(sender.messages[1].Body, "--token '")
	fullScanReport := paidAggregateReport()
	fullScanReport.AggregateEvent.ParserType = "full_scan_bundle"
	body, err := json.Marshal(fullScanReport)
	if err != nil {
		t.Fatal(err)
	}
	uploadReq := httptest.NewRequest(http.MethodPost, "/api/full-scan-client-reports", bytes.NewReader(body))
	uploadReq.Host = "example.test"
	uploadReq.Header.Set("Authorization", "Bearer "+fullScanToken)
	uploadReq.Header.Set("Content-Type", "application/json")
	uploadRec := httptest.NewRecorder()
	createFullScanClientReportHandler(store, sender, 15*time.Minute).ServeHTTP(uploadRec, uploadReq)

	if uploadRec.Code != http.StatusCreated {
		t.Fatalf("expected full-scan upload status 201, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}
	var response analysisSessionResponse
	if err := json.Unmarshal(uploadRec.Body.Bytes(), &response); err != nil {
		t.Fatalf("response is not valid session JSON: %v", err)
	}
	job, err := store.GetJob(response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.ScanType != app.ScanTypeFullScan || job.Status != app.StatusCompleted {
		t.Fatalf("expected completed full-scan job, got %#v", job)
	}
	if len(sender.messages) != 3 || !strings.Contains(sender.messages[2].Body, "/api/public-artifacts/") {
		t.Fatalf("expected plugin-ready email, got %#v", sender.messages)
	}
	reuseReq := httptest.NewRequest(http.MethodPost, "/api/full-scan-client-reports", bytes.NewReader(body))
	reuseReq.Header.Set("Authorization", "Bearer "+fullScanToken)
	reuseReq.Header.Set("Content-Type", "application/json")
	reuseRec := httptest.NewRecorder()
	createFullScanClientReportHandler(store, sender, 15*time.Minute).ServeHTTP(reuseRec, reuseReq)
	if reuseRec.Code != http.StatusConflict {
		t.Fatalf("expected reused token conflict, got %d: %s", reuseRec.Code, reuseRec.Body.String())
	}
}

func TestEmailUnlockSendFailureRendersActionablePage(t *testing.T) {
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sender := failingEmailSender{err: errEmailDelivery{provider: "ses", detail: "sandbox", err: errors.New("MessageRejected: sandbox")}}
	req := httptest.NewRequest(http.MethodPost, "/api/email-unlocks", strings.NewReader("email=dev%40example.com"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()

	createEmailUnlockHandler(store, sender).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected service unavailable, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Email delivery is not active yet") {
		t.Fatalf("expected actionable SES message, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Request failed") {
		t.Fatalf("expected specialized email page, got %s", rec.Body.String())
	}
}

func TestEmailUnlockSendFailureDoesNotExposeConfirmationToken(t *testing.T) {
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sender := failingEmailSender{err: errEmailDelivery{provider: "ses", detail: "sandbox", err: errors.New("MessageRejected: sandbox")}}
	req := httptest.NewRequest(http.MethodPost, "/api/email-unlocks", strings.NewReader("email=dev%40example.com"))
	req.Host = "example.test"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()

	createEmailUnlockHandler(store, sender).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected service unavailable, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "/email/confirm/") || strings.Contains(rec.Body.String(), "full-scan --token") {
		t.Fatalf("send failure must not expose confirmation or full-scan tokens: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Email delivery is not active yet") {
		t.Fatalf("expected SES delivery explanation, got %s", rec.Body.String())
	}
}

func extractPathFromEmail(body, marker string) string {
	index := strings.Index(body, marker)
	if index < 0 {
		return ""
	}
	end := index
	for end < len(body) && body[end] != '\n' && body[end] != '\r' && body[end] != ' ' {
		end++
	}
	return body[index:end]
}

func extractQuotedPath(body, marker string) string {
	index := strings.Index(body, marker)
	if index < 0 {
		return ""
	}
	end := index
	for end < len(body) && body[end] != '"' && body[end] != '\'' && body[end] != '<' {
		end++
	}
	return body[index:end]
}

func extractTokenAfter(body, marker string) string {
	index := strings.Index(body, marker)
	if index < 0 {
		return ""
	}
	index += len(marker)
	end := index
	for end < len(body) && body[end] != '\'' && body[end] != '\n' && body[end] != '\r' {
		end++
	}
	return body[index:end]
}

func TestPaidBundleUploadRequiresPaidTokenAndLimit(t *testing.T) {
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token := "paid-token"
	job := app.Job{
		ID:                   "job-paid-token",
		Status:               app.StatusUploading,
		ScanType:             app.ScanTypePaidBundle,
		MaxUploadBytes:       maxPaidUploadBytes,
		UploadTokenHash:      tokenHash(token),
		ReportTokenHash:      tokenHash("report-token"),
		UploadTokenExpiresAt: time.Now().UTC().Add(15 * time.Minute),
	}
	if err := store.CreateUploadSession(job); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/paid-uploads/job-paid-token?limit=100", bytes.NewReader(testPaidBundle(t)))
	req.SetPathValue("id", "job-paid-token")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("X-Scan-Limit", "100")
	rec := httptest.NewRecorder()
	paidBundleUploadHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected paid upload status 201, got %d: %s", rec.Code, rec.Body.String())
	}
	finalizeReq := httptest.NewRequest(http.MethodPost, "/api/paid-uploads/job-paid-token/finalize", nil)
	finalizeReq.SetPathValue("id", "job-paid-token")
	finalizeReq.Header.Set("Authorization", "Bearer "+token)
	finalizeRec := httptest.NewRecorder()
	finalizeTokenUploadHandler(store).ServeHTTP(finalizeRec, finalizeReq)

	if finalizeRec.Code != http.StatusAccepted {
		t.Fatalf("expected finalize status 202, got %d: %s", finalizeRec.Code, finalizeRec.Body.String())
	}
	stored, err := store.GetJob("job-paid-token")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != app.StatusPending || stored.ScanType != app.ScanTypePaidBundle {
		t.Fatalf("expected pending paid bundle job, got %#v", stored)
	}
}

func TestCreatePaidSessionRequiresLocalEnablementAndWaiver(t *testing.T) {
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	disabledReq := httptest.NewRequest(http.MethodPost, "/api/paid-sessions", strings.NewReader(`{"waiver_accepted":true,"acknowledgment":"at my own risk"}`))
	disabledRec := httptest.NewRecorder()
	createPaidSessionHandler(store, 15*time.Minute).ServeHTTP(disabledRec, disabledReq)
	if disabledRec.Code != http.StatusPaymentRequired {
		t.Fatalf("expected disabled paid session status 402, got %d: %s", disabledRec.Code, disabledRec.Body.String())
	}

	t.Setenv("CLAUDE_ANALYZER_ENABLE_LOCAL_PAID_SESSIONS", "true")
	missingWaiverReq := httptest.NewRequest(http.MethodPost, "/api/paid-sessions", strings.NewReader(`{"waiver_accepted":false}`))
	missingWaiverRec := httptest.NewRecorder()
	createPaidSessionHandler(store, 15*time.Minute).ServeHTTP(missingWaiverRec, missingWaiverReq)
	if missingWaiverRec.Code != http.StatusBadRequest {
		t.Fatalf("expected missing waiver status 400, got %d: %s", missingWaiverRec.Code, missingWaiverRec.Body.String())
	}
}

func TestCreatePaidSessionReturnsLocalFirstPaidCommandByDefault(t *testing.T) {
	t.Setenv("CLAUDE_ANALYZER_ENABLE_LOCAL_PAID_SESSIONS", "true")
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/paid-sessions", strings.NewReader(`{"waiver_accepted":true,"acknowledgment":"I accept at my own risk"}`))
	req.Host = "example.test"
	rec := httptest.NewRecorder()

	createPaidSessionHandler(store, 15*time.Minute).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected paid session status 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var session analysisSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("response is not valid session JSON: %v", err)
	}
	for _, forbidden := range []string{"/api/paid-uploads/", "tar -C", "application/gzip", "raw bundle", "bundles my 100"} {
		if strings.Contains(session.Command, forbidden) || strings.Contains(session.Prompt, forbidden) || strings.Contains(session.UploadPath, forbidden) {
			t.Fatalf("public paid session exposed raw upload instruction %q: %#v", forbidden, session)
		}
	}
	for _, want := range []string{"npx --yes agent-analyzer@latest analyze --paid --limit 5", "/api/paid-client-reports", "sanitized aggregate", "does not upload raw logs"} {
		if !strings.Contains(session.Command, want) && !strings.Contains(session.Prompt, want) {
			t.Fatalf("paid local-first session missing %q: %#v", want, session)
		}
	}
	if session.Token != "" || session.JobID != "" || session.FinalizePath != "" {
		t.Fatalf("public local-first paid session should not mint a raw upload job/token: %#v", session)
	}
}

func TestCreatePaidSessionReturnsLegacyPaidBundleOnlyWhenExplicit(t *testing.T) {
	t.Setenv("CLAUDE_ANALYZER_ENABLE_LOCAL_PAID_SESSIONS", "true")
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/paid-sessions?legacy_raw_bundle=1", strings.NewReader(`{"waiver_accepted":true,"acknowledgment":"I accept at my own risk"}`))
	req.Host = "example.test"
	rec := httptest.NewRecorder()

	createPaidSessionHandler(store, 15*time.Minute).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected paid session status 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var session analysisSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("response is not valid session JSON: %v", err)
	}
	if !strings.Contains(session.UploadPath, "/api/paid-uploads/") || !strings.Contains(session.Command, "limit=100") || !strings.Contains(session.Command, "X-Scan-Limit: 100") {
		t.Fatalf("expected explicit legacy paid bundle command, got %#v", session)
	}
	job, err := store.GetJob(session.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.ScanType != app.ScanTypePaidBundle || job.WaiverAcceptedAt.IsZero() {
		t.Fatalf("expected waiver-gated paid bundle job, got %#v", job)
	}
}

func TestPaidBundleUploadRejectsFreeToken(t *testing.T) {
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token := "free-token"
	job := app.Job{
		ID:                   "job-free-token",
		Status:               app.StatusUploading,
		ScanType:             app.ScanTypeSingle,
		MaxUploadBytes:       maxUploadBytes,
		UploadTokenHash:      tokenHash(token),
		ReportTokenHash:      tokenHash("report-token"),
		UploadTokenExpiresAt: time.Now().UTC().Add(15 * time.Minute),
	}
	if err := store.CreateUploadSession(job); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/paid-uploads/job-free-token?limit=100", bytes.NewReader(testPaidBundle(t)))
	req.SetPathValue("id", "job-free-token")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("X-Scan-Limit", "100")
	rec := httptest.NewRecorder()
	paidBundleUploadHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected free token rejection 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPaidBundleUploadRequiresScanLimitContract(t *testing.T) {
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token := "paid-token"
	job := app.Job{
		ID:                   "job-paid-limit",
		Status:               app.StatusUploading,
		ScanType:             app.ScanTypePaidBundle,
		MaxUploadBytes:       maxPaidUploadBytes,
		UploadTokenHash:      tokenHash(token),
		ReportTokenHash:      tokenHash("report-token"),
		UploadTokenExpiresAt: time.Now().UTC().Add(15 * time.Minute),
	}
	if err := store.CreateUploadSession(job); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/paid-uploads/job-paid-limit", bytes.NewReader(testPaidBundle(t)))
	req.SetPathValue("id", "job-paid-limit")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/gzip")
	rec := httptest.NewRecorder()
	paidBundleUploadHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected missing limit rejection 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTokenUploadRejectsExpiredToken(t *testing.T) {
	store, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token := "expired-token"
	job := app.Job{
		ID:                   "job-expired-token",
		Status:               app.StatusUploading,
		MaxUploadBytes:       maxUploadBytes,
		UploadTokenHash:      tokenHash(token),
		ReportTokenHash:      tokenHash("report-token"),
		UploadTokenExpiresAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := store.CreateUploadSession(job); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/uploads/job-expired-token", strings.NewReader("log line"))
	req.SetPathValue("id", "job-expired-token")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	tokenUploadHandler(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusGone {
		t.Fatalf("expected expired token status 410, got %d: %s", rec.Code, rec.Body.String())
	}
}

func testPaidBundle(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gzipWriter)
	data := []byte(`{"type":"tool","command":"cat src/auth.ts","output":"ok"}` + "\n")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "logs/session.jsonl", Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func paidAggregateReport() analyzer.Report {
	return analyzer.Report{
		JobID:   "local-paid",
		Version: analyzer.Version,
		Score:   68,
		Metrics: analyzer.Metrics{Turns: 12, SessionCount: 2, EstimatedTokens: 2400},
		SecurityReceipt: analyzer.SecurityReceipt{
			RawTranscriptSentToLLM: false,
			OutboundDuringAnalysis: false,
			SecretsRedacted:        1,
			RawLogTTL:              "not uploaded",
		},
		AggregateEvent: analyzer.AggregateSafeEvent{
			Event:      "analysis_completed",
			ParserType: "paid_bundle",
			Findings:   map[string]string{"repeated_file_reads": "high"},
		},
		Findings: []analyzer.Finding{
			{ID: "repeated_file_reads", Severity: "high", CostImpact: "medium-high", Evidence: analyzer.FindingEvidence{Count: 6}},
		},
	}
}
