package guard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func LoadPlan(path string) (Plan, []byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, nil, err
	}
	var p Plan
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&p); err != nil {
		return Plan{}, nil, err
	}
	if err = d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return Plan{}, nil, errors.New("plan must contain one JSON object")
		}
		return Plan{}, nil, fmt.Errorf("invalid trailing plan JSON: %w", err)
	}
	base := filepath.Dir(path)
	resolve := func(v string) string {
		if v != "" && !filepath.IsAbs(v) {
			return filepath.Clean(filepath.Join(base, v))
		}
		return v
	}
	p.Repository = resolve(p.Repository)
	p.CandidateFile = resolve(p.CandidateFile)
	p.DriftFile = resolve(p.DriftFile)
	resolveCheck := func(check *Check) {
		check.Path = resolve(check.Path)
		check.BeforePath = resolve(check.BeforePath)
		check.AfterPath = resolve(check.AfterPath)
		check.WorkingDir = resolve(check.WorkingDir)
	}
	for i := range p.Checks {
		resolveCheck(&p.Checks[i])
	}
	for i := range p.RecoveryChecks {
		resolveCheck(&p.RecoveryChecks[i])
	}
	if err := ValidatePlan(&p); err != nil {
		return Plan{}, nil, err
	}
	return p, b, nil
}

func ValidatePlan(p *Plan) error {
	p.ReleaseID = strings.TrimSpace(p.ReleaseID)
	p.Version = strings.TrimSpace(p.Version)
	if p.ReleaseID == "" || p.Version == "" {
		return errors.New("release_id and version are required")
	}
	if len(p.ReleaseID) > 200 || len(p.Version) > 100 {
		return errors.New("release_id or version is too long")
	}
	if len(p.Rollback) == 0 {
		return errors.New("at least one rollback step is required")
	}
	for index := range p.Rollback {
		p.Rollback[index] = strings.TrimSpace(p.Rollback[index])
		if p.Rollback[index] == "" || len(p.Rollback[index]) > 2000 {
			return fmt.Errorf("rollback step %d is empty or too long", index+1)
		}
	}
	if len(p.RecoveryChecks) == 0 {
		return errors.New("at least one recovery_check is required to prove rollback prerequisites")
	}
	if p.Repository != "" && (strings.TrimSpace(p.BaseRef) == "" || strings.TrimSpace(p.TargetRef) == "") {
		return errors.New("base_ref and target_ref are required with repository")
	}
	if p.CandidateFile == "" && p.Repository == "" && len(p.Checks) == 0 && p.DriftFile == "" && p.Fleet == nil {
		return errors.New("release plan must contain at least one delivery verification surface")
	}
	if p.Fleet != nil {
		center, err := url.Parse(strings.TrimSpace(p.Fleet.CenterURL))
		if err != nil || (center.Scheme != "http" && center.Scheme != "https") || center.Host == "" {
			return errors.New("fleet center_url must be an absolute HTTP(S) URL")
		}
		if len(p.Fleet.Nodes) == 0 {
			return errors.New("fleet nodes are required")
		}
		seen := map[string]bool{}
		for index := range p.Fleet.Nodes {
			p.Fleet.Nodes[index] = strings.TrimSpace(p.Fleet.Nodes[index])
			if p.Fleet.Nodes[index] == "" || seen[p.Fleet.Nodes[index]] {
				return fmt.Errorf("fleet node %d is empty or duplicated", index+1)
			}
			seen[p.Fleet.Nodes[index]] = true
		}
		if p.Fleet.MaxAgeSeconds < 0 || p.Fleet.MaxAgeSeconds > 86400 {
			return errors.New("fleet max_age_seconds must be between 0 and 86400")
		}
	}
	if p.Observation != nil {
		if p.Fleet == nil || p.Observation.Seconds <= 0 || p.Observation.Seconds > 7*24*60*60 {
			return errors.New("observation requires fleet and seconds between 1 and 604800")
		}
		if p.Observation.PollSeconds < 0 || p.Observation.PollSeconds > p.Observation.Seconds {
			return errors.New("observation poll_seconds must not exceed the observation window")
		}
		if p.Observation.MaxCriticalAlerts < 0 {
			return errors.New("max_critical_alerts cannot be negative")
		}
	}
	if len(p.Metrics) > 0 && (p.Fleet == nil || p.Observation == nil) {
		return errors.New("metrics require fleet and a positive observation window")
	}
	aggregates := map[string]bool{"avg": true, "min": true, "max": true, "sum": true, "last": true, "rate": true}
	reducers := map[string]bool{"avg": true, "min": true, "max": true, "sum": true, "last": true}
	for index := range p.Metrics {
		metric := &p.Metrics[index]
		metric.Name = strings.TrimSpace(metric.Name)
		metric.Metric = strings.TrimSpace(metric.Metric)
		if metric.Name == "" || metric.Metric == "" {
			return fmt.Errorf("metric policy %d requires name and metric", index+1)
		}
		if metric.Aggregate == "" {
			metric.Aggregate = "avg"
		}
		if !aggregates[metric.Aggregate] {
			return fmt.Errorf("metric policy %q has unsupported aggregate %q", metric.Name, metric.Aggregate)
		}
		if metric.SeriesReduce == "" {
			metric.SeriesReduce = "avg"
		}
		if !reducers[metric.SeriesReduce] {
			return fmt.Errorf("metric policy %q has unsupported series_reduce %q", metric.Name, metric.SeriesReduce)
		}
		if metric.Direction == "" {
			metric.Direction = "lower"
		}
		if metric.Direction != "lower" && metric.Direction != "higher" {
			return fmt.Errorf("metric policy %q direction must be lower or higher", metric.Name)
		}
		if metric.BaselineSeconds <= 0 {
			metric.BaselineSeconds = 300
		}
		if metric.BaselineSeconds > 31*24*60*60 {
			return fmt.Errorf("metric policy %q baseline is too long", metric.Name)
		}
		if metric.MaxRegressionPercent == nil && metric.MaxValue == nil && metric.MinValue == nil {
			return fmt.Errorf("metric policy %q requires at least one threshold", metric.Name)
		}
	}
	return validateChecks(p.Checks, p.RecoveryChecks)
}

func validateChecks(checkGroups ...[]Check) error {
	supported := map[string]bool{"command": true, "playwright": true, "http": true, "file": true, "json": true, "sql": true, "env": true, "compose": true}
	seen := map[string]bool{}
	for groupIndex, checks := range checkGroups {
		for index := range checks {
			check := &checks[index]
			check.Name = strings.TrimSpace(check.Name)
			check.Type = strings.TrimSpace(check.Type)
			if check.Name == "" || len(check.Name) > 300 {
				return fmt.Errorf("check %d has an empty or oversized name", index+1)
			}
			if seen[check.Name] {
				return fmt.Errorf("check name %q is duplicated", check.Name)
			}
			seen[check.Name] = true
			if !supported[check.Type] {
				return fmt.Errorf("check %q has unsupported type %q", check.Name, check.Type)
			}
			if check.TimeoutSecs < 0 || check.TimeoutSecs > 1800 {
				return fmt.Errorf("check %q timeout_seconds must be between 0 and 1800", check.Name)
			}
			if groupIndex == 1 && check.Required != nil && !*check.Required {
				return fmt.Errorf("recovery check %q cannot be optional", check.Name)
			}
			switch check.Type {
			case "command", "playwright":
				if strings.TrimSpace(check.Command) == "" {
					return fmt.Errorf("check %q requires command", check.Name)
				}
			case "http":
				parsed, err := url.Parse(check.URL)
				if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
					return fmt.Errorf("check %q requires an absolute HTTP(S) URL", check.Name)
				}
			case "file", "json":
				if check.Path == "" {
					return fmt.Errorf("check %q requires path", check.Name)
				}
			case "env", "compose":
				if check.BeforePath == "" || check.AfterPath == "" {
					return fmt.Errorf("check %q requires before_path and after_path", check.Name)
				}
			case "sql":
				if check.Query == "" || check.DSNEnv == "" || (check.Driver != "postgres" && check.Driver != "mysql") {
					return fmt.Errorf("check %q requires query, dsn_env, and postgres or mysql driver", check.Name)
				}
			}
		}
	}
	return nil
}

type ProgressFunc func(stage string, report Report) error

func Run(ctx context.Context, p Plan, planBytes []byte) Report {
	return RunWithProgress(ctx, p, planBytes, nil)
}

func RunWithProgress(ctx context.Context, p Plan, planBytes []byte, progress ProgressFunc) Report {
	sum := sha256.Sum256(planBytes)
	now := time.Now().UTC()
	r := Report{SchemaVersion: "releaseguard.report/v1", ReleaseID: p.ReleaseID, Version: p.Version, Decision: "GO", GeneratedAt: now, PlanSHA256: hex.EncodeToString(sum[:]), Rollback: p.Rollback, Manifest: Manifest{ReleaseID: p.ReleaseID, Version: p.Version, CreatedAt: now, Metadata: p.Metadata}}
	if err := ValidatePlan(&p); err != nil {
		r.Results = append(r.Results, Result{Name: "release plan validation", Type: "plan", Phase: "plan", Status: "fail", Required: true, Summary: err.Error()})
		decide(&r)
		_ = emitProgress(progress, "completed", r)
		return r
	}
	checkpoint := func() bool {
		if ctx.Err() != nil {
			r.Decision = "HOLD"
			r.DecisionReason = "validation interrupted; rerun the same plan to restart deterministic checks"
			_ = emitProgress(progress, "checking", r)
			return false
		}
		live := r
		live.Decision = "HOLD"
		live.DecisionReason = "release validation checks are in progress"
		if err := emitProgress(progress, "checking", live); err != nil {
			r.Results = append(r.Results, Result{Name: "persist validation checkpoint", Type: "internal", Phase: "internal", Status: "fail", Required: true, Summary: err.Error()})
			decide(&r)
			return false
		}
		return true
	}
	if !checkpoint() {
		return r
	}
	var candidate *CandidateSummary
	var gitManifest *GitManifest
	for _, check := range p.RecoveryChecks {
		if check.WorkingDir == "" && p.Repository != "" {
			check.WorkingDir = p.Repository
		}
		result := runCheck(ctx, check)
		result.Phase = "recovery"
		r.Results = append(r.Results, result)
		if !checkpoint() {
			return r
		}
	}
	if p.CandidateFile != "" {
		var result Result
		candidate, result = loadCandidate(p.CandidateFile)
		result.Phase = "delivery"
		r.Manifest.Candidate = candidate
		r.Results = append(r.Results, result)
		if !checkpoint() {
			return r
		}
	}
	if p.Repository != "" {
		var result Result
		gitManifest, result = buildGitManifest(ctx, p)
		result.Phase = "delivery"
		r.Manifest.Git = gitManifest
		r.Results = append(r.Results, result)
		if !checkpoint() {
			return r
		}
	}
	if candidate != nil && gitManifest != nil {
		result := checkCandidateGit(candidate, gitManifest)
		result.Phase = "delivery"
		r.Results = append(r.Results, result)
		if !checkpoint() {
			return r
		}
	}
	for _, c := range p.Checks {
		if c.WorkingDir == "" && p.Repository != "" {
			c.WorkingDir = p.Repository
		}
		result := runCheck(ctx, c)
		result.Phase = "verification"
		r.Results = append(r.Results, result)
		if !checkpoint() {
			return r
		}
	}
	if p.DriftFile != "" {
		result := checkDrift(p.DriftFile, p.ExpectedDrifts)
		result.Phase = "infrastructure"
		r.Results = append(r.Results, result)
		if !checkpoint() {
			return r
		}
	}
	if p.Fleet != nil {
		evidence, result := checkFleet(ctx, *p.Fleet, p.Version)
		result.Phase = "observation"
		r.Manifest.FleetBefore = evidence
		r.Results = append(r.Results, result)
		if !checkpoint() {
			return r
		}
	}
	if len(p.Metrics) > 0 {
		for _, policy := range p.Metrics {
			evidence, result := captureMetricBaseline(ctx, *p.Fleet, policy, now)
			result.Phase = "observation"
			r.Manifest.Metrics = append(r.Manifest.Metrics, evidence)
			r.Results = append(r.Results, result)
			if !checkpoint() {
				return r
			}
		}
	}
	if p.Observation != nil && p.Observation.Seconds > 0 {
		r.Observation = &ObservationState{Status: "observing", StartedAt: time.Now().UTC(), DeadlineAt: time.Now().UTC().Add(time.Duration(p.Observation.Seconds) * time.Second), Samples: []FleetEvidence{}}
		r.Decision = "HOLD"
		r.DecisionReason = "post-release observation is in progress"
		if err := emitProgress(progress, "observing", r); err != nil {
			r.Results = append(r.Results, Result{Name: "persist observation checkpoint", Type: "internal", Phase: "internal", Status: "fail", Required: true, Summary: err.Error()})
			decide(&r)
			return r
		}
		result := observe(ctx, p, &r, progress)
		result.Phase = "observation"
		if ctx.Err() != nil {
			r.Decision = "HOLD"
			r.DecisionReason = "observation interrupted; resume the persisted run"
			r.Observation.Status = "observing"
			_ = emitProgress(progress, "observing", r)
			return r
		}
		r.Results = append(r.Results, result)
	}
	decide(&r)
	if r.Observation != nil {
		r.Observation.Status = "completed"
	}
	if err := emitProgress(progress, "completed", r); err != nil {
		r.Results = append(r.Results, Result{Name: "persist completed run", Type: "internal", Phase: "internal", Status: "fail", Required: true, Summary: err.Error()})
		decide(&r)
	}
	return r
}

func ResumeObservation(ctx context.Context, p Plan, report Report, progress ProgressFunc) Report {
	if report.Observation == nil || report.Observation.Status != "observing" {
		report.Results = append(report.Results, Result{Name: "resume observation", Type: "internal", Phase: "internal", Status: "fail", Required: true, Summary: "report has no active observation"})
		decide(&report)
		return report
	}
	result := observe(ctx, p, &report, progress)
	result.Phase = "observation"
	if ctx.Err() != nil {
		report.Decision = "HOLD"
		report.DecisionReason = "observation interrupted; resume the persisted run"
		report.Observation.Status = "observing"
		_ = emitProgress(progress, "observing", report)
		return report
	}
	report.Results = append(report.Results, result)
	report.Observation.Status = "completed"
	decide(&report)
	if err := emitProgress(progress, "completed", report); err != nil {
		report.Results = append(report.Results, Result{Name: "persist completed run", Type: "internal", Phase: "internal", Status: "fail", Required: true, Summary: err.Error()})
		decide(&report)
	}
	return report
}

func emitProgress(progress ProgressFunc, stage string, report Report) error {
	if progress == nil {
		return nil
	}
	return progress(stage, report)
}

func decide(r *Report) {
	failedOptional := 0
	for _, x := range r.Results {
		if x.Status == "fail" && x.Required {
			r.Decision = "NO-GO"
			r.DecisionReason = x.Name + ": " + x.Summary
			return
		}
		if x.Status == "fail" {
			failedOptional++
		}
	}
	if failedOptional > 0 {
		r.Decision = "HOLD"
		r.DecisionReason = fmt.Sprintf("%d optional checks need review", failedOptional)
		return
	}
	r.Decision = "GO"
	r.DecisionReason = "all required release checks passed; final approval remains a human decision"
}

func WriteReport(path string, report Report) error { return writeJSONAtomic(path, report, false) }
func WriteReportWithOverwrite(path string, report Report, overwrite bool) error {
	return writeJSONAtomic(path, report, overwrite)
}
func ArtifactMatches(path string, value any) bool {
	existing, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	expected, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return false
	}
	expected = append(expected, '\n')
	return bytes.Equal(existing, expected)
}
func writeJSONAtomic(path string, value any, overwrite bool) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".releaseguard-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if x := tmp.Close(); err == nil {
		err = x
	}
	if err != nil {
		return err
	}
	if overwrite {
		return replaceFile(name, path)
	}
	if err = os.Link(name, path); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return fmt.Errorf("refusing to overwrite immutable artifact %s", path)
		}
		return fmt.Errorf("publish immutable artifact: %w", err)
	}
	return nil
}

func CreateApproval(reportPath, out, decision, by, note string) error {
	if decision != "GO" && decision != "HOLD" && decision != "NO-GO" {
		return errors.New("decision must be GO, HOLD, or NO-GO")
	}
	by = strings.TrimSpace(by)
	note = strings.TrimSpace(note)
	if by == "" {
		return errors.New("approved_by is required")
	}
	if len(by) > 100 || len(note) > 2000 {
		return errors.New("approved_by or note is too long")
	}
	r, b, err := LoadReport(reportPath)
	if err != nil {
		return err
	}
	automatedRank, ok := decisionRank(r.Decision)
	if !ok {
		return errors.New("report decision is invalid")
	}
	humanRank, _ := decisionRank(decision)
	if humanRank < automatedRank {
		return fmt.Errorf("human decision %s cannot relax automated decision %s", decision, r.Decision)
	}
	sum := sha256.Sum256(b)
	a := Approval{SchemaVersion: "releaseguard.approval/v1", ReleaseID: r.ReleaseID, ReportSHA256: hex.EncodeToString(sum[:]), Decision: decision, ApprovedBy: by, ApprovedAt: time.Now().UTC(), Note: note}
	return writeJSONAtomic(out, a, false)
}

func LoadReport(path string) (Report, []byte, error) {
	var report Report
	b, err := os.ReadFile(path)
	if err != nil {
		return report, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&report); err != nil {
		return report, nil, err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return report, nil, errors.New("report must contain exactly one JSON object")
	}
	if report.SchemaVersion != "releaseguard.report/v1" || strings.TrimSpace(report.ReleaseID) == "" || len(report.PlanSHA256) != 64 {
		return report, nil, errors.New("report identity is invalid")
	}
	if report.Observation != nil && report.Observation.Status == "observing" {
		return report, nil, errors.New("cannot approve a report while observation is active")
	}
	return report, b, nil
}

func decisionRank(decision string) (int, bool) {
	switch decision {
	case "GO":
		return 0, true
	case "HOLD":
		return 1, true
	case "NO-GO":
		return 2, true
	default:
		return 0, false
	}
}
