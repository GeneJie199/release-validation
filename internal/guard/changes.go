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
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"
)

const expectedChangesSpec = "lifecycle-spec/expected-changes/v1"

func loadExpectedChanges(path, releaseID, version string) (coverage *ChangeCoverage, result Result) {
	start := time.Now()
	defer func() { result.DurationMS = time.Since(start).Milliseconds() }()
	result = Result{Name: "expected change declaration", Type: "expected_changes", Status: "fail", Required: true, Evidence: map[string]any{}}
	raw, err := readBoundedFile(path, 16<<20)
	if err != nil {
		result.Summary = err.Error()
		return nil, result
	}
	var document ExpectedChangeDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&document); err != nil {
		result.Summary = "invalid expected changes: " + err.Error()
		return nil, result
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		result.Summary = "expected changes must contain one JSON object"
		return nil, result
	}
	if err = validateExpectedChanges(&document, releaseID, version); err != nil {
		result.Summary = err.Error()
		return nil, result
	}
	digest := sha256.Sum256(raw)
	coverage = &ChangeCoverage{
		Spec:           document.Spec,
		DocumentSHA256: hex.EncodeToString(digest[:]),
		Declared:       document.Changes,
		Observed:       []ObservedChange{},
		Sources:        []ChangeSourceEvidence{},
		Correlations:   []ChangeCorrelation{},
		Unexpected:     []ObservedChange{},
		ExpectedTotal:  len(document.Changes),
	}
	result.Status = "pass"
	result.Summary = fmt.Sprintf("%d versioned expected changes loaded", len(document.Changes))
	result.Evidence["spec"] = document.Spec
	result.Evidence["document_sha256"] = coverage.DocumentSHA256
	result.Evidence["declared_changes"] = len(document.Changes)
	return coverage, result
}

func validateExpectedChanges(document *ExpectedChangeDocument, releaseID, version string) error {
	if document.Spec != expectedChangesSpec || document.Kind != "expected-changes" {
		return fmt.Errorf("expected changes must use %s and kind expected-changes", expectedChangesSpec)
	}
	if document.ReleaseID != releaseID || document.Version != version {
		return errors.New("expected changes release_id and version must match the release plan")
	}
	if _, err := time.Parse(time.RFC3339, document.GeneratedAt); err != nil {
		return errors.New("expected changes generated_at must be RFC3339")
	}
	if len(document.Changes) == 0 || len(document.Changes) > 10000 {
		return errors.New("expected changes must contain between 1 and 10000 declarations")
	}
	allowedSources := map[string]bool{"infrascout": true, "database": true, "fleet": true, "topology": true}
	allowedActions := map[string]bool{"added": true, "removed": true, "changed": true}
	seenIDs := map[string]bool{}
	seenSelectors := map[string]bool{}
	for index := range document.Changes {
		change := &document.Changes[index]
		change.ID = strings.TrimSpace(change.ID)
		change.Source = strings.ToLower(strings.TrimSpace(change.Source))
		change.Action = strings.ToLower(strings.TrimSpace(change.Action))
		change.ResourceID = strings.TrimSpace(change.ResourceID)
		change.ResourceType = strings.TrimSpace(change.ResourceType)
		change.NodeID = strings.TrimSpace(change.NodeID)
		change.Fingerprint = strings.TrimSpace(change.Fingerprint)
		change.Summary = strings.TrimSpace(change.Summary)
		if change.ID == "" || len(change.ID) > 200 || seenIDs[change.ID] {
			return fmt.Errorf("expected change %d has an empty, duplicate, or oversized id", index+1)
		}
		seenIDs[change.ID] = true
		if !allowedSources[change.Source] || !allowedActions[change.Action] {
			return fmt.Errorf("expected change %q has unsupported source or action", change.ID)
		}
		if change.ResourceID == "" || len(change.ResourceID) > 1000 || len(change.ResourceType) > 200 || len(change.NodeID) > 200 || len(change.Fingerprint) > 200 {
			return fmt.Errorf("expected change %q has an invalid resource selector", change.ID)
		}
		if change.Summary == "" || len(change.Summary) > 2000 {
			return fmt.Errorf("expected change %q requires a bounded summary", change.ID)
		}
		selector := strings.Join([]string{change.Source, change.Action, change.ResourceID, change.ResourceType, change.NodeID, change.Fingerprint}, "\x00")
		if seenSelectors[selector] {
			return fmt.Errorf("expected change %q duplicates another change selector", change.ID)
		}
		seenSelectors[selector] = true
		if err := normalizeUniqueStrings(&change.Fields, 100, 300); err != nil {
			return fmt.Errorf("expected change %q fields: %w", change.ID, err)
		}
		if err := normalizeUniqueStrings(&change.EvidenceIDs, 100, 300); err != nil {
			return fmt.Errorf("expected change %q evidence_ids: %w", change.ID, err)
		}
		if err := normalizeUniqueStrings(&change.VerificationChecks, 100, 300); err != nil {
			return fmt.Errorf("expected change %q verification_checks: %w", change.ID, err)
		}
		if err := normalizeUniqueStrings(&change.MetricPolicies, 100, 300); err != nil {
			return fmt.Errorf("expected change %q metric_policies: %w", change.ID, err)
		}
		if err := normalizeUniqueStrings(&change.AffectedNodes, 1000, 300); err != nil {
			return fmt.Errorf("expected change %q affected_nodes: %w", change.ID, err)
		}
	}
	return nil
}

func validateExpectedChangeLinks(plan Plan, coverage *ChangeCoverage, git *GitManifest) Result {
	start := time.Now()
	result := Result{Name: "expected change verification links", Type: "change_links", Status: "fail", Required: true, Evidence: map[string]any{}}
	if coverage == nil {
		result.Summary = "expected change declarations are unavailable"
		return result
	}
	checks := map[string]bool{}
	for _, check := range plan.Checks {
		checks[check.Name] = true
	}
	metrics := map[string]bool{}
	for _, metric := range plan.Metrics {
		metrics[metric.Name] = true
	}
	nodes := map[string]bool{}
	if plan.Fleet != nil {
		for _, node := range plan.Fleet.Nodes {
			nodes[node] = true
		}
	}
	issues := []string{}
	databaseDeclarations := 0
	linkedChecks := 0
	linkedMetrics := 0
	for _, change := range coverage.Declared {
		if change.Source == "database" {
			databaseDeclarations++
			if len(change.VerificationChecks) == 0 {
				issues = append(issues, change.ID+": database change requires at least one verification_checks entry")
			}
		}
		if (change.Source == "fleet" || change.Source == "topology") && change.NodeID == "" && len(change.AffectedNodes) == 0 {
			issues = append(issues, change.ID+": fleet or topology change requires node_id or affected_nodes")
		}
		for _, name := range change.VerificationChecks {
			if !checks[name] {
				issues = append(issues, change.ID+": verification check not found: "+name)
			} else {
				linkedChecks++
			}
		}
		for _, name := range change.MetricPolicies {
			if !metrics[name] {
				issues = append(issues, change.ID+": metric policy not found: "+name)
			} else {
				linkedMetrics++
			}
		}
		for _, node := range change.AffectedNodes {
			if !nodes[node] {
				issues = append(issues, change.ID+": affected FleetScope node not found: "+node)
			}
		}
	}
	if git != nil && len(git.MigrationFiles) > 0 && databaseDeclarations == 0 {
		issues = append(issues, "Git contains migration files but no database expected change is declared")
	}
	result.Evidence["linked_checks"] = linkedChecks
	result.Evidence["linked_metrics"] = linkedMetrics
	result.Evidence["database_declarations"] = databaseDeclarations
	result.Evidence["issues"] = issues
	result.DurationMS = time.Since(start).Milliseconds()
	if len(issues) > 0 {
		result.Summary = fmt.Sprintf("%d expected change verification links are invalid", len(issues))
		return result
	}
	result.Status = "pass"
	result.Summary = fmt.Sprintf("%d expected changes have valid verification, metric, and node references", len(coverage.Declared))
	return result
}

func normalizeUniqueStrings(values *[]string, maxItems, maxLength int) error {
	if len(*values) > maxItems {
		return fmt.Errorf("more than %d values", maxItems)
	}
	seen := map[string]bool{}
	for index := range *values {
		(*values)[index] = strings.TrimSpace((*values)[index])
		value := (*values)[index]
		if value == "" || len(value) > maxLength || seen[value] {
			return errors.New("values must be non-empty, unique, and bounded")
		}
		seen[value] = true
	}
	sort.Strings(*values)
	return nil
}

func expectedChangeRequired(change ExpectedChange) bool {
	return change.Required == nil || *change.Required
}

type driftChangeItem struct {
	ID             string         `json:"id"`
	ResourceID     string         `json:"resourceId"`
	Type           string         `json:"type"`
	Summary        string         `json:"summary"`
	Severity       string         `json:"severity"`
	Before         map[string]any `json:"before"`
	After          map[string]any `json:"after"`
	Fingerprint    string         `json:"fingerprint"`
	Classification string         `json:"classification"`
}

func loadInfraObservedChanges(path string) ([]ObservedChange, ChangeSourceEvidence, Result) {
	start := time.Now()
	result := Result{Name: "InfraScout change artifact", Type: "change_source", Status: "fail", Required: true, Evidence: map[string]any{}}
	raw, err := readBoundedFile(path, 32<<20)
	if err != nil {
		result.Summary = err.Error()
		return nil, ChangeSourceEvidence{}, result
	}
	var document struct {
		Added   []driftChangeItem `json:"added"`
		Removed []driftChangeItem `json:"removed"`
		Changed []driftChangeItem `json:"changed"`
	}
	if err = json.Unmarshal(raw, &document); err != nil {
		result.Summary = "invalid InfraScout drift: " + err.Error()
		return nil, ChangeSourceEvidence{}, result
	}
	observed := make([]ObservedChange, 0, len(document.Added)+len(document.Removed)+len(document.Changed))
	appendItems := func(action string, items []driftChangeItem) {
		for _, item := range items {
			resourceID := item.ID
			if resourceID == "" {
				resourceID = item.ResourceID
			}
			source := "infrascout"
			if strings.HasPrefix(item.Type, "database.") {
				source = "database"
			}
			id := item.Fingerprint
			if id == "" {
				id = source + ":" + action + ":" + resourceID
			}
			observed = append(observed, ObservedChange{ID: id, Source: source, Action: action, ResourceID: resourceID, ResourceType: item.Type, Fields: changedFields(item.Before, item.After), Fingerprint: item.Fingerprint, Severity: strings.ToLower(item.Severity), Summary: item.Summary, Classification: strings.ToLower(item.Classification)})
		}
	}
	appendItems("added", document.Added)
	appendItems("removed", document.Removed)
	appendItems("changed", document.Changed)
	sortObservedChanges(observed)
	digest := sha256.Sum256(raw)
	source := ChangeSourceEvidence{Source: "infrascout", ArtifactSHA256: hex.EncodeToString(digest[:]), CheckedAt: time.Now().UTC().Format(time.RFC3339), Items: len(observed)}
	result.Status = "pass"
	result.Summary = fmt.Sprintf("%d InfraScout and database changes captured", len(observed))
	result.DurationMS = time.Since(start).Milliseconds()
	result.Evidence["artifact_sha256"] = source.ArtifactSHA256
	result.Evidence["observed_changes"] = len(observed)
	return observed, source, result
}

func changedFields(before, after map[string]any) []string {
	set := map[string]bool{}
	for key, value := range before {
		if other, ok := after[key]; !ok || !reflect.DeepEqual(value, other) {
			set[key] = true
		}
	}
	for key, value := range after {
		if other, ok := before[key]; !ok || !reflect.DeepEqual(value, other) {
			set[key] = true
		}
	}
	fields := make([]string, 0, len(set))
	for key := range set {
		fields = append(fields, key)
	}
	sort.Strings(fields)
	return fields
}

func finalizeChangeCoverage(ctx context.Context, plan Plan, report *Report) Result {
	start := time.Now()
	result := Result{Name: "expected change coverage", Type: "change_coverage", Status: "fail", Required: true, Evidence: map[string]any{}}
	coverage := report.Manifest.Changes
	if coverage == nil {
		result.Summary = "expected change evidence is unavailable"
		return result
	}
	if expectedSourcesRequireFleet(coverage.Declared) {
		if plan.Fleet == nil {
			result.Summary = "fleet or topology expected changes require a FleetScope policy"
			return result
		}
		observed, source, err := loadFleetObservedChanges(ctx, *plan.Fleet, plan.ReleaseID)
		if err != nil {
			result.Summary = err.Error()
			return result
		}
		coverage.Observed = append(coverage.Observed, observed...)
		coverage.Sources = append(coverage.Sources, source)
	}
	correlateChanges(coverage)
	result.Evidence["expected_total"] = coverage.ExpectedTotal
	result.Evidence["matched_total"] = coverage.MatchedTotal
	result.Evidence["missing_required"] = coverage.MissingRequired
	result.Evidence["missing_optional"] = coverage.MissingOptional
	result.Evidence["unexpected_total"] = coverage.UnexpectedTotal
	result.DurationMS = time.Since(start).Milliseconds()
	if coverage.MissingRequired > 0 || coverage.UnexpectedTotal > 0 {
		result.Summary = fmt.Sprintf("%d required changes missing and %d observed changes unexpected", coverage.MissingRequired, coverage.UnexpectedTotal)
		return result
	}
	result.Status = "pass"
	if coverage.MissingOptional > 0 {
		result.Summary = fmt.Sprintf("all required changes matched; %d optional changes were not observed", coverage.MissingOptional)
	} else {
		result.Summary = fmt.Sprintf("all %d expected changes matched with no unexpected changes", coverage.MatchedTotal)
	}
	return result
}

func expectedSourcesRequireFleet(changes []ExpectedChange) bool {
	for _, change := range changes {
		if change.Source == "fleet" || change.Source == "topology" {
			return true
		}
	}
	return false
}

func loadFleetObservedChanges(ctx context.Context, policy FleetPolicy, releaseID string) ([]ObservedChange, ChangeSourceEvidence, error) {
	token := ""
	if policy.TokenEnv != "" {
		token = os.Getenv(policy.TokenEnv)
		if token == "" {
			return nil, ChangeSourceEvidence{}, fmt.Errorf("fleet token environment variable %s is empty", policy.TokenEnv)
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(policy.CenterURL, "/")+"/api/v1/changes", nil)
	if err != nil {
		return nil, ChangeSourceEvidence{}, err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return nil, ChangeSourceEvidence{}, fmt.Errorf("FleetScope changes query failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil, ChangeSourceEvidence{}, fmt.Errorf("FleetScope changes endpoint returned %s", response.Status)
	}
	var changes []struct {
		ID             string `json:"id"`
		NodeID         string `json:"node_id"`
		ResourceID     string `json:"resource_id"`
		ResourceType   string `json:"resource_type"`
		Kind           string `json:"kind"`
		Severity       string `json:"severity"`
		Summary        string `json:"summary"`
		Classification string `json:"classification"`
		ReleaseID      string `json:"release_id"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<20))
	if err = decoder.Decode(&changes); err != nil {
		return nil, ChangeSourceEvidence{}, fmt.Errorf("FleetScope changes response is invalid: %w", err)
	}
	targetNodes := map[string]bool{}
	for _, node := range policy.Nodes {
		targetNodes[node] = true
	}
	observed := []ObservedChange{}
	for _, change := range changes {
		if change.ReleaseID != releaseID || !targetNodes[change.NodeID] {
			continue
		}
		source := "fleet"
		if strings.HasPrefix(change.ResourceType, "topology.") {
			source = "topology"
		}
		action := strings.ToLower(change.Kind)
		if action != "added" && action != "removed" && action != "changed" {
			continue
		}
		observed = append(observed, ObservedChange{ID: change.ID, Source: source, Action: action, ResourceID: change.ResourceID, ResourceType: change.ResourceType, NodeID: change.NodeID, Severity: strings.ToLower(change.Severity), Summary: change.Summary, Classification: strings.ToLower(change.Classification), ReleaseID: change.ReleaseID})
	}
	sortObservedChanges(observed)
	source := ChangeSourceEvidence{Source: "fleetscope", CheckedAt: time.Now().UTC().Format(time.RFC3339), Items: len(observed)}
	return observed, source, nil
}

func correlateChanges(coverage *ChangeCoverage) {
	coverage.Correlations = []ChangeCorrelation{}
	coverage.Unexpected = []ObservedChange{}
	coverage.ExpectedTotal = len(coverage.Declared)
	coverage.MatchedTotal = 0
	coverage.MissingRequired = 0
	coverage.MissingOptional = 0
	used := make([]bool, len(coverage.Observed))
	for _, expected := range coverage.Declared {
		required := expectedChangeRequired(expected)
		matches := []int{}
		for index, observed := range coverage.Observed {
			if !used[index] && changeMatches(expected, observed) {
				matches = append(matches, index)
			}
		}
		correlation := ChangeCorrelation{ExpectedID: expected.ID, Required: required, EvidenceIDs: append([]string(nil), expected.EvidenceIDs...), Reasons: []string{}}
		switch len(matches) {
		case 0:
			if required {
				correlation.Status = "missing"
				coverage.MissingRequired++
			} else {
				correlation.Status = "optional-missing"
				coverage.MissingOptional++
			}
			correlation.Reasons = append(correlation.Reasons, "no observed change matched the exact declaration")
		case 1:
			index := matches[0]
			used[index] = true
			correlation.Status = "matched"
			correlation.ObservedID = coverage.Observed[index].ID
			coverage.MatchedTotal++
		default:
			correlation.Status = "ambiguous"
			correlation.Reasons = append(correlation.Reasons, fmt.Sprintf("%d observed changes matched one declaration", len(matches)))
			for _, index := range matches {
				used[index] = true
			}
			if required {
				coverage.MissingRequired++
			} else {
				coverage.MissingOptional++
			}
		}
		coverage.Correlations = append(coverage.Correlations, correlation)
	}
	for index, observed := range coverage.Observed {
		if !used[index] {
			coverage.Unexpected = append(coverage.Unexpected, observed)
		}
	}
	coverage.UnexpectedTotal = len(coverage.Unexpected)
}

func changeMatches(expected ExpectedChange, observed ObservedChange) bool {
	if expected.Source != observed.Source || expected.Action != observed.Action || expected.ResourceID != observed.ResourceID {
		return false
	}
	if expected.ResourceType != "" && expected.ResourceType != observed.ResourceType {
		return false
	}
	if expected.NodeID != "" && expected.NodeID != observed.NodeID {
		return false
	}
	if expected.Fingerprint != "" && expected.Fingerprint != observed.Fingerprint {
		return false
	}
	if observed.Classification == "denied" {
		return false
	}
	for _, field := range expected.Fields {
		if !contains(observed.Fields, field) {
			return false
		}
	}
	return true
}

func sortObservedChanges(changes []ObservedChange) {
	sort.Slice(changes, func(i, j int) bool {
		left := changes[i].Source + "\x00" + changes[i].ResourceID + "\x00" + changes[i].Action + "\x00" + changes[i].ID
		right := changes[j].Source + "\x00" + changes[j].ResourceID + "\x00" + changes[j].Action + "\x00" + changes[j].ID
		return left < right
	})
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("artifact exceeds %d bytes", limit)
	}
	return raw, nil
}
