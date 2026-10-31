package guard

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const candidateMaxAge = 24 * time.Hour
const candidateClockSkew = time.Minute

type candidateDocument struct {
	Spec        string    `json:"spec"`
	Kind        string    `json:"kind"`
	GeneratedAt time.Time `json:"generatedAt"`
	Requirement struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"requirement"`
	AcceptanceCriteria []struct {
		ID            string `json:"id"`
		RequirementID string `json:"requirementId"`
		Satisfied     bool   `json:"satisfied"`
	} `json:"acceptanceCriteria"`
	Tasks []struct {
		ID            string   `json:"id"`
		RequirementID string   `json:"requirementId"`
		Status        string   `json:"status"`
		Branch        string   `json:"branch"`
		WorktreePath  string   `json:"worktreePath"`
		DependsOn     []string `json:"dependsOn"`
	} `json:"tasks"`
	Evidence  []candidateEvidence `json:"evidence"`
	Readiness struct {
		CriteriaTotal        int  `json:"criteriaTotal"`
		CriteriaSatisfied    int  `json:"criteriaSatisfied"`
		CriteriaWithEvidence int  `json:"criteriaWithEvidence"`
		TasksTotal           int  `json:"tasksTotal"`
		TasksDone            int  `json:"tasksDone"`
		SourcesTotal         int  `json:"sourcesTotal"`
		SourcesClean         int  `json:"sourcesClean"`
		Ready                bool `json:"ready"`
	} `json:"readiness"`
	Sources []struct {
		TaskID         string `json:"taskId"`
		RepositoryPath string `json:"repositoryPath"`
		WorktreePath   string `json:"worktreePath"`
		Branch         string `json:"branch"`
		HeadCommit     string `json:"headCommit"`
		Clean          bool   `json:"clean"`
	} `json:"sources"`
	EvidenceComplete        json.RawMessage `json:"evidenceComplete,omitempty"`
	TeamRuntime             json.RawMessage `json:"teamRuntime,omitempty"`
	MachineEvidenceRequired bool            `json:"machineEvidenceRequired,omitempty"`
	MachineEvidenceCurrent  *bool           `json:"machineEvidenceCurrent,omitempty"`
}

type candidateEvidence struct {
	ID            string            `json:"id"`
	RequirementID string            `json:"requirementId"`
	CriterionID   string            `json:"criterionId"`
	TaskID        string            `json:"taskId"`
	Kind          string            `json:"kind"`
	Status        string            `json:"status"`
	CreatedAt     time.Time         `json:"createdAt"`
	Metadata      map[string]string `json:"metadata"`
}

func candidateSamePath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	left = filepath.Clean(filepath.FromSlash(strings.ReplaceAll(left, "\\", "/")))
	right = filepath.Clean(filepath.FromSlash(strings.ReplaceAll(right, "\\", "/")))
	return left == right || runtime.GOOS == "windows" && strings.EqualFold(left, right)
}

func candidateMachineKey(e candidateEvidence) string {
	if key, _, err := candidateControllerCheck(e); err == nil && key != "" {
		return key
	}
	directory := e.Metadata["workingDir"]
	if directory != "" {
		directory = filepath.Clean(filepath.FromSlash(strings.ReplaceAll(directory, "\\", "/")))
		if runtime.GOOS == "windows" {
			directory = strings.ToLower(directory)
		}
	}
	return e.Metadata["command"] + "\x00" + directory + "\x00" + e.TaskID
}

// Only the runtime-owned complete Team scope can supersede an earlier worktree.
// Ordinary v1 machine evidence keeps its independent command/directory/task key.
func candidateControllerCheck(e candidateEvidence) (string, int, error) {
	m := e.Metadata
	if m["logicalCheckId"] == "" && m["planTaskId"] == "" && m["verificationAttempt"] == "" {
		return "", 0, nil
	}
	logical, err := hex.DecodeString(m["logicalCheckId"])
	attempt, parseErr := strconv.Atoi(m["verificationAttempt"])
	index, indexErr := strconv.Atoi(m["verificationCheckIndex"])
	parts := []string{m["missionId"], m["planTaskId"], e.TaskID, m["logicalCheckId"], m["command"]}
	if err != nil || len(logical) != 32 || parseErr != nil || attempt <= 0 || indexErr != nil || index < 0 || strings.TrimSpace(m["executionId"]) == "" || strings.TrimSpace(m["verificationRunId"]) == "" {
		return "", 0, fmt.Errorf("candidate controller check scope or attempt is invalid")
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" || strings.ContainsRune(part, '\x00') {
			return "", 0, fmt.Errorf("candidate controller check scope is incomplete")
		}
	}
	return "team\x00" + strings.Join(parts, "\x00"), attempt, nil
}

func candidateMachineNewer(current, previous candidateEvidence) bool {
	_, currentAttempt, _ := candidateControllerCheck(current)
	_, previousAttempt, _ := candidateControllerCheck(previous)
	if currentAttempt != previousAttempt {
		return currentAttempt > previousAttempt
	}
	return current.CreatedAt.After(previous.CreatedAt) || current.CreatedAt.Equal(previous.CreatedAt) && current.Status != "passed"
}

func latestCandidateMachineChecks(evidence []candidateEvidence) map[string]candidateEvidence {
	latest := map[string]candidateEvidence{}
	for _, e := range evidence {
		if (e.Kind != "test" && e.Kind != "command") || strings.TrimSpace(e.Metadata["command"]) == "" {
			continue
		}
		key := candidateMachineKey(e)
		previous, exists := latest[key]
		if !exists || candidateMachineNewer(e, previous) {
			latest[key] = e
		}
	}
	return latest
}

func candidateMachineSourceMatches(e candidateEvidence, c candidateDocument) bool {
	m := e.Metadata
	if m["sourceVerified"] != "true" || m["sourceClean"] != "true" || m["sourceCleanAfter"] != "true" || strings.TrimSpace(m["verificationRunId"]) == "" ||
		!strings.EqualFold(m["sourceCommit"], m["sourceCommitAfter"]) || !candidateSamePath(m["sourceRepository"], m["sourceRepositoryAfter"]) || !candidateSamePath(m["sourceWorktree"], m["sourceWorktreeAfter"]) {
		return false
	}
	for _, source := range c.Sources {
		linked := e.TaskID == source.TaskID || e.TaskID == "" && (candidateSamePath(m["workingDir"], source.WorktreePath) || candidateSamePath(m["workingDir"], source.RepositoryPath))
		if source.Clean && linked && strings.EqualFold(m["sourceCommit"], source.HeadCommit) && candidateSamePath(m["sourceRepository"], source.RepositoryPath) && candidateSamePath(m["sourceWorktree"], source.WorktreePath) {
			return true
		}
	}
	return false
}

// This checks local evidence consistency, not authenticity against a remote signer.
func validateCandidateDetails(c candidateDocument, now time.Time) error {
	if c.Kind != "release-candidate" || c.GeneratedAt.IsZero() || now.Sub(c.GeneratedAt) > candidateMaxAge || c.GeneratedAt.Sub(now) > candidateClockSkew {
		return fmt.Errorf("candidate generatedAt must be within 24 hours and no more than one minute in the future")
	}
	if len(c.EvidenceComplete) > 0 && !bytes.Equal(bytes.TrimSpace(c.EvidenceComplete), []byte("true")) {
		return fmt.Errorf("candidate runtime evidence is incomplete")
	}
	if len(c.TeamRuntime) > 0 {
		var signal struct {
			EvidenceComplete json.RawMessage `json:"evidenceComplete"`
		}
		if json.Unmarshal(c.TeamRuntime, &signal) != nil || !bytes.Equal(bytes.TrimSpace(signal.EvidenceComplete), []byte("true")) {
			return fmt.Errorf("candidate runtime evidence is incomplete")
		}
	}
	if c.MachineEvidenceCurrent != nil && !*c.MachineEvidenceCurrent || c.MachineEvidenceRequired && c.MachineEvidenceCurrent == nil {
		return fmt.Errorf("candidate machine evidence is not current")
	}
	if len(c.AcceptanceCriteria) != c.Readiness.CriteriaTotal || len(c.Tasks) != c.Readiness.TasksTotal {
		return fmt.Errorf("candidate detail lengths disagree with readiness counts")
	}
	criteria, tasks := map[string]bool{}, map[string]bool{}
	for _, item := range c.AcceptanceCriteria {
		if strings.TrimSpace(item.ID) == "" || criteria[item.ID] || item.RequirementID != c.Requirement.ID || !item.Satisfied {
			return fmt.Errorf("candidate criterion identity, requirement or satisfaction is invalid")
		}
		criteria[item.ID] = true
	}
	for _, task := range c.Tasks {
		if strings.TrimSpace(task.ID) == "" || tasks[task.ID] || task.RequirementID != c.Requirement.ID || task.Status != "done" {
			return fmt.Errorf("candidate task identity, requirement or completion is invalid")
		}
		tasks[task.ID] = true
	}
	sourcedTasks := map[string]bool{}
	for _, source := range c.Sources {
		if !tasks[source.TaskID] {
			return fmt.Errorf("candidate source is not linked to a completed task")
		}
		sourcedTasks[source.TaskID] = true
		for _, task := range c.Tasks {
			if task.ID == source.TaskID && (task.Branch != source.Branch || !candidateSamePath(task.WorktreePath, source.WorktreePath)) {
				return fmt.Errorf("candidate task and source branch/worktree do not match")
			}
		}
	}
	for _, task := range c.Tasks {
		if task.WorktreePath != "" && !sourcedTasks[task.ID] {
			return fmt.Errorf("candidate task worktree has no source snapshot")
		}
		for _, dependency := range task.DependsOn {
			if !tasks[dependency] || dependency == task.ID {
				return fmt.Errorf("candidate task dependency is invalid")
			}
		}
	}
	seen, latest := map[string]bool{}, map[string]candidateEvidence{}
	machine := map[string]candidateEvidence{}
	for _, evidence := range c.Evidence {
		if strings.TrimSpace(evidence.ID) == "" || seen[evidence.ID] || evidence.RequirementID != c.Requirement.ID || evidence.CriterionID != "" && !criteria[evidence.CriterionID] || evidence.TaskID != "" && !tasks[evidence.TaskID] {
			return fmt.Errorf("candidate evidence identity or linkage is invalid")
		}
		seen[evidence.ID] = true
		if evidence.CreatedAt.IsZero() {
			return fmt.Errorf("candidate evidence is missing a timestamp")
		}
		if evidence.Status != "passed" && evidence.Status != "failed" && evidence.Status != "informational" {
			return fmt.Errorf("candidate evidence status is invalid")
		}
		if evidence.CriterionID != "" {
			previous, exists := latest[evidence.CriterionID]
			if !exists || evidence.CreatedAt.After(previous.CreatedAt) || evidence.CreatedAt.Equal(previous.CreatedAt) && evidence.Status != "passed" {
				latest[evidence.CriterionID] = evidence
			}
		}
		if (evidence.Kind == "test" || evidence.Kind == "command") && strings.TrimSpace(evidence.Metadata["command"]) != "" {
			if _, _, err := candidateControllerCheck(evidence); err != nil {
				return err
			}

			key := candidateMachineKey(evidence)
			previous, exists := machine[key]
			if !exists || candidateMachineNewer(evidence, previous) {
				machine[key] = evidence
			}
		}
	}
	for id := range criteria {
		if !candidateEvidenceFresh(latest[id], c.GeneratedAt, now) {
			return fmt.Errorf("candidate latest criterion evidence is stale or future-dated")
		}
		if latest[id].Status != "passed" {
			return fmt.Errorf("candidate criterion lacks current passed evidence")
		}
	}
	if len(machine) == 0 {
		return fmt.Errorf("candidate lacks passed machine-check evidence with an executed command")
	}
	for _, check := range machine {
		if !candidateEvidenceFresh(check, c.GeneratedAt, now) {
			return fmt.Errorf("candidate latest machine-check evidence is stale or future-dated")
		}
		if code, exists := check.Metadata["exitCode"]; exists && check.Status == "passed" && code != "0" {
			return fmt.Errorf("candidate passed machine-check evidence has a nonzero exit code")
		}
		if status, exists := check.Metadata["runStatus"]; exists && check.Status == "passed" && status != "passed" {
			return fmt.Errorf("candidate passed machine-check evidence has an unsuccessful run status")
		}
		if check.Status != "passed" {
			return fmt.Errorf("candidate latest machine-check evidence is not passed")
		}
		if !candidateMachineSourceMatches(check, c) {
			return fmt.Errorf("candidate machine-check proof is not bound to unchanged clean source at the exported commit")
		}
	}
	return nil
}

func candidateEvidenceFresh(e candidateEvidence, generated, now time.Time) bool {
	return !e.CreatedAt.IsZero() && now.Sub(e.CreatedAt) <= candidateMaxAge && e.CreatedAt.Sub(generated) <= candidateClockSkew && e.CreatedAt.Sub(now) <= candidateClockSkew
}
