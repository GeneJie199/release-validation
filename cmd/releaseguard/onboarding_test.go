package main

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/GeneJie199/release-validation/internal/guard"
)

func TestWriteStarterPlanUsesRelativeRepositoryAndNeverOverwrites(t *testing.T) {
	repository := createTestGitRepository(t)
	out := filepath.Join(t.TempDir(), "release-plan.json")
	path, err := writeStarterPlan(repository, "1.0.0", "release-1", "HEAD^", out)
	if err != nil {
		t.Fatal(err)
	}
	if path != out {
		t.Fatalf("path=%q", path)
	}
	plan, _, err := guard.LoadPlan(out)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ReleaseID != "release-1" || plan.Version != "1.0.0" || len(plan.RecoveryChecks) != 1 || plan.RecoveryChecks[0].Type != "git-ref" {
		t.Fatalf("plan=%+v", plan)
	}
	resolved, _ := filepath.Abs(plan.Repository)
	resolvedInfo, resolvedErr := os.Stat(resolved)
	repositoryInfo, repositoryErr := os.Stat(repository)
	if resolvedErr != nil || repositoryErr != nil || !os.SameFile(resolvedInfo, repositoryInfo) {
		t.Fatalf("repository=%q want=%q", resolved, repository)
	}
	if _, err = writeStarterPlan(repository, "1.0.0", "release-2", "HEAD^", out); err == nil {
		t.Fatal("existing starter plan must not be replaced")
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Fatalf("starter plan stat=%+v err=%v", info, err)
	}
}

func TestRequestHealthRejectsMalformedURL(t *testing.T) {
	client := &http.Client{}
	if _, err := requestHealth(context.Background(), client, "://bad"); err == nil {
		t.Fatal("malformed health URL must return an error")
	}
}

func TestWriteStarterPlanFallsBackToHeadForSingleCommitRepository(t *testing.T) {
	repository := createTestGitRepository(t)

	out := filepath.Join(t.TempDir(), "release-plan.json")
	if _, err := writeStarterPlan(repository, "1.0.0", "release-1", "", out); err != nil {
		t.Fatal(err)
	}
	plan, _, err := guard.LoadPlan(out)
	if err != nil {
		t.Fatal(err)
	}
	if plan.BaseRef != "HEAD" || plan.RecoveryChecks[0].Ref != "HEAD" {
		t.Fatalf("base=%q recovery=%q", plan.BaseRef, plan.RecoveryChecks[0].Ref)
	}
}

func createTestGitRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repository}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	runGit("init", "-q")
	runGit("config", "user.name", "releaseguard-test")
	runGit("config", "user.email", "releaseguard-test@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("# release candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-qm", "initial release candidate")
	return repository
}
