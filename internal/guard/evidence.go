package guard

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func loadCandidate(path string) (*CandidateSummary, Result) {
	r := Result{Name: "DevCycle release candidate", Type: "candidate", Status: "fail", Required: true, Evidence: map[string]any{"path": path}}
	b, err := os.ReadFile(path)
	if err != nil {
		r.Summary = err.Error()
		return nil, r
	}
	var raw struct {
		Spec        string `json:"spec"`
		Requirement struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"requirement"`
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
			Branch         string `json:"branch"`
			HeadCommit     string `json:"headCommit"`
			Clean          bool   `json:"clean"`
		} `json:"sources"`
	}
	if err = json.Unmarshal(b, &raw); err != nil {
		r.Summary = err.Error()
		return nil, r
	}
	c := &CandidateSummary{Spec: raw.Spec, RequirementID: raw.Requirement.ID, RequirementTitle: raw.Requirement.Title, CriteriaTotal: raw.Readiness.CriteriaTotal, CriteriaSatisfied: raw.Readiness.CriteriaSatisfied, CriteriaWithEvidence: raw.Readiness.CriteriaWithEvidence, TasksTotal: raw.Readiness.TasksTotal, TasksDone: raw.Readiness.TasksDone, SourcesTotal: raw.Readiness.SourcesTotal, SourcesClean: raw.Readiness.SourcesClean, Sources: []CandidateSourceSummary{}, Ready: raw.Readiness.Ready}
	for _, source := range raw.Sources {
		c.Sources = append(c.Sources, CandidateSourceSummary{TaskID: source.TaskID, RepositoryPath: source.RepositoryPath, Branch: source.Branch, HeadCommit: source.HeadCommit, Clean: source.Clean})
	}
	r.Evidence["candidate"] = c
	if raw.Spec != "lifecycle-spec/release-candidate/v1" {
		r.Summary = "unsupported release candidate spec"
		return c, r
	}
	if !c.Ready {
		r.Summary = "release candidate is not ready"
		return c, r
	}
	if c.RequirementID == "" || c.RequirementTitle == "" || c.CriteriaTotal == 0 || c.TasksTotal == 0 || c.SourcesTotal == 0 || c.CriteriaSatisfied != c.CriteriaTotal || c.CriteriaWithEvidence != c.CriteriaTotal || c.TasksDone != c.TasksTotal || c.SourcesClean != c.SourcesTotal {
		r.Summary = "release candidate readiness counts are inconsistent"
		return c, r
	}
	if c.SourcesTotal != len(c.Sources) || c.SourcesClean > c.SourcesTotal {
		r.Summary = "release candidate source counts are inconsistent"
		return c, r
	}
	clean := 0
	seenTasks := map[string]bool{}
	for _, source := range c.Sources {
		if source.Clean {
			clean++
		}
		commitBytes, decodeErr := hex.DecodeString(source.HeadCommit)
		if source.TaskID == "" || source.RepositoryPath == "" || source.Branch == "" || seenTasks[source.TaskID] || decodeErr != nil || (len(commitBytes) != 20 && len(commitBytes) != 32) {
			r.Summary = "release candidate contains an invalid source commit"
			return c, r
		}
		seenTasks[source.TaskID] = true
	}
	if clean != c.SourcesClean {
		r.Summary = "release candidate clean source count is inconsistent"
		return c, r
	}
	r.Status = "pass"
	r.Summary = "release candidate is ready"
	return c, r
}

func checkCandidateGit(candidate *CandidateSummary, manifest *GitManifest) Result {
	result := Result{Name: "DevCycle source provenance", Type: "candidate-git", Status: "fail", Required: true, Evidence: map[string]any{"target_commit": manifest.TargetCommit}}
	if candidate == nil || manifest == nil {
		result.Summary = "candidate and Git manifest are both required"
		return result
	}
	for _, source := range candidate.Sources {
		if source.Clean && strings.EqualFold(source.HeadCommit, manifest.TargetCommit) {
			result.Status = "pass"
			result.Summary = "ReleaseGuard target commit matches a clean DevCycle source"
			result.Evidence["source"] = source
			return result
		}
	}
	result.Summary = "ReleaseGuard target commit is not pinned by a clean DevCycle source"
	result.Evidence["candidate_sources"] = candidate.Sources
	return result
}

func buildGitManifest(ctx context.Context, p Plan) (*GitManifest, Result) {
	r := Result{Name: "Git release range", Type: "git", Status: "fail", Required: true, Evidence: map[string]any{}}
	m := &GitManifest{Repository: p.Repository, BaseRef: p.BaseRef, TargetRef: p.TargetRef, Commits: []string{}, ChangedFiles: []string{}, MigrationFiles: []string{}, Configuration: []string{}, UnexpectedFiles: []string{}}
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = p.Repository
		prepareManagedCommand(cmd)
		output := &boundedTail{maximum: maxCommandEvidenceBytes}
		cmd.Stdout = output
		cmd.Stderr = output
		e := cmd.Run()
		if e != nil {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), e, strings.TrimSpace(output.String()))
		}
		return strings.TrimSpace(output.String()), nil
	}
	var err error
	if m.BaseCommit, err = git("rev-parse", p.BaseRef+"^{commit}"); err != nil {
		r.Summary = err.Error()
		return m, r
	}
	if m.TargetCommit, err = git("rev-parse", p.TargetRef+"^{commit}"); err != nil {
		r.Summary = err.Error()
		return m, r
	}
	if _, err = git("merge-base", "--is-ancestor", m.BaseCommit, m.TargetCommit); err != nil {
		r.Summary = "target_ref is not descended from base_ref: " + err.Error()
		return m, r
	}
	status, err := git("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		r.Summary = err.Error()
		return m, r
	}
	m.WorkingTreeClean = status == ""
	if status != "" {
		for _, line := range strings.Split(status, "\n") {
			if len(line) >= 4 {
				m.DirtyFiles = append(m.DirtyFiles, strings.TrimSpace(line[3:]))
			}
		}
	}
	logs, err := git("log", "--format=%H %s", p.BaseRef+".."+p.TargetRef)
	if err != nil {
		r.Summary = err.Error()
		return m, r
	}
	if logs != "" {
		m.Commits = strings.Split(logs, "\n")
	}
	files, err := git("diff", "--name-only", p.BaseRef+".."+p.TargetRef)
	if err != nil {
		r.Summary = err.Error()
		return m, r
	}
	if files != "" {
		m.ChangedFiles = strings.Split(files, "\n")
	}
	for _, f := range m.ChangedFiles {
		migration, configuration := classifySensitiveReleaseFile(f)
		if migration {
			m.MigrationFiles = append(m.MigrationFiles, f)
		}
		if configuration {
			m.Configuration = append(m.Configuration, f)
		}
		if (contains(m.MigrationFiles, f) || contains(m.Configuration, f)) && !matchesAnyPath(p.ExpectedFiles, f) {
			m.UnexpectedFiles = append(m.UnexpectedFiles, f)
		}
	}
	sort.Strings(m.ChangedFiles)
	r.Evidence["manifest"] = m
	if !m.WorkingTreeClean {
		r.Summary = fmt.Sprintf("repository working tree has %d uncommitted paths", len(m.DirtyFiles))
		return m, r
	}
	if len(m.UnexpectedFiles) > 0 {
		r.Summary = fmt.Sprintf("%d migration/configuration files are not declared in expected_files", len(m.UnexpectedFiles))
		return m, r
	}
	r.Status = "pass"
	r.Summary = fmt.Sprintf("%d commits and %d changed files captured", len(m.Commits), len(m.ChangedFiles))
	return m, r
}

func classifySensitiveReleaseFile(file string) (bool, bool) {
	normalized := strings.ToLower(filepath.ToSlash(file))
	segments := strings.Split(strings.Trim(normalized, "/"), "/")
	base := filepath.Base(normalized)
	hasSegment := func(wanted ...string) bool {
		for _, segment := range segments {
			for _, value := range wanted {
				if segment == value {
					return true
				}
			}
		}
		return false
	}
	migration := strings.HasSuffix(base, ".sql") || hasSegment("migration", "migrations")
	configuration := hasSegment("config", "configs", "deploy", "deployment", "helm", "k8s", "manifests") || strings.HasPrefix(normalized, ".github/workflows/") || base == "dockerfile" || base == "compose.yml" || base == "compose.yaml" || strings.HasPrefix(base, "docker-compose.") || strings.HasSuffix(base, ".env.example") || strings.HasSuffix(base, ".service")
	return migration, configuration
}
func matchesAnyPath(patterns []string, value string) bool {
	value = filepath.ToSlash(value)
	for _, raw := range patterns {
		pattern := filepath.ToSlash(strings.TrimSpace(raw))
		if pattern == value {
			return true
		}
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			if value == prefix || strings.HasPrefix(value, prefix+"/") {
				return true
			}
		}
		if matched, _ := path.Match(pattern, value); matched {
			return true
		}
	}
	return false
}
func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}

func checkFleet(ctx context.Context, p FleetPolicy, releaseVersion string) (*FleetEvidence, Result) {
	r := Result{Name: "Fleet node versions", Type: "fleet", Status: "fail", Required: true, Evidence: map[string]any{}}
	e := &FleetEvidence{CheckedAt: time.Now().UTC().Format(time.RFC3339), Nodes: []NodeEvidence{}}
	if p.CenterURL == "" || len(p.Nodes) == 0 {
		r.Summary = "fleet center_url and nodes are required"
		return e, r
	}
	label := p.VersionLabel
	if label == "" {
		label = "version"
	}
	maxAge := time.Duration(p.MaxAgeSeconds) * time.Second
	if maxAge <= 0 {
		maxAge = 5 * time.Minute
	}
	client := &http.Client{Timeout: 15 * time.Second}
	token := ""
	if p.TokenEnv != "" {
		token = os.Getenv(p.TokenEnv)
		if token == "" {
			r.Summary = fmt.Sprintf("fleet token environment variable %s is empty", p.TokenEnv)
			return e, r
		}
	}
	for _, id := range p.Nodes {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.CenterURL, "/")+"/api/v1/nodes/"+url.PathEscape(id), nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			e.Nodes = append(e.Nodes, NodeEvidence{NodeID: id, Health: "unreachable", ExpectedVersion: releaseVersion})
			continue
		}
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			e.Nodes = append(e.Nodes, NodeEvidence{NodeID: id, Health: "invalid_response", ExpectedVersion: releaseVersion})
			continue
		}
		if resp.StatusCode != 200 {
			e.Nodes = append(e.Nodes, NodeEvidence{NodeID: id, Health: "unreachable", ExpectedVersion: releaseVersion})
			continue
		}
		var x struct {
			Summary struct {
				Health     string `json:"health"`
				ObservedAt string `json:"observed_at"`
			} `json:"summary"`
			Report struct {
				Labels map[string]string `json:"labels"`
			} `json:"report"`
		}
		if err := json.Unmarshal(b, &x); err != nil {
			e.Nodes = append(e.Nodes, NodeEvidence{NodeID: id, Health: "invalid_response", ExpectedVersion: releaseVersion})
			continue
		}
		want := p.ExpectedVersions[id]
		if want == "" {
			want = releaseVersion
		}
		actual := x.Report.Labels[label]
		fresh := false
		if t, err := time.Parse(time.RFC3339, x.Summary.ObservedAt); err == nil {
			age := time.Since(t)
			fresh = age >= -time.Minute && age <= maxAge
		}
		health := strings.ToLower(x.Summary.Health)
		healthOK := health != "critical" && health != "stale" && health != ""
		e.Nodes = append(e.Nodes, NodeEvidence{NodeID: id, Health: x.Summary.Health, ObservedAt: x.Summary.ObservedAt, ActualVersion: actual, ExpectedVersion: want, Match: actual == want && fresh && healthOK})
	}
	alertsURL := strings.TrimRight(p.CenterURL, "/") + "/api/v1/alerts?status=open"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, alertsURL, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		r.Evidence["fleet"] = e
		r.Summary = "fleet alerts query failed: " + err.Error()
		return e, r
	}
	var alerts []struct {
		NodeID   string `json:"node_id"`
		Severity string `json:"severity"`
		Status   string `json:"status"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&alerts)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.Evidence["fleet"] = e
		r.Summary = fmt.Sprintf("fleet alerts endpoint returned %s", resp.Status)
		return e, r
	}
	if decodeErr != nil {
		r.Evidence["fleet"] = e
		r.Summary = "fleet alerts response is invalid: " + decodeErr.Error()
		return e, r
	}
	targetNodes := map[string]bool{}
	for _, id := range p.Nodes {
		targetNodes[id] = true
	}
	for _, a := range alerts {
		if targetNodes[a.NodeID] && strings.EqualFold(a.Severity, "critical") && !strings.EqualFold(a.Status, "resolved") {
			e.CriticalAlerts++
		}
	}
	bad := 0
	for _, n := range e.Nodes {
		if !n.Match {
			bad++
		}
	}
	r.Evidence["fleet"] = e
	if bad > 0 {
		r.Summary = fmt.Sprintf("%d of %d nodes have a version, freshness, or health mismatch", bad, len(e.Nodes))
		return e, r
	}
	r.Status = "pass"
	r.Summary = fmt.Sprintf("all %d nodes report the expected version", len(e.Nodes))
	return e, r
}

func observe(ctx context.Context, p Plan, r *Report, progress ProgressFunc) Result {
	start := r.Observation.StartedAt
	result := Result{Name: "post-release observation window", Type: "observation", Status: "fail", Required: true, Evidence: map[string]any{}}
	seconds := p.Observation.Seconds
	if p.Fleet == nil {
		result.Summary = "observation requires fleet policy"
		return result
	}
	poll := p.Observation.PollSeconds
	if poll <= 0 {
		poll = 5
	}
	remaining := time.Until(r.Observation.DeadlineAt)
	if remaining < 0 {
		remaining = 0
	}
	deadline := time.NewTimer(remaining)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Duration(poll) * time.Second)
	defer ticker.Stop()
	samples := append([]FleetEvidence(nil), r.Observation.Samples...)
	check := func() bool {
		after, fleetResult := checkFleet(ctx, *p.Fleet, p.Version)
		r.Manifest.FleetAfter = after
		samples = append(samples, *after)
		r.Observation.Samples = append([]FleetEvidence(nil), samples...)
		result.Evidence["samples"] = samples
		if err := emitProgress(progress, "observing", *r); err != nil {
			result.Summary = "persist observation checkpoint: " + err.Error()
			return false
		}
		if fleetResult.Status == "fail" {
			result.Summary = fleetResult.Summary
			return false
		}
		if after.CriticalAlerts > p.Observation.MaxCriticalAlerts {
			result.Summary = fmt.Sprintf("%d critical alerts exceed allowed %d", after.CriticalAlerts, p.Observation.MaxCriticalAlerts)
			return false
		}
		return true
	}
	if !check() {
		result.DurationMS = time.Since(start).Milliseconds()
		return result
	}
	for {
		select {
		case <-ctx.Done():
			result.Summary = ctx.Err().Error()
			result.DurationMS = time.Since(start).Milliseconds()
			return result
		case <-ticker.C:
			if !check() {
				result.DurationMS = time.Since(start).Milliseconds()
				return result
			}
		case <-deadline.C:
			if !check() {
				result.DurationMS = time.Since(start).Milliseconds()
				return result
			}
			for index, policy := range p.Metrics {
				updated, comparison := compareMetricAfter(ctx, *p.Fleet, policy, r.Manifest.Metrics[index], start, time.Now().UTC())
				comparison.Phase = "observation"
				r.Manifest.Metrics[index] = updated
				r.Results = append(r.Results, comparison)
				if comparison.Status == "fail" && comparison.Required {
					result.Summary = comparison.Summary
					result.DurationMS = time.Since(start).Milliseconds()
					return result
				}
			}
			result.Status = "pass"
			result.Summary = fmt.Sprintf("%d-second observation window passed with %d samples", seconds, len(samples))
			result.DurationMS = time.Since(start).Milliseconds()
			return result
		}
	}
}
