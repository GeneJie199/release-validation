package guard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Build actual isolated Git worktrees: the same QA command fails on the first
// source, then passes on the fixed source. Exercise the complete candidate gate.
func TestControllerFixCycleSupersedesPriorWorktreeWithoutHidingIndependentChecks(t *testing.T) {
	root := t.TempDir()
	repo, first, second := filepath.Join(root, "repo"), filepath.Join(root, "qa-1"), filepath.Join(root, "qa-2")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return out
	}
	git(repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "initial")
	git(repo, "worktree", "add", "-b", "qa-first", first)
	git(repo, "worktree", "add", "-b", "qa-fixed", second)
	if err := os.WriteFile(filepath.Join(second, "fixed.txt"), []byte("fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(second, "add", "fixed.txt")
	git(second, "commit", "-qm", "repair")
	const command = "git cat-file -e HEAD:fixed.txt"
	firstCheck := exec.Command("git", "-C", first, "cat-file", "-e", "HEAD:fixed.txt")
	if err := firstCheck.Run(); err == nil {
		t.Fatal("initial machine check unexpectedly passed")
	}
	git(second, "cat-file", "-e", "HEAD:fixed.txt")
	head := func(dir string) string { return string(bytes.TrimSpace(git(dir, "rev-parse", "HEAD"))) }
	oldHead, newHead := head(first), head(second)
	var document candidateDocument
	if err := json.Unmarshal([]byte(validCandidateJSON(repo, newHead)), &document); err != nil {
		t.Fatal(err)
	}
	document.Tasks[0].Branch, document.Tasks[0].WorktreePath = "qa-fixed", second
	document.Sources[0].Branch, document.Sources[0].WorktreePath = "qa-fixed", second
	newProof := document.Evidence[0]
	acceptance := document.Evidence[0]
	acceptance.ID = "acceptance-proof"
	acceptance.Metadata = map[string]string{"missionId": "mission", "spec": "devcycle.ai-team/v2alpha1"}
	newProof.CriterionID = ""
	newProof.ID, newProof.CreatedAt = "current-proof", time.Now().Add(-10*time.Second)
	newProof.Metadata["command"], newProof.Metadata["workingDir"] = command, second
	newProof.Metadata["sourceWorktree"], newProof.Metadata["sourceWorktreeAfter"] = second, second
	oldProof := newProof
	oldProof.ID, oldProof.CriterionID, oldProof.Status, oldProof.CreatedAt = "prior-proof", "", "failed", time.Now().UTC()
	oldProof.Metadata = map[string]string{}
	for key, value := range newProof.Metadata {
		oldProof.Metadata[key] = value
	}
	oldProof.Metadata["sourceWorktree"], oldProof.Metadata["sourceWorktreeAfter"], oldProof.Metadata["workingDir"] = first, first, first
	oldProof.Metadata["sourceCommit"], oldProof.Metadata["sourceCommitAfter"] = oldHead, oldHead
	logical := sha256.Sum256([]byte("mission\x00qa-plan\x000\x00" + command))
	setScope := func(proof *candidateEvidence, attempt int) {
		proof.Metadata["logicalCheckId"] = hex.EncodeToString(logical[:])
		proof.Metadata["missionId"], proof.Metadata["planTaskId"] = "mission", "qa-plan"
		proof.Metadata["verificationAttempt"] = strconv.Itoa(attempt)
		proof.Metadata["verificationCheckIndex"] = "0"
		proof.Metadata["executionId"], proof.Metadata["verificationRunId"] = "execution-"+strconv.Itoa(attempt), "run-"+strconv.Itoa(attempt)
	}
	setScope(&oldProof, 1)
	setScope(&newProof, 2)
	document.Evidence = []candidateEvidence{oldProof, newProof, acceptance}
	plan := Plan{ReleaseID: "fix-cycle", Version: "2.0.0-rc.1", CandidateFile: filepath.Join(root, "candidate.json"), Repository: repo, BaseRef: oldHead, TargetRef: newHead, RecoveryChecks: []Check{{Name: "old source exists", Type: "file", Path: filepath.Join(repo, "README.md")}}, Rollback: []string{"restore old commit"}}
	check := func(want string) {
		t.Helper()
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(plan.CandidateFile, data, 0o600); err != nil {
			t.Fatal(err)
		}
		planBytes, _ := json.Marshal(plan)
		if report := Run(context.Background(), plan, planBytes); report.Decision != want {
			t.Fatalf("got %s want %s: %+v", report.Decision, want, report.Results)
		}
	}
	t.Run("higher controller attempt supersedes old cwd even with older timestamp", func(t *testing.T) { check("GO") })
	t.Run("all sources must be covered by verified source ancestry", func(t *testing.T) {
		candidate := &CandidateSummary{Sources: []CandidateSourceSummary{{RepositoryPath: repo, HeadCommit: oldHead}, {RepositoryPath: repo, HeadCommit: newHead}}, MachineChecks: []CandidateMachineCheck{{RepositoryPath: repo, HeadCommit: newHead}}}
		if !candidateSourcesMachineCovered(candidate) {
			t.Fatal("final verified commit did not cover its explorer ancestor")
		}
		tree := string(bytes.TrimSpace(git(repo, "rev-parse", "HEAD^{tree}")))
		unrelated := string(bytes.TrimSpace(git(repo, "commit-tree", tree, "-m", "unverified independent source")))
		candidate.Sources = append(candidate.Sources, CandidateSourceSummary{RepositoryPath: repo, HeadCommit: unrelated})
		if candidateSourcesMachineCovered(candidate) {
			t.Fatal("unrelated unverified source was covered")
		}
	})
	t.Run("newest controller attempt failed", func(t *testing.T) {
		document.Evidence[1].Status = "failed"
		check("NO-GO")
		document.Evidence[1].Status = "passed"
	})
	t.Run("different logical checks remain independent", func(t *testing.T) {
		document.Evidence[1].Metadata["logicalCheckId"] = hex.EncodeToString(make([]byte, 32))
		check("NO-GO")
		document.Evidence[1].Metadata["logicalCheckId"] = hex.EncodeToString(logical[:])
	})
	t.Run("same logical ID cannot hide a different command failure", func(t *testing.T) {
		document.Evidence[0].Metadata["command"] = "independent command"
		check("NO-GO")
		document.Evidence[0].Metadata["command"] = command
	})
	t.Run("incomplete controller scope fails closed", func(t *testing.T) {
		delete(document.Evidence[1].Metadata, "executionId")
		check("NO-GO")
		document.Evidence[1].Metadata["executionId"] = "execution-2"
	})
	t.Run("legacy independent worktrees retain failure", func(t *testing.T) {
		for i := range document.Evidence {
			for _, key := range []string{"logicalCheckId", "planTaskId", "verificationAttempt"} {
				delete(document.Evidence[i].Metadata, key)
			}
		}
		check("NO-GO")
	})
}
