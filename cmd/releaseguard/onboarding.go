package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/GeneJie199/release-validation/internal/guard"
	"github.com/GeneJie199/release-validation/internal/runstore"
)

func initialize(args []string) {
	flags := flag.NewFlagSet("init", flag.ExitOnError)
	repository := flags.String("repository", ".", "Git repository to validate")
	versionLabel := flags.String("version", "", "release version (defaults to the current Git description)")
	releaseID := flags.String("release-id", "", "stable release identity")
	baseRef := flags.String("base-ref", "", "reviewed base Git ref (defaults to the latest tag or parent commit)")
	out := flags.String("out", "release-plan.json", "new plan output; existing files are never replaced")
	_ = flags.Parse(args)
	path, err := writeStarterPlan(*repository, *versionLabel, *releaseID, *baseRef, *out)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(path)
}

func writeStarterPlan(repository, versionLabel, releaseID, baseRef, out string) (string, error) {
	repository = filepath.Clean(repository)
	repositoryRoot, err := gitOutput(repository, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("repository must be a readable Git worktree")
	}
	repositoryRoot, err = filepath.Abs(repositoryRoot)
	if err != nil {
		return "", err
	}
	outAbsolute, err := filepath.Abs(out)
	if err != nil {
		return "", err
	}
	repositoryForPlan, err := filepath.Rel(filepath.Dir(outAbsolute), repositoryRoot)
	if err != nil {
		repositoryForPlan = repositoryRoot
	}
	if strings.TrimSpace(versionLabel) == "" {
		versionLabel, _ = gitOutput(repository, "describe", "--tags", "--always")
		versionLabel = strings.TrimSpace(versionLabel)
	}
	if versionLabel == "" {
		return "", errors.New("version could not be inferred; pass --version")
	}
	if strings.TrimSpace(baseRef) == "" {
		baseRef, _ = gitOutput(repository, "describe", "--tags", "--abbrev=0")
		baseRef = strings.TrimSpace(baseRef)
		if baseRef == "" {
			baseRef, _ = gitOutput(repository, "rev-parse", "HEAD^")
			baseRef = strings.TrimSpace(baseRef)
		}
		if baseRef == "" {
			baseRef = "HEAD"
		}
	}
	if strings.TrimSpace(releaseID) == "" {
		name := strings.NewReplacer(" ", "-", "_", "-", "/", "-", "\\", "-").Replace(filepath.Base(repositoryRoot))
		releaseID = fmt.Sprintf("%s-%s-%s", name, versionLabel, time.Now().UTC().Format("20060102T150405Z"))
	}
	check := guard.Check{Name: "repository diff is well formed", Type: "command", Command: "git diff --check", TimeoutSecs: 60}
	if fileExists(filepath.Join(repositoryRoot, "go.mod")) {
		check = guard.Check{Name: "Go test suite", Type: "command", Command: "go test ./...", TimeoutSecs: 180}
	} else if fileExists(filepath.Join(repositoryRoot, "package.json")) {
		check = guard.Check{Name: "package test suite", Type: "command", Command: "npm test", TimeoutSecs: 300}
	}
	plan := guard.Plan{
		ReleaseID:      releaseID,
		Version:        versionLabel,
		Repository:     repositoryForPlan,
		BaseRef:        baseRef,
		TargetRef:      "HEAD",
		Checks:         []guard.Check{check},
		RecoveryChecks: []guard.Check{{Name: "reviewed base commit retained", Type: "git-ref", Ref: baseRef}},
		Rollback:       []string{"Stop the new release and remove it from traffic", "Restore the reviewed base artifact and configuration", "Rerun health and data-store smoke checks before reopening traffic"},
		Metadata:       map[string]string{"generated_by": "releaseguard init"},
	}
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	file, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("refusing to replace existing plan %s", out)
		}
		return "", err
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(out)
		return "", err
	}
	return out, nil
}

func doctor(args []string) {
	flags := flag.NewFlagSet("doctor", flag.ExitOnError)
	reportPath := flags.String("report", "release-report.json", "immutable report to inspect")
	statePath := flags.String("state", "releaseguard-runs.db", "run database to inspect; missing is a warning")
	serverURL := flags.String("url", "", "optional running decision console URL")
	_ = flags.Parse(args)
	failures := 0
	printCheck := func(status, name, detail string) {
		fmt.Printf("[%s] %s: %s\n", status, name, detail)
		if status == "FAIL" {
			failures++
		}
	}
	printCheck("PASS", "binary", version)
	report, _, err := guard.LoadReport(*reportPath)
	if err != nil {
		printCheck("FAIL", "report", err.Error())
	} else {
		printCheck("PASS", "report", report.ReleaseID+" / "+report.Decision)
		if approval, _, approvalErr := guard.LoadBoundApproval(*reportPath); approvalErr == nil {
			printCheck("PASS", "approval", approval.Decision+" bound to current report")
		} else if errors.Is(approvalErr, os.ErrNotExist) {
			printCheck("WARN", "approval", "not recorded")
		} else {
			printCheck("FAIL", "approval", approvalErr.Error())
		}
	}
	if *statePath != "" {
		store, openErr := runstore.OpenReadOnly(*statePath)
		if errors.Is(openErr, os.ErrNotExist) {
			printCheck("WARN", "run state", "not created")
		} else if openErr != nil {
			printCheck("FAIL", "run state", openErr.Error())
		} else {
			items, listErr := store.List(context.Background(), 1)
			_ = store.Close()
			if listErr != nil {
				printCheck("FAIL", "run state", listErr.Error())
			} else {
				printCheck("PASS", "run state", fmt.Sprintf("readable; %d recent run(s) sampled", len(items)))
			}
		}
	}
	if *serverURL != "" {
		client := &http.Client{Timeout: 5 * time.Second}
		status, requestErr := requestHealth(context.Background(), client, strings.TrimRight(*serverURL, "/")+"/api/v1/health")
		if requestErr == nil {
			printCheck("PASS", "decision console", status)
		} else {
			printCheck("FAIL", "decision console", requestErr.Error())
		}
	}
	if failures > 0 {
		fmt.Fprintf(os.Stderr, "doctor found %d failure(s)\n", failures)
		os.Exit(1)
	}
}

func openWhenReady(ctx context.Context, healthURL, pageURL string) {
	client := &http.Client{Timeout: time.Second}
	for attempt := 0; attempt < 30; attempt++ {
		if _, err := requestHealth(ctx, client, healthURL); err == nil {
			_ = openBrowser(pageURL)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func requestHealth(ctx context.Context, client *http.Client, healthURL string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil {
		return "", readErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("decision console returned %s", response.Status)
	}
	return response.Status, nil
}

func openBrowser(target string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
	case "darwin":
		return exec.Command("open", target).Start()
	default:
		return exec.Command("xdg-open", target).Start()
	}
}

func gitOutput(repository string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", repository}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
