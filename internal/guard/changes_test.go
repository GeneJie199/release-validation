package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionedExpectedChangesCoverInfraAndDatabase(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "expected.json"), map[string]any{
		"spec": expectedChangesSpec, "kind": "expected-changes", "release_id": "rel-42", "version": "2.4.0", "generated_at": time.Now().UTC().Format(time.RFC3339),
		"changes": []map[string]any{
			{"id": "service-image", "source": "infrascout", "action": "changed", "resource_id": "service:checkout", "resource_type": "service", "fields": []string{"image"}, "summary": "deploy reviewed image", "evidence_ids": []string{"build-attestation"}},
			{"id": "orders-column", "source": "database", "action": "added", "resource_id": "dbmeta:column:public.orders.total", "resource_type": "database.column", "summary": "add reviewed order total", "verification_checks": []string{"migration artifact reviewed"}},
		},
	})
	writeTestJSON(t, filepath.Join(dir, "drift.json"), map[string]any{
		"added":   []map[string]any{{"id": "dbmeta:column:public.orders.total", "type": "database.column", "summary": "column added", "severity": "HIGH", "fingerprint": "drift_db"}},
		"removed": []any{},
		"changed": []map[string]any{{"id": "service:checkout", "type": "service", "summary": "service changed", "severity": "MEDIUM", "before": map[string]any{"image": "v1", "pid": 1}, "after": map[string]any{"image": "v2", "pid": 2}, "fingerprint": "drift_service"}},
	})
	planPath := filepath.Join(dir, "plan.json")
	writeTestJSON(t, planPath, map[string]any{
		"release_id": "rel-42", "version": "2.4.0", "expected_changes_file": "expected.json", "drift_file": "drift.json",
		"recovery_checks": []map[string]any{{"name": "expected declaration retained", "type": "file", "path": "expected.json"}},
		"checks":          []map[string]any{{"name": "migration artifact reviewed", "type": "file", "path": "drift.json"}},
		"rollback":        []string{"restore the previous reviewed release"},
	})
	plan, raw, err := LoadPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), plan, raw)
	if report.Decision != "GO" || report.Manifest.Changes == nil {
		t.Fatalf("decision=%s reason=%s changes=%+v", report.Decision, report.DecisionReason, report.Manifest.Changes)
	}
	coverage := report.Manifest.Changes
	if coverage.MatchedTotal != 2 || coverage.MissingRequired != 0 || coverage.UnexpectedTotal != 0 || len(coverage.DocumentSHA256) != 64 {
		t.Fatalf("coverage=%+v", coverage)
	}
	if len(report.Guidance) != 1 || report.Guidance[0].Code != "approval.review" {
		t.Fatalf("guidance=%+v", report.Guidance)
	}
	if got := coverage.Observed[1].Fields; strings.Join(got, ",") != "image,pid" {
		t.Fatalf("changed fields=%v", got)
	}
	if got := coverage.Correlations[0].EvidenceIDs; len(got) != 1 || got[0] != "build-attestation" {
		t.Fatalf("correlation evidence ids=%v", got)
	}
}

func TestAmbiguousExpectedChangeIsNotAlsoReportedAsUnexpected(t *testing.T) {
	coverage := &ChangeCoverage{
		Declared: []ExpectedChange{{ID: "service", Source: "fleet", Action: "changed", ResourceID: "service:api", Summary: "roll out service"}},
		Observed: []ObservedChange{
			{ID: "first", Source: "fleet", Action: "changed", ResourceID: "service:api"},
			{ID: "second", Source: "fleet", Action: "changed", ResourceID: "service:api"},
		},
	}
	correlateChanges(coverage)
	if coverage.MissingRequired != 1 || coverage.UnexpectedTotal != 0 || coverage.Correlations[0].Status != "ambiguous" {
		t.Fatalf("coverage=%+v", coverage)
	}
}

func TestPublishedPlanExamplesRemainValid(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, _, err := LoadPlan(filepath.Join(root, "release-plan.example.json")); err != nil {
		t.Fatalf("release-plan.example.json: %v", err)
	}
	plan, _, err := LoadPlan(filepath.Join(root, "examples", "full-release-plan.json"))
	if err != nil {
		t.Fatalf("examples/full-release-plan.json: %v", err)
	}
	coverage, result := loadExpectedChanges(plan.ExpectedChangesFile, plan.ReleaseID, plan.Version)
	if result.Status != "pass" || coverage == nil {
		t.Fatalf("expected changes example: %+v", result)
	}
	if links := validateExpectedChangeLinks(plan, coverage, nil); links.Status != "pass" {
		t.Fatalf("expected changes links: %+v", links)
	}
}

func TestExpectedChangeLinksRejectMissingMetricCheckAndNode(t *testing.T) {
	coverage := &ChangeCoverage{Declared: []ExpectedChange{{ID: "edge", Source: "topology", Action: "added", ResourceID: "edge:a-b", Summary: "new edge", VerificationChecks: []string{"smoke"}, MetricPolicies: []string{"latency"}, AffectedNodes: []string{"node-b"}}}}
	result := validateExpectedChangeLinks(Plan{Checks: []Check{{Name: "other"}}, Metrics: []MetricPolicy{{Name: "errors"}}, Fleet: &FleetPolicy{Nodes: []string{"node-a"}}}, coverage, nil)
	if result.Status != "fail" || !strings.Contains(result.Summary, "3 expected change verification links") {
		t.Fatalf("result=%+v", result)
	}
}

func TestExpectedChangeCoverageBlocksMissingDeniedAndUnexpected(t *testing.T) {
	required := true
	coverage := &ChangeCoverage{
		Declared: []ExpectedChange{
			{ID: "required", Source: "topology", Action: "added", ResourceID: "topology.edge:api-db", Summary: "new dependency", Required: &required},
			{ID: "denied", Source: "fleet", Action: "changed", ResourceID: "deployment:api", Summary: "new version"},
		},
		Observed: []ObservedChange{
			{ID: "fleet-denied", Source: "fleet", Action: "changed", ResourceID: "deployment:api", Classification: "denied"},
			{ID: "unexpected", Source: "topology", Action: "removed", ResourceID: "topology.edge:api-cache"},
		},
	}
	correlateChanges(coverage)
	if coverage.MissingRequired != 2 || coverage.UnexpectedTotal != 2 || coverage.MatchedTotal != 0 {
		t.Fatalf("coverage=%+v", coverage)
	}
}

func TestOptionalExpectedChangeDoesNotBlockCoverage(t *testing.T) {
	optional := false
	coverage := &ChangeCoverage{Declared: []ExpectedChange{{ID: "optional", Source: "database", Action: "added", ResourceID: "dbmeta:index:optional", Summary: "optional index", Required: &optional}}}
	correlateChanges(coverage)
	if coverage.MissingOptional != 1 || coverage.MissingRequired != 0 || coverage.UnexpectedTotal != 0 {
		t.Fatalf("coverage=%+v", coverage)
	}
}

func TestFleetExpectedChangesAreReleaseAndNodeScoped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/changes" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fleet-secret" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprint(w, `[
          {"id":"wanted","node_id":"node-a","resource_id":"edge:api-db","resource_type":"topology.edge","kind":"added","classification":"expected","release_id":"rel-7","summary":"dependency added"},
          {"id":"other-release","node_id":"node-a","resource_id":"edge:old","resource_type":"topology.edge","kind":"removed","classification":"unexpected","release_id":"rel-6"},
          {"id":"other-node","node_id":"node-b","resource_id":"edge:other","resource_type":"topology.edge","kind":"added","classification":"unexpected","release_id":"rel-7"}
        ]`)
	}))
	defer server.Close()
	t.Setenv("CHANGE_FLEET_TOKEN", "fleet-secret")
	report := Report{Manifest: Manifest{Changes: &ChangeCoverage{Declared: []ExpectedChange{{ID: "dependency", Source: "topology", Action: "added", ResourceID: "edge:api-db", ResourceType: "topology.edge", NodeID: "node-a", Summary: "dependency added"}}}}}
	plan := Plan{ReleaseID: "rel-7", Fleet: &FleetPolicy{CenterURL: server.URL, TokenEnv: "CHANGE_FLEET_TOKEN", Nodes: []string{"node-a"}}}
	result := finalizeChangeCoverage(context.Background(), plan, &report)
	if result.Status != "pass" || report.Manifest.Changes.MatchedTotal != 1 || len(report.Manifest.Changes.Observed) != 1 {
		t.Fatalf("result=%+v coverage=%+v", result, report.Manifest.Changes)
	}
}

func TestExpectedChangesRejectUnknownFieldsAndIdentityMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "expected.json")
	if err := os.WriteFile(path, []byte(`{"spec":"lifecycle-spec/expected-changes/v1","kind":"expected-changes","release_id":"other","version":"1","generated_at":"2026-08-23T00:00:00Z","changes":[{"id":"x","source":"fleet","action":"added","resource_id":"x","summary":"x","unknown":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	coverage, result := loadExpectedChanges(path, "rel", "1")
	if coverage != nil || result.Status != "fail" || !strings.Contains(result.Summary, "unknown") {
		t.Fatalf("coverage=%+v result=%+v", coverage, result)
	}
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
