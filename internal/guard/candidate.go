package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
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
	EvidenceComplete json.RawMessage `json:"evidenceComplete,omitempty"`
	TeamRuntime      json.RawMessage `json:"teamRuntime,omitempty"`
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
		if evidence.CreatedAt.IsZero() || now.Sub(evidence.CreatedAt) > candidateMaxAge || evidence.CreatedAt.Sub(c.GeneratedAt) > candidateClockSkew || evidence.CreatedAt.Sub(now) > candidateClockSkew {
			return fmt.Errorf("candidate evidence is stale, missing a timestamp or dated after candidate generation")
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
			linked := sourcedTasks[evidence.TaskID]
			for _, source := range c.Sources {
				linked = linked || candidateSamePath(evidence.Metadata["workingDir"], source.WorktreePath) || candidateSamePath(evidence.Metadata["workingDir"], source.RepositoryPath)
			}
			if !linked {
				return fmt.Errorf("candidate machine check is not linked to a source task or directory")
			}
			key := evidence.Metadata["command"] + "\x00" + evidence.Metadata["workingDir"] + "\x00" + evidence.TaskID
			previous, exists := machine[key]
			if !exists || evidence.CreatedAt.After(previous.CreatedAt) || evidence.CreatedAt.Equal(previous.CreatedAt) && evidence.Status != "passed" {
				machine[key] = evidence
			}
		}
	}
	for id := range criteria {
		if latest[id].Status != "passed" {
			return fmt.Errorf("candidate criterion lacks current passed evidence")
		}
	}
	if len(machine) == 0 {
		return fmt.Errorf("candidate lacks passed machine-check evidence with an executed command")
	}
	for _, check := range machine {
		if check.Status != "passed" {
			return fmt.Errorf("candidate latest machine-check evidence is not passed")
		}
	}
	return nil
}
