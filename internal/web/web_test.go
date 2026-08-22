package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GeneJie199/release-validation/internal/guard"
	"github.com/GeneJie199/release-validation/internal/runstore"
)

func TestLiveRunReportAndHistory(t *testing.T) {
	store, err := runstore.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan := guard.Plan{ReleaseID: "live-release", Version: "1", Rollback: []string{"undo"}}
	record, err := store.Create(context.Background(), plan.ReleaseID, "sha", plan)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	report := guard.Report{ReleaseID: plan.ReleaseID, Version: plan.Version, Decision: "HOLD", Observation: &guard.ObservationState{Status: "observing", StartedAt: now, DeadlineAt: now.Add(time.Minute), Samples: []guard.FleetEvidence{{CheckedAt: now.Format(time.RFC3339)}}}}
	if err := store.Update(context.Background(), record.ID, "observing", report); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler(filepath.Join(t.TempDir(), "missing-report.json"), "long-enough-test-approval-token", store))
	defer server.Close()
	response, err := http.Get(server.URL + "/api/v1/report")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var live guard.Report
	if err := json.NewDecoder(response.Body).Decode(&live); err != nil || live.Observation == nil || len(live.Observation.Samples) != 1 {
		t.Fatalf("live=%+v err=%v", live, err)
	}
	response, err = http.Get(server.URL + "/api/v1/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	var capabilities map[string]bool
	if err := json.NewDecoder(response.Body).Decode(&capabilities); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if capabilities["approval_write"] || !capabilities["approval_blocked_by_active_run"] {
		t.Fatalf("capabilities = %v", capabilities)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/approval", strings.NewReader(`{"decision":"HOLD","approved_by":"qa"}`))
	request.Header.Set("Authorization", "Bearer long-enough-test-approval-token")
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("approval during active run status = %d", response.StatusCode)
	}
	response, err = http.Get(server.URL + "/api/v1/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var runs []map[string]any
	if err := json.NewDecoder(response.Body).Decode(&runs); err != nil || len(runs) != 1 || runs[0]["stage"] != "observing" {
		t.Fatalf("runs=%v err=%v", runs, err)
	}
}

func TestHandlerServesReportApprovalAndSecurityHeaders(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "report.json")
	served := guard.Report{SchemaVersion: "releaseguard.report/v1", ReleaseID: "release-a", Decision: "GO", PlanSHA256: strings.Repeat("a", 64)}
	raw, _ := json.Marshal(served)
	if err := os.WriteFile(report, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := guard.CreateApproval(report, report+".approval.json", "GO", "qa", "reviewed"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(report))
	defer srv.Close()

	for _, path := range []string{"/", "/app.js", "/style.css", "/suite.js", "/vendor/lucide.min.js", "/api/v1/health", "/api/v1/capabilities", "/api/v1/report", "/api/v1/approval"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d", path, res.StatusCode)
		}
		if res.Header.Get("X-Content-Type-Options") != "nosniff" || res.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("GET %s missing security headers", path)
		}
	}

	indexResponse, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	index, err := io.ReadAll(indexResponse.Body)
	_ = indexResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`class="skip-link"`, `id="main-content" tabindex="-1"`, `id="decision" role="status"`, `aria-label="发布流程"`, `id="observation-announcer"`, `aria-label="套件模块"`} {
		if !bytes.Contains(index, []byte(marker)) {
			t.Fatalf("index page missing UI contract %q", marker)
		}
	}

	res, err := http.Get(srv.URL + "/api/v1/report")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	if err = json.NewDecoder(res.Body).Decode(&body); err != nil || body["decision"] != "GO" {
		t.Fatalf("report body = %v, error = %v", body, err)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control = %q", res.Header.Get("Cache-Control"))
	}
}

func TestApprovalRejectsCrossSiteBrowserMutation(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	report := guard.Report{SchemaVersion: "releaseguard.report/v1", ReleaseID: "release-cross-site", Decision: "GO", PlanSHA256: strings.Repeat("a", 64)}
	raw, _ := json.Marshal(report)
	if err := os.WriteFile(reportPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(HandlerWithApprovalToken(reportPath, "long-enough-test-approval-token"))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/approval", strings.NewReader(`{"decision":"GO","approved_by":"qa"}`))
	request.Header.Set("Authorization", "Bearer long-enough-test-approval-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://attacker.invalid")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site approval status=%d", response.StatusCode)
	}
}

func TestHandlerDoesNotServeApprovalBoundToPreviousReport(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	first := guard.Report{SchemaVersion: "releaseguard.report/v1", ReleaseID: "release-a", Decision: "GO", PlanSHA256: strings.Repeat("a", 64)}
	raw, _ := json.Marshal(first)
	if err := os.WriteFile(reportPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := guard.CreateApproval(reportPath, reportPath+".approval.json", "GO", "qa", "first report"); err != nil {
		t.Fatal(err)
	}
	second := guard.Report{SchemaVersion: "releaseguard.report/v1", ReleaseID: "release-b", Decision: "GO", PlanSHA256: strings.Repeat("b", 64)}
	raw, _ = json.Marshal(second)
	if err := os.WriteFile(reportPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(Handler(reportPath))
	defer server.Close()
	response, err := http.Get(server.URL + "/api/v1/approval")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("stale approval status = %d", response.StatusCode)
	}
	if _, err := os.Stat(reportPath + ".approval.json"); err != nil {
		t.Fatalf("stale approval must remain available for audit: %v", err)
	}
}

func TestDifferentPlanActiveRunDoesNotReplaceOrBlockServedReport(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	served := guard.Report{SchemaVersion: "releaseguard.report/v1", ReleaseID: "release-a", Decision: "GO", PlanSHA256: strings.Repeat("a", 64)}
	raw, _ := json.Marshal(served)
	if err := os.WriteFile(reportPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := runstore.Open(filepath.Join(dir, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan := guard.Plan{ReleaseID: "release-a", Version: "2", Rollback: []string{"undo"}}
	record, err := store.Create(context.Background(), plan.ReleaseID, strings.Repeat("b", 64), plan)
	if err != nil {
		t.Fatal(err)
	}
	live := guard.Report{SchemaVersion: "releaseguard.report/v1", ReleaseID: plan.ReleaseID, Decision: "HOLD", Observation: &guard.ObservationState{Status: "observing"}}
	if err := store.Update(context.Background(), record.ID, "observing", live); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler(reportPath, "long-enough-test-approval-token", store))
	defer server.Close()
	response, err := http.Get(server.URL + "/api/v1/report")
	if err != nil {
		t.Fatal(err)
	}
	var got guard.Report
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if got.ReleaseID != "release-a" || got.Version != "" {
		t.Fatalf("served release = %q", got.ReleaseID)
	}
	response, err = http.Get(server.URL + "/api/v1/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	var capabilities map[string]bool
	if err := json.NewDecoder(response.Body).Decode(&capabilities); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !capabilities["approval_write"] || capabilities["approval_blocked_by_active_run"] {
		t.Fatalf("capabilities = %v", capabilities)
	}
}

func TestApprovalWriteRequiresTokenAndIsImmutable(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "report.json")
	reportJSON, _ := json.Marshal(guard.Report{SchemaVersion: "releaseguard.report/v1", ReleaseID: "rel-web", Decision: "GO", PlanSHA256: strings.Repeat("a", 64)})
	if err := os.WriteFile(report, reportJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	readOnly := httptest.NewServer(Handler(report))
	defer readOnly.Close()
	res, err := http.Post(readOnly.URL+"/api/v1/approval", "application/json", strings.NewReader(`{"decision":"GO","approved_by":"qa"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("read-only POST status = %d", res.StatusCode)
	}

	writable := httptest.NewServer(HandlerWithApprovalToken(report, "secret"))
	defer writable.Close()
	post := func(token, body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, writable.URL+"/api/v1/approval", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		return response
	}
	valid := `{"decision":"GO","approved_by":"release-owner","note":"reviewed"}`
	res = post("wrong", valid)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d", res.StatusCode)
	}
	res = post("secret", valid+` {}`)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("trailing JSON status = %d", res.StatusCode)
	}
	res = post("secret", valid)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", res.StatusCode)
	}
	res = post("secret", valid)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("overwrite status = %d", res.StatusCode)
	}
}

func TestHandlerMissingReportAndApproval(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "private", "missing.json")
	srv := httptest.NewServer(Handler(missing))
	defer srv.Close()
	for _, path := range []string{"/api/v1/report", "/api/v1/approval"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d", path, res.StatusCode)
		}
		if strings.Contains(string(body), missing) || strings.Contains(string(body), filepath.Dir(missing)) {
			t.Errorf("GET %s leaked server path: %s", path, body)
		}
	}
}

func TestStaticResourcesRejectWritesAndApprovalAuthIsRateLimited(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "report.json")
	reportJSON, _ := json.Marshal(guard.Report{SchemaVersion: "releaseguard.report/v1", ReleaseID: "rel-rate", Decision: "GO", PlanSHA256: strings.Repeat("a", 64)})
	if err := os.WriteFile(report, reportJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(HandlerWithApprovalToken(report, "correct-token"))
	defer srv.Close()
	response, err := http.Post(srv.URL+"/app.js", "text/plain", strings.NewReader("write"))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST static status = %d", response.StatusCode)
	}
	for attempt := 1; attempt <= 6; attempt++ {
		request, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/approval", strings.NewReader(`{"decision":"GO","approved_by":"qa"}`))
		request.Header.Set("Authorization", "Bearer wrong-token")
		response, err = http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		want := http.StatusUnauthorized
		if attempt == 6 {
			want = http.StatusTooManyRequests
		}
		if response.StatusCode != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.StatusCode, want)
		}
	}
}

func TestServeRejectsRemoteAddress(t *testing.T) {
	if err := Serve(context.Background(), "0.0.0.0:0", "report.json"); err == nil {
		t.Fatal("expected non-loopback address rejection")
	}
	if err := Serve(context.Background(), "not-an-address", "report.json"); err == nil {
		t.Fatal("expected malformed address rejection")
	}
}
