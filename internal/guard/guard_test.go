package guard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunChecksAndDecision(t *testing.T) {
	d := t.TempDir()
	file := filepath.Join(d, "artifact.txt")
	if err := os.WriteFile(file, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	required := true
	p := Plan{ReleaseID: "rel-1", Version: "1.2.3", Rollback: []string{"restore"}, RecoveryChecks: []Check{{Name: "rollback artifact", Type: "file", Path: file}}, Checks: []Check{{Name: "artifact", Type: "file", Path: file, Required: &required}, {Name: "health", Type: "http", URL: srv.URL, WantStatus: 204}}}
	b, _ := json.Marshal(p)
	r := Run(context.Background(), p, b)
	if r.Decision != "GO" {
		t.Fatalf("decision=%s results=%+v", r.Decision, r.Results)
	}
}

func TestCandidateGitManifestAndImmutableApproval(t *testing.T) {
	d := t.TempDir()
	repo := filepath.Join(d, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, b)
		}
	}
	run("init")
	run("config", "user.name", "Test")
	run("config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "base")
	baseBytes, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	base := string(bytes.TrimSpace(baseBytes))
	if err := os.MkdirAll(filepath.Join(repo, "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "migrations", "001.sql"), []byte("select 1;"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "migration")
	targetBytes, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	target := string(bytes.TrimSpace(targetBytes))
	candidate := filepath.Join(d, "candidate.json")
	candidateJSON := fmt.Sprintf(`{"spec":"lifecycle-spec/release-candidate/v1","requirement":{"id":"req-1","title":"Ship"},"sources":[{"taskId":"task-1","repositoryPath":%q,"branch":"main","headCommit":%q,"clean":true}],"readiness":{"criteriaTotal":1,"criteriaSatisfied":1,"criteriaWithEvidence":1,"tasksTotal":1,"tasksDone":1,"sourcesTotal":1,"sourcesClean":1,"ready":true}}`, repo, target)
	if err := os.WriteFile(candidate, []byte(candidateJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	p := Plan{ReleaseID: "rel", Version: "1", CandidateFile: candidate, Repository: repo, BaseRef: base, TargetRef: "HEAD", ExpectedFiles: []string{"migrations/001.sql"}, RecoveryChecks: []Check{{Name: "previous source available", Type: "file", Path: filepath.Join(repo, "README.md")}}, Rollback: []string{"undo"}}
	b, _ := json.Marshal(p)
	r := Run(context.Background(), p, b)
	if r.Decision != "GO" || r.Manifest.Git == nil || len(r.Manifest.Git.MigrationFiles) != 1 {
		t.Fatalf("report=%+v", r)
	}
	out := filepath.Join(d, "report.json")
	if err := WriteReport(out, r); err != nil {
		t.Fatal(err)
	}
	if err := WriteReport(out, r); err == nil {
		t.Fatal("expected immutable overwrite rejection")
	}
	approval := filepath.Join(d, "approval.json")
	if err := CreateApproval(out, approval, "GO", "operator", "reviewed"); err != nil {
		t.Fatal(err)
	}
	var a Approval
	raw, _ := os.ReadFile(approval)
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	reportBytes, _ := os.ReadFile(out)
	sum := sha256.Sum256(reportBytes)
	if a.ReportSHA256 != fmt.Sprintf("%x", sum) {
		t.Fatal("approval digest does not bind report")
	}
}

func TestApprovalOutputPathKeepsApprovalsBoundAcrossReportReplacement(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	write := func(releaseID, planSHA string) {
		t.Helper()
		report := Report{SchemaVersion: "releaseguard.report/v1", ReleaseID: releaseID, Decision: "GO", PlanSHA256: planSHA}
		raw, _ := json.Marshal(report)
		if err := os.WriteFile(reportPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("release-a", strings.Repeat("a", 64))
	firstPath, err := ApprovalOutputPath(reportPath)
	if err != nil || firstPath != reportPath+".approval.json" {
		t.Fatalf("first approval path = %q, err = %v", firstPath, err)
	}
	if err = CreateApproval(reportPath, firstPath, "GO", "qa", "first"); err != nil {
		t.Fatal(err)
	}
	write("release-b", strings.Repeat("b", 64))
	secondPath, err := ApprovalOutputPath(reportPath)
	if err != nil || secondPath == firstPath {
		t.Fatalf("replacement approval path = %q, err = %v", secondPath, err)
	}
	if err = CreateApproval(reportPath, secondPath, "GO", "qa", "second"); err != nil {
		t.Fatal(err)
	}
	approval, _, err := LoadBoundApproval(reportPath)
	if err != nil || approval.ReleaseID != "release-b" || approval.Note != "second" {
		t.Fatalf("bound approval = %+v, err = %v", approval, err)
	}
	if _, err = os.Stat(firstPath); err != nil {
		t.Fatalf("first approval was not retained: %v", err)
	}
}

func TestEnvironmentAndComposeConfigurationChecks(t *testing.T) {
	dir := t.TempDir()
	write := func(name, value string) string {
		file := filepath.Join(dir, name)
		if err := os.WriteFile(file, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return file
	}
	envBefore := write("before.env", "APP_ENV=production\nLOG_LEVEL=info\nSECRET=old\n")
	envAfter := write("after.env", "APP_ENV=production\nLOG_LEVEL=debug\nSECRET=new\n")
	result := runCheck(context.Background(), Check{Name: "env", Type: "env", BeforePath: envBefore, AfterPath: envAfter, RequiredKeys: []string{"APP_ENV", "SECRET"}, AllowedChanges: []string{"LOG_LEVEL", "SECRET"}})
	if result.Status != "pass" {
		t.Fatalf("env result=%+v", result)
	}
	if evidence, _ := json.Marshal(result.Evidence); bytes.Contains(evidence, []byte(`"old"`)) || bytes.Contains(evidence, []byte(`"new"`)) {
		t.Fatalf("environment values leaked into evidence: %s", evidence)
	}
	composeBefore := write("before.yml", "services:\n  api:\n    image: app:1\n    ports: [\"8080:8080\"]\n")
	composeAfter := write("after.yml", "services:\n  api:\n    image: app:2\n    ports: [\"8080:8080\"]\n")
	result = runCheck(context.Background(), Check{Name: "compose", Type: "compose", BeforePath: composeBefore, AfterPath: composeAfter, AllowedChanges: []string{"services.api.image"}})
	if result.Status != "pass" {
		t.Fatalf("compose result=%+v", result)
	}
	result = runCheck(context.Background(), Check{Name: "compose", Type: "compose", BeforePath: composeBefore, AfterPath: composeAfter})
	if result.Status != "fail" {
		t.Fatalf("undeclared compose change must fail: %+v", result)
	}
}
func TestUnexpectedDriftIsNoGo(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "drift.json")
	if err := os.WriteFile(path, []byte(`{"added":[{"resourceId":"container:new"}],"removed":[],"changed":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := Plan{ReleaseID: "rel-2", Version: "2", Rollback: []string{"undo"}, RecoveryChecks: []Check{{Name: "drift snapshot retained", Type: "file", Path: path}}, DriftFile: path}
	b, _ := json.Marshal(p)
	r := Run(context.Background(), p, b)
	if r.Decision != "NO-GO" {
		t.Fatalf("decision=%s", r.Decision)
	}
}
func TestLoadPlanRejectsUnknownFields(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plan.json")
	_ = os.WriteFile(p, []byte(`{"release_id":"x","version":"1","rollback":["x"],"typo":1}`), 0o600)
	if _, _, err := LoadPlan(p); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestLoadPlanRejectsTrailingJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(p, []byte(`{"release_id":"x","version":"1","rollback":["undo"]} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPlan(p); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
}

func TestLoadPlanRequiresRecoveryProofAndVerificationSurface(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(`{"release_id":"x","version":"1","checks":[{"name":"health","type":"http","url":"https://example.test"}],"rollback":["undo"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPlan(path); err == nil || !strings.Contains(err.Error(), "recovery_check") {
		t.Fatalf("error = %v", err)
	}
}

func TestExpectedFilePatterns(t *testing.T) {
	for _, value := range []string{"migrations/001.sql", "migrations/archive/001.sql", "deploy/api.service"} {
		if !matchesAnyPath([]string{"migrations/**", "deploy/*.service"}, value) {
			t.Fatalf("expected %s to match", value)
		}
	}
	if matchesAnyPath([]string{"migrations/**"}, "internal/api.go") {
		t.Fatal("unexpected path match")
	}
}

func TestSensitiveReleaseFileClassificationIsSegmentAware(t *testing.T) {
	if migration, configuration := classifySensitiveReleaseFile("internal/configurator/parser.go"); migration || configuration {
		t.Fatalf("incidental substring classified as sensitive: migration=%v configuration=%v", migration, configuration)
	}
	for _, file := range []string{"migrations/001.sql", "deploy/api.service", ".github/workflows/release.yml", "config/production.yaml"} {
		migration, configuration := classifySensitiveReleaseFile(file)
		if !migration && !configuration {
			t.Fatalf("%s was not classified as sensitive", file)
		}
	}
}

func TestFleetReadsSnakeCaseFreshnessAndHealth(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	t.Setenv("TEST_FLEET_TOKEN", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/api/v1/alerts" {
			_, _ = w.Write([]byte(`[{"node_id":"unrelated-node","severity":"critical","status":"open"}]`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"summary":{"health":"healthy","observed_at":%q},"report":{"labels":{"version":"1.2.3"}}}`, now)
	}))
	defer server.Close()
	evidence, result := checkFleet(context.Background(), FleetPolicy{CenterURL: server.URL, TokenEnv: "TEST_FLEET_TOKEN", Nodes: []string{"node-1"}, VersionLabel: "version", MaxAgeSeconds: 60}, "1.2.3")
	if result.Status != "pass" || len(evidence.Nodes) != 1 || !evidence.Nodes[0].Match || evidence.CriticalAlerts != 0 {
		t.Fatalf("result=%+v evidence=%+v", result, evidence)
	}
}

func TestSQLCheckRejectsMultipleStatementsBeforeOpeningDatabase(t *testing.T) {
	result := Result{Evidence: map[string]any{}}
	err := sqlCheck(context.Background(), Check{Type: "sql", Driver: "postgres", DSNEnv: "MISSING_DSN", Query: "SELECT 1; DROP TABLE releases"}, &result)
	if err == nil || !strings.Contains(err.Error(), "exactly one statement") {
		t.Fatalf("error=%v", err)
	}
}

func TestGitRefCheckResolvesWithoutShell(t *testing.T) {
	repository := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("# release candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"config", "user.name", "releaseguard-test"}, {"config", "user.email", "releaseguard-test@example.invalid"}, {"add", "README.md"}, {"commit", "-qm", "initial release candidate"}} {
		command = exec.Command("git", args...)
		command.Dir = repository
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	result := Result{Evidence: map[string]any{}}
	if err := gitRefCheck(context.Background(), Check{Type: "git-ref", Ref: "HEAD", WorkingDir: repository}, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence["commit"].(string)) < 40 {
		t.Fatalf("evidence=%v", result.Evidence)
	}
}

func TestFleetReportsMalformedNodeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/alerts" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{"summary":`))
	}))
	defer server.Close()
	evidence, result := checkFleet(context.Background(), FleetPolicy{CenterURL: server.URL, Nodes: []string{"node-1"}}, "1.2.3")
	if result.Status != "fail" || len(evidence.Nodes) != 1 || evidence.Nodes[0].Health != "invalid_response" {
		t.Fatalf("result=%+v evidence=%+v", result, evidence)
	}
}

func TestApprovalCannotRelaxAutomatedDecision(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	report := Report{SchemaVersion: "releaseguard.report/v1", ReleaseID: "rel-blocked", Decision: "NO-GO", PlanSHA256: strings.Repeat("a", 64)}
	if err := WriteReport(reportPath, report); err != nil {
		t.Fatal(err)
	}
	if err := CreateApproval(reportPath, filepath.Join(dir, "go.json"), "GO", "operator", "override"); err == nil {
		t.Fatal("GO must not override automated NO-GO")
	}
	if err := CreateApproval(reportPath, filepath.Join(dir, "no-go.json"), "NO-GO", "operator", "confirmed"); err != nil {
		t.Fatal(err)
	}
}

func TestImmutableArtifactConcurrentWritersHaveSingleWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.json")
	results := make(chan error, 2)
	go func() { results <- writeJSONAtomic(path, map[string]int{"writer": 1}, false) }()
	go func() { results <- writeJSONAtomic(path, map[string]int{"writer": 2}, false) }()
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("writer errors = %v, %v", first, second)
	}
}
