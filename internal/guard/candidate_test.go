package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validCandidateJSON(repo, target string) string {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	document := fmt.Sprintf(`{"spec":"lifecycle-spec/release-candidate/v1","kind":"release-candidate","generatedAt":%q,"requirement":{"id":"req-1","title":"Ship"},"acceptanceCriteria":[{"id":"criterion-1","requirementId":"req-1","satisfied":true}],"tasks":[{"id":"task-1","requirementId":"req-1","status":"done","branch":"main","worktreePath":%q}],"evidence":[{"id":"proof-1","requirementId":"req-1","criterionId":"criterion-1","taskId":"task-1","kind":"test","status":"passed","createdAt":%q,"metadata":{"command":"go test ./..."}}],"sources":[{"taskId":"task-1","repositoryPath":%q,"worktreePath":%q,"branch":"main","headCommit":%q,"clean":true}],"readiness":{"criteriaTotal":1,"criteriaSatisfied":1,"criteriaWithEvidence":1,"tasksTotal":1,"tasksDone":1,"sourcesTotal":1,"sourcesClean":1,"ready":true}}`, now, repo, now, repo, repo, target)
	var raw map[string]any
	_ = json.Unmarshal([]byte(document), &raw)
	metadata := raw["evidence"].([]any)[0].(map[string]any)["metadata"].(map[string]any)
	for key, value := range map[string]string{"verificationRunId": "run-1", "sourceRepository": repo, "sourceWorktree": repo, "sourceCommit": target, "sourceRepositoryAfter": repo, "sourceWorktreeAfter": repo, "sourceCommitAfter": target, "sourceClean": "true", "sourceCleanAfter": "true", "sourceVerified": "true"} {
		metadata[key] = value
	}
	data, _ := json.Marshal(raw)
	return string(data)
}

func TestCandidateDetailsRejectForgedReadiness(t *testing.T) {
	item := func(document map[string]any, key string) map[string]any {
		return document[key].([]any)[0].(map[string]any)
	}
	for _, test := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"positive summary with missing details", func(c map[string]any) { delete(c, "acceptanceCriteria"); delete(c, "tasks"); delete(c, "evidence") }},
		{"missing evidence", func(c map[string]any) { delete(c, "evidence") }},
		{"machine proof missing source binding", func(c map[string]any) { delete(item(c, "evidence")["metadata"].(map[string]any), "sourceVerified") }},
		{"machine proof missing run identity", func(c map[string]any) { delete(item(c, "evidence")["metadata"].(map[string]any), "verificationRunId") }},
		{"machine proof source was dirty", func(c map[string]any) { item(c, "evidence")["metadata"].(map[string]any)["sourceClean"] = "false" }},
		{"machine proof source became dirty", func(c map[string]any) { item(c, "evidence")["metadata"].(map[string]any)["sourceCleanAfter"] = "false" }},
		{"machine proof source changed during execution", func(c map[string]any) {
			item(c, "evidence")["metadata"].(map[string]any)["sourceCommitAfter"] = strings.Repeat("b", 40)
		}},
		{"source committed after machine proof", func(c map[string]any) { item(c, "sources")["headCommit"] = strings.Repeat("b", 40) }},
		{"machine evidence current false", func(c map[string]any) { c["machineEvidenceCurrent"] = false }},
		{"machine evidence required without current signal", func(c map[string]any) { c["machineEvidenceRequired"] = true }},
		{"duplicate evidence identity", func(c map[string]any) { c["evidence"] = append(c["evidence"].([]any), item(c, "evidence")) }},
		{"criterion count mismatch", func(c map[string]any) { c["acceptanceCriteria"] = []any{} }},
		{"unsatisfied criterion", func(c map[string]any) { item(c, "acceptanceCriteria")["satisfied"] = false }},
		{"criterion wrong requirement", func(c map[string]any) { item(c, "acceptanceCriteria")["requirementId"] = "other" }},
		{"task not done", func(c map[string]any) { item(c, "tasks")["status"] = "in_progress" }},
		{"task wrong requirement", func(c map[string]any) { item(c, "tasks")["requirementId"] = "other" }},
		{"source wrong task", func(c map[string]any) { item(c, "sources")["taskId"] = "other" }},
		{"source wrong branch", func(c map[string]any) { item(c, "sources")["branch"] = "other" }},
		{"source wrong worktree", func(c map[string]any) { item(c, "sources")["worktreePath"] = "/other/source" }},
		{"evidence wrong requirement", func(c map[string]any) { item(c, "evidence")["requirementId"] = "other" }},
		{"evidence wrong criterion", func(c map[string]any) { item(c, "evidence")["criterionId"] = "other" }},
		{"evidence wrong task", func(c map[string]any) { item(c, "evidence")["taskId"] = "other" }},
		{"failed evidence", func(c map[string]any) { item(c, "evidence")["status"] = "failed" }},
		{"passed machine with nonzero exit", func(c map[string]any) { item(c, "evidence")["metadata"].(map[string]any)["exitCode"] = "1" }},
		{"passed machine with failed run", func(c map[string]any) { item(c, "evidence")["metadata"].(map[string]any)["runStatus"] = "failed" }},
		{"natural language assertion without command", func(c map[string]any) {
			item(c, "evidence")["metadata"] = map[string]any{"missionId": "mission", "spec": "devcycle.ai-team/v2alpha1"}
		}},
		{"machine check not linked to source", func(c map[string]any) { delete(item(c, "evidence"), "taskId") }},
		{"machine check status superseded", func(c map[string]any) {
			item(c, "evidence")["createdAt"] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
			c["evidence"] = append(c["evidence"].([]any), map[string]any{"id": "machine-2", "requirementId": "req-1", "taskId": "task-1", "kind": "test", "status": "failed", "createdAt": c["generatedAt"], "metadata": map[string]any{"command": "go test ./..."}})
		}},
		{"missing generation time", func(c map[string]any) { delete(c, "generatedAt") }},
		{"expired candidate", func(c map[string]any) { c["generatedAt"] = time.Now().Add(-25 * time.Hour).Format(time.RFC3339Nano) }},
		{"future candidate", func(c map[string]any) { c["generatedAt"] = time.Now().Add(2 * time.Minute).Format(time.RFC3339Nano) }},
		{"expired evidence", func(c map[string]any) {
			item(c, "evidence")["createdAt"] = time.Now().Add(-25 * time.Hour).Format(time.RFC3339Nano)
		}},
		{"future evidence", func(c map[string]any) {
			item(c, "evidence")["createdAt"] = time.Now().Add(2 * time.Minute).Format(time.RFC3339Nano)
		}},
		{"runtime evidence incomplete", func(c map[string]any) { c["teamRuntime"] = map[string]any{"evidenceComplete": false} }},
		{"runtime evidence status missing", func(c map[string]any) { c["teamRuntime"] = map[string]any{} }},
		{"root evidence incomplete", func(c map[string]any) { c["evidenceComplete"] = false }},
		{"root evidence signal null", func(c map[string]any) { c["evidenceComplete"] = nil }},
		{"runtime evidence signal null", func(c map[string]any) { c["teamRuntime"] = nil }},
		{"newer failed proof wins despite array order", func(c map[string]any) {
			old := item(c, "evidence")
			old["createdAt"] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
			c["evidence"] = append(c["evidence"].([]any), map[string]any{"id": "proof-2", "requirementId": "req-1", "criterionId": "criterion-1", "kind": "test", "status": "failed", "createdAt": c["generatedAt"]})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal([]byte(validCandidateJSON(t.TempDir(), strings.Repeat("a", 40))), &document); err != nil {
				t.Fatal(err)
			}
			test.edit(document)
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "candidate.json")
			if err = os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, result := loadCandidate(path); result.Status != "fail" {
				t.Fatalf("forged readiness accepted: %+v", result)
			}
		})
	}
}

func TestCandidateCompleteDetailsAndRuntimeSignalPass(t *testing.T) {
	for _, signal := range []string{"", `,"evidenceComplete":true`, `,"teamRuntime":{"evidenceComplete":true}`} {
		path := filepath.Join(t.TempDir(), "candidate.json")
		document := validCandidateJSON(t.TempDir(), strings.Repeat("a", 40))
		document = strings.TrimSuffix(document, "}") + signal + "}"
		if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, result := loadCandidate(path); result.Status != "pass" {
			t.Fatalf("valid details rejected: %+v", result)
		}
	}
}

func TestCandidateNormalizedSourcePathsAndMachineWorkingDirectory(t *testing.T) {
	repo := t.TempDir()
	var document candidateDocument
	if err := json.Unmarshal([]byte(validCandidateJSON(repo, strings.Repeat("a", 40))), &document); err != nil {
		t.Fatal(err)
	}
	document.Tasks[0].WorktreePath = strings.ReplaceAll(repo, "\\", "/")
	document.Evidence[0].TaskID = ""
	document.Evidence[0].Metadata["workingDir"] = strings.ReplaceAll(repo, "/", "\\")
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "candidate.json")
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, result := loadCandidate(path); result.Status != "pass" {
		t.Fatalf("equivalent source paths rejected: %+v", result)
	}
}

func TestCandidateTargetMustHaveMatchingMachineCommit(t *testing.T) {
	repo := t.TempDir()
	target := strings.Repeat("b", 40)
	candidate := &CandidateSummary{Sources: []CandidateSourceSummary{{TaskID: "new-lead", RepositoryPath: repo, HeadCommit: target, Clean: true}}, MachineChecks: []CandidateMachineCheck{{EvidenceID: "old-qa", RepositoryPath: repo, HeadCommit: strings.Repeat("a", 40)}}}
	manifest := &GitManifest{Repository: repo, TargetCommit: target}
	if result := checkCandidateGit(candidate, manifest); result.Status != "fail" {
		t.Fatal("old QA snapshot authorized a different release target")
	}
}
